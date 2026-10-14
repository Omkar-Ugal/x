// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package build makes Unikraft images from a runtime, a rootfs and roms.
package build

import (
	"context"
	"errors"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/progress/progresswriter"
	imagespec "unikraft.com/x/image-spec"
)

// Builder makes Unikraft images. It calls each of its functions only when a
// step needs it.
type Builder struct {
	// Images gives the accessor for runtime kernels and OCI rootfs sources. When
	// it is nil, the build uses a default accessor without credentials.
	Images func(ctx context.Context) (*imagespec.Accessor, error)

	// Buildkit connects one time for each rootfs that needs a daemon. The build
	// calls cleanup, which can be nil, and does not close the client.
	Buildkit func(ctx context.Context) (*client.Client, func(), error)

	// Session gives the attachables for each solve, such as registry auth. When
	// it is nil, BuildKit pulls images without credentials.
	Session func(ctx context.Context) ([]session.Attachable, error)

	// Progress gives a writer for one solve, with prefix on each vertex. When
	// it is nil, the build discards the progress.
	Progress func(ctx context.Context, prefix string) (progresswriter.Writer, error)

	// TempDir holds the intermediate files. Empty means os.TempDir.
	TempDir string
}

func (b Builder) images(ctx context.Context) (*imagespec.Accessor, error) {
	if b.Images == nil {
		return imagespec.NewAccessor(), nil
	}
	access, err := b.Images(ctx)
	if err != nil {
		return nil, err
	}
	if access == nil {
		return nil, errors.New("image callback gave no accessor")
	}
	return access, nil
}

// discardProgress reads and drops the solve status, because the client blocks
// when nothing reads the channel.
type discardProgress struct {
	status chan *client.SolveStatus
	done   chan struct{}
}

func newDiscardProgress() progresswriter.Writer {
	p := &discardProgress{
		status: make(chan *client.SolveStatus),
		done:   make(chan struct{}),
	}
	go func() {
		defer close(p.done)
		for range p.status {
		}
	}()
	return p
}

func (p *discardProgress) Done() <-chan struct{} { return p.done }

func (p *discardProgress) Err() error { return nil }

func (p *discardProgress) Status() chan *client.SolveStatus { return p.status }
