// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/kraftfile"
)

func TestRootfsOCINilAccessor(t *testing.T) {
	_, err := Builder{
		Images: func(context.Context) (*imagespec.Accessor, error) {
			return nil, nil
		},
	}.BuildRootfs(t.Context(), BuildOpts{
		Rootfs: FSOpts{
			Path: "unikraft.io/official/base-compat",
			Type: kraftfile.SourceTypeOCI,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.ErrorContains(t, err, "image callback gave no accessor")
}

func TestRootfsDockerfileWithoutBuildkit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Dockerfile")
	require.NoError(t, os.WriteFile(path, []byte("FROM scratch\n"), 0o644))

	_, err := Builder{}.BuildRootfs(t.Context(), BuildOpts{
		Rootfs: FSOpts{
			Path:   path,
			Type:   kraftfile.SourceTypeDockerfile,
			Format: kraftfile.FsTypeCpio,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.ErrorContains(t, err, "no buildkit connector configured")
}

// TestBuildStateSourcesNeedBuildkit asserts that the sources in a
// StateSource are not read from the host.
func TestBuildStateSourcesNeedBuildkit(t *testing.T) {
	t.Chdir(writeTestDirectory(t))
	st := StateSource{State: llb.Scratch()}

	for name, opts := range map[string]BuildOpts{
		"dir rom": {
			Roms: []FSOpts{{Source: st, Path: "subdir", Type: kraftfile.SourceTypeDirectory}},
		},
		"dockerfile rootfs": {
			Rootfs: FSOpts{Source: st, Path: "Dockerfile", Type: kraftfile.SourceTypeDockerfile},
		},
	} {
		t.Run(name, func(t *testing.T) {
			opts.Platform = []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}}
			_, err := Builder{}.Build(t.Context(), opts)
			require.ErrorContains(t, err, "no buildkit connector configured")
		})
	}
}

func TestRootfsDockerfileSessionError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Dockerfile")
	require.NoError(t, os.WriteFile(path, []byte("FROM scratch\n"), 0o644))

	_, err := Builder{
		Buildkit: func(context.Context) (*client.Client, func(), error) {
			return nil, nil, errors.New("buildkit called")
		},
		Session: func(context.Context) ([]session.Attachable, error) {
			return nil, errors.New("no profile")
		},
	}.BuildRootfs(t.Context(), BuildOpts{
		Rootfs: FSOpts{
			Path:   path,
			Type:   kraftfile.SourceTypeDockerfile,
			Format: kraftfile.FsTypeCpio,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.ErrorContains(t, err, "no profile")
}

func TestRootfsLocalSourcesSkipDependencies(t *testing.T) {
	b := Builder{
		Images: func(context.Context) (*imagespec.Accessor, error) {
			return nil, errors.New("images called")
		},
		Buildkit: func(context.Context) (*client.Client, func(), error) {
			return nil, nil, errors.New("buildkit called")
		},
	}

	for _, src := range []FSOpts{
		{Path: writeTestDirectory(t), Type: kraftfile.SourceTypeDirectory},
		{Path: writeTestTarballFile(t), Type: kraftfile.SourceTypeTarball},
		{Path: writeTestCpioFile(t), Type: kraftfile.SourceTypeCpio},
	} {
		t.Run(string(src.Type), func(t *testing.T) {
			imgs, err := b.BuildRootfs(t.Context(), BuildOpts{
				Rootfs:   src,
				Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
			})
			require.NoError(t, err)
			for _, img := range imgs {
				_ = img.Close()
			}
		})
	}
}

func TestRootfsTempDir(t *testing.T) {
	dir := t.TempDir()
	imgs, err := Builder{TempDir: dir}.BuildRootfs(t.Context(), BuildOpts{
		Rootfs: FSOpts{
			Path:   writeTestDirectory(t),
			Type:   kraftfile.SourceTypeDirectory,
			Format: kraftfile.FsTypeCpio,
		},
		Platform: []ocispec.Platform{{OS: "fc", Architecture: "x86_64"}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, img := range imgs {
			_ = img.Close()
		}
	})

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
