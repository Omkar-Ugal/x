// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/moby/buildkit/client/llb"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"unikraft.com/x/kraftfile"
)

// Source is the directory tree that the path of a rootfs or a rom resolves in.
type Source interface {
	isSource()
}

// LocalSource is a directory on the host.
type LocalSource struct {
	Dir string
}

// StateSource is a directory tree that BuildKit solves from LLB. The build
// does not apply a .dockerignore file to it.
type StateSource struct {
	State llb.State
}

func (LocalSource) isSource() {}

func (StateSource) isSource() {}

// join returns p as a host path, resolving a relative p in s.Dir.
func (s LocalSource) join(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.Dir, p)
}

// localize turns a LocalSource into a host path and rejects an unknown
// Source. An OCI path names an image, thus it drops its Source.
func localize(o *FSOpts) error {
	if o.DockerfileSource != nil {
		_, state := o.Source.(StateSource)
		_, dockerfileState := o.DockerfileSource.(StateSource)
		if !state || !dockerfileState {
			return fmt.Errorf("a dockerfile source and its source must both be LLB states")
		}
	}

	switch src := o.Source.(type) {
	case nil, StateSource:
	case LocalSource:
		if o.Type != kraftfile.SourceTypeOCI {
			o.Path = src.join(o.Path)
		}
		o.Source = nil
	default:
		return fmt.Errorf("unsupported source %T", o.Source)
	}

	if o.Type == kraftfile.SourceTypeOCI {
		o.Source = nil
	}
	return nil
}

// resolveState cleans the path in a StateSource and makes sure that its type
// can come from LLB.
func resolveState(o *FSOpts) error {
	p, err := cleanSourcePath(o.Path)
	if err != nil {
		return err
	}
	o.Path = p

	switch {
	case o.DockerfileSource != nil:
		if o.Dockerfile == "" {
			return fmt.Errorf("a dockerfile source needs a dockerfile")
		}
		if _, err := cleanSourcePath(o.Dockerfile); err != nil {
			return err
		}
	case o.Dockerfile != "":
		if _, err := cleanSourcePath(path.Join(o.Path, o.Dockerfile)); err != nil {
			return err
		}
	}

	switch o.Type {
	case kraftfile.SourceTypeDirectory, kraftfile.SourceTypeDockerfile:
		return nil
	}
	return fmt.Errorf("source type %q cannot come from an LLB state", o.Type)
}

// cleanSourcePath returns p as a clean path in a StateSource, with "." for
// its root.
func cleanSourcePath(p string) (string, error) {
	p = path.Clean(p)
	switch {
	case path.IsAbs(p):
		return "", fmt.Errorf("path %q must be relative to the source", p)
	case p == ".." || strings.HasPrefix(p, "../"):
		return "", fmt.Errorf("path %q is outside the source", p)
	}
	return p, nil
}

// subdir returns the directory p of st as the root of a new state. When p is
// a symlink, the state holds the directory that it points to.
func subdir(st llb.State, p string) llb.State {
	if p == "." {
		return st
	}
	return llb.Scratch().File(llb.Copy(st, "/"+p, "/", &llb.CopyInfo{
		CopyDirContentsOnly: true,
		FollowSymlinks:      true,
	}))
}

// marshalState marshals st for BuildKit to solve on platform p.
func marshalState(ctx context.Context, st llb.State, p ocispec.Platform, noCache bool) (*llb.Definition, error) {
	constraints := []llb.ConstraintsOpt{llb.Platform(p)}
	if noCache {
		constraints = append(constraints, llb.IgnoreCache)
	}
	return st.Marshal(ctx, constraints...)
}
