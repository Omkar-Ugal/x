// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildkit

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/moby/buildkit/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"unikraft.com/cloud/sdk/platform"
)

// DefaultImage is the image new builders run, callers should pin theirs with
// WithImage.
const DefaultImage = "official/utils/buildkit:latest"

const (
	port     = 1234
	cacheDir = "/var/lib/buildkit"

	readyTimeout = 2 * time.Minute
)

// ErrQuota is returned by Platform when the caller's account is over its
// quota, thus the builder cannot be created or started.
var ErrQuota = errors.New("builder quota exceeded")

// Platform runs builders as instances in the caller's Unikraft Cloud account.
type Platform struct {
	c        platform.Client
	image    string
	vcpus    int32
	memoryMB int64
	cacheMB  uint64
	cooldown time.Duration
	insecure bool
}

// Option configures a Platform.
type Option func(*Platform)

// WithImage sets the image that new builders run. Existing builders keep
// theirs.
func WithImage(ref string) Option {
	return func(p *Platform) { p.image = ref }
}

// WithVCPUs sets the vCPUs of new builders, 4 by default.
func WithVCPUs(n int32) Option {
	return func(p *Platform) { p.vcpus = n }
}

// WithMemory sets the memory of new builders in MiB, 8192 by default.
func WithMemory(mb int64) Option {
	return func(p *Platform) { p.memoryMB = mb }
}

// WithCacheSize sets the size of new builders' cache volume in MiB, 20480 by
// default. The build cache is kept to half of it, see gcKeepStorage.
func WithCacheSize(mb uint64) Option {
	return func(p *Platform) { p.cacheMB = mb }
}

// WithCooldown sets how long new builders stay idle before they scale to zero,
// 5 minutes by default.
func WithCooldown(d time.Duration) Option {
	return func(p *Platform) { p.cooldown = d }
}

func WithInsecure(insecure bool) Option {
	return func(p *Platform) { p.insecure = insecure }
}

// NewPlatform returns a Platform that manages builders through c, which
// carries the caller's token and metro.
func NewPlatform(c platform.Client, opts ...Option) *Platform {
	p := &Platform{
		c:        c,
		image:    DefaultImage,
		vcpus:    4,
		memoryMB: 8192,
		cacheMB:  20480,
		cooldown: 5 * time.Minute,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Acquire implements Backend. It creates the builder for key with a cache
// volume if there is none, and starts it if it was stopped or in standby.
func (p *Platform) Acquire(ctx context.Context, key Key) (*Daemon, error) {
	name := "buildkit-" + cmp.Or(key.Name, "default")
	// The builder may be booting, waking from standby or shutting down.
	wctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()

	inst, err := p.instance(wctx, name)
	if err != nil {
		return nil, err
	}

	if inst.ServiceGroup == nil || len(inst.ServiceGroup.Domains) == 0 {
		return nil, fmt.Errorf("builder %s has no domain, delete it to recreate it", name)
	}
	t, err := clientTLS(inst.Env)
	if err != nil {
		return nil, fmt.Errorf("builder %s: %w, delete it to recreate it", name, err)
	}
	addr := net.JoinHostPort(strings.TrimSuffix(inst.ServiceGroup.Domains[0].Fqdn, "."), strconv.Itoa(port))
	edge, err := edgeDialer(addr, p.insecure)
	if err != nil {
		return nil, fmt.Errorf("connecting to builder %s: %w", name, err)
	}
	c, err := dial(ctx, addr, t, edge)
	if err != nil {
		return nil, fmt.Errorf("connecting to builder %s: %w", name, err)
	}

	if err := c.Wait(wctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("waiting for builder %s: %w", name, err)
	}
	return &Daemon{Client: c, Addr: addr, TLS: t, edge: edge}, nil
}

// instance returns the named builder instance, creating it if there is none
// and starting it if it was stopped.
func (p *Platform) instance(ctx context.Context, name string) (*platform.Instance, error) {
	inst, err := p.get(ctx, name)
	if err != nil {
		return nil, err
	}
	if inst == nil {
		cerr := p.create(ctx, name)
		// Look again either way, a concurrent Acquire may have created it.
		if inst, err = p.get(ctx, name); inst == nil {
			return nil, cmp.Or(cerr, err, fmt.Errorf("builder %s not found after creating it", name))
		}
	}

	// Unlike standby, shutting down refuses connections and stopped ignores them.
	for inst.State == platform.InstanceStateDraining || inst.State == platform.InstanceStateStopping {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for builder %s to stop: %w", name, context.Cause(ctx))
		case <-time.After(time.Second):
		}
		if inst, err = p.get(ctx, name); err != nil {
			return nil, err
		}
		if inst == nil {
			return nil, fmt.Errorf("builder %s was deleted while stopping", name)
		}
	}
	// The edge also wakes a builder in standby, but only a start tells why the
	// platform refuses it.
	if inst.State == platform.InstanceStateStopped || inst.State == platform.InstanceStateStandby {
		_, err := p.c.StartInstanceByUUID(ctx, inst.Uuid, platform.StartInstanceByUUIDRequestBody{})
		// A concurrent Acquire may have started it.
		if err != nil && !platform.ErrorContainsOnly(err, platform.APIHTTPErrorFailedWrongVMState) {
			return nil, fmt.Errorf("starting builder %s: %w", name, quota(err))
		}
	}
	return inst, nil
}

// get returns the named builder instance, or nil if there is none.
func (p *Platform) get(ctx context.Context, name string) (*platform.Instance, error) {
	resp, err := p.c.GetInstances(ctx, []platform.NameOrUUID{{Name: &name}}, platform.GetInstancesOpts{})
	if platform.ErrorContainsOnly(err, platform.APIHTTPErrorNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting builder %s: %w", name, err)
	}
	if resp.Data == nil || len(resp.Data.Instances) == 0 {
		return nil, nil
	}
	return &resp.Data.Instances[0], nil
}

// create creates the named builder instance and its cache volume, with
// newly generated mTLS certificates and keys in its env.
func (p *Platform) create(ctx context.Context, name string) error {
	vol := name + "-cache"
	_, err := p.c.CreateVolume(ctx, platform.CreateVolumeRequest{Name: &vol, SizeMb: new(p.cacheMB)})
	if err != nil && !platform.ErrorContainsOnly(err, platform.APIHTTPErrorAlreadyExists) {
		return fmt.Errorf("creating cache volume %s: %w", vol, quota(err))
	}

	env, err := generateCerts()
	if err != nil {
		return fmt.Errorf("generating certificates: %w", err)
	}
	env[EnvOCIWorkerGCKeepStorage] = gcKeepStorage(p.cacheMB)
	_, err = p.c.CreateInstance(ctx, platform.CreateInstanceRequest{
		Name:      &name,
		Image:     platform.ImageReference(p.image),
		Vcpus:     new(p.vcpus),
		MemoryMb:  new(p.memoryMB),
		Autostart: new(true),
		Env:       env,
		Volumes:   []platform.CreateInstanceRequestVolume{{Name: &vol, At: cacheDir}},
		ServiceGroup: &platform.CreateInstanceRequestServiceGroup{
			Services: []platform.Service{{
				Port:            port,
				DestinationPort: new(uint32(port)),
				Handlers:        []platform.ConnectionHandler{platform.ConnectionHandlerTls},
			}},
		},
		ScaleToZero: &platform.CreateInstanceScaleToZero{
			Policy:         new(platform.InstanceScaleToZeroPolicyOn),
			CooldownTimeMs: new(int32(p.cooldown.Milliseconds())),
		},
	})
	if err != nil {
		return fmt.Errorf("creating builder %s: %w", name, quota(err))
	}
	return nil
}

// quota adds ErrQuota to err if the platform refused the request because of
// the caller's quota.
func quota(err error) error {
	if platform.ErrorContains(err, platform.APIHTTPErrorQuota) {
		return fmt.Errorf("%w: %w", ErrQuota, err)
	}
	return err
}

// gcKeepStorage returns the garbage collection limits for a cache volume of
// the given size in MiB, in the "reserved,free,max" MB form of
// EnvOCIWorkerGCKeepStorage.
func gcKeepStorage(cacheMiB uint64) string {
	mb := cacheMiB * (1 << 20) / 1_000_000
	return fmt.Sprintf("%d,%d,%d", mb/10, mb/2, mb/2)
}

// dial connects to buildkitd at addr with buildkit's mTLS inside the TLS that
// the platform's edge terminates.
func dial(ctx context.Context, addr string, t TLS, edge func(context.Context) (net.Conn, error)) (*client.Client, error) {
	inner, err := t.config()
	if err != nil {
		return nil, err
	}
	return client.New(ctx, "tcp://"+addr,
		client.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return edge(ctx)
		}),
		client.WithGRPCDialOption(grpc.WithTransportCredentials(credentials.NewTLS(inner))),
		client.WithGRPCDialOption(grpc.WithAuthority(t.ServerName)),
	)
}

// edgeDialer returns a function that opens a connection to addr through the
// TLS that the platform's edge terminates.
func edgeDialer(addr string, insecure bool) (func(context.Context) (net.Conn, error), error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	d := &tls.Dialer{Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure}}
	return func(ctx context.Context) (net.Conn, error) {
		return d.DialContext(ctx, "tcp", addr)
	}, nil
}
