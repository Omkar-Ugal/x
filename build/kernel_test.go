// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/moby/buildkit/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/image-spec/schemes"
	"unikraft.com/x/kraftfile"

	"unikraft.com/x/build"
	"unikraft.com/x/build/internal/buildfs"
)

// stepRecorder keeps the name of every step reported to it, in the order
// they started.
type stepRecorder struct {
	names []string
}

func (r *stepRecorder) Write(ss *client.SolveStatus) {
	for _, v := range ss.Vertexes {
		if v.Completed == nil {
			r.names = append(r.names, v.Name)
		}
	}
}

func TestKernelResolveIsAStep(t *testing.T) {
	host := startRegistry(t)
	b := registryBuilder(alice)
	ref := host + "/official/base-compat"
	_, err := b.Save(t.Context(), &imagespec.Location{Scheme: schemes.OCI, Path: ref}, testImages(fcIntel)...)
	require.NoError(t, err)

	var steps stepRecorder
	b.Progress = &steps
	imgs, err := b.Build(t.Context(), build.BuildOpts{
		Runtime:  ref,
		Platform: []ocispec.Platform{fcIntel},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, img := range imgs {
			_ = img.Close()
		}
	})

	assert.Equal(t, []string{"[kernel] resolve " + ref}, steps.names)
}

func TestBuildIsDatedByItsNewestPart(t *testing.T) {
	older := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)

	host := startRegistry(t)
	b := registryBuilder(alice)
	push := func(repo string, created time.Time, part imagespec.NewImageOpt) string {
		ref := host + "/" + repo
		_, err := b.Save(t.Context(), &imagespec.Location{Scheme: schemes.OCI, Path: ref}, imagespec.NewImage(
			imagespec.WithPlatform(fcIntel),
			imagespec.WithImageMetadata(imagespec.ImageMetadata{Created: &created}),
			part,
		))
		require.NoError(t, err)
		return ref
	}
	buildDate := func(t *testing.T, opts build.BuildOpts) time.Time {
		opts.Platform = []ocispec.Platform{fcIntel}
		imgs, err := b.Build(t.Context(), opts)
		require.NoError(t, err)
		t.Cleanup(func() {
			for _, img := range imgs {
				_ = img.Close()
			}
		})
		require.Len(t, imgs, 1)
		created := imgs[0].Metadata().Created
		require.NotNil(t, created)
		return *created
	}

	var initrd bytes.Buffer
	require.NoError(t, buildfs.CreateCPIO(t.Context(), &initrd, fstest.MapFS{
		"hello.txt": {Data: []byte("hello\n"), Mode: 0o644},
	}))
	kernel := imagespec.WithKernel(imagespec.NewStaticFile("kernel", []byte("kernel")))
	rootfs := imagespec.WithInitrd(imagespec.NewStaticFile("initrd", initrd.Bytes()))

	for _, tc := range []struct {
		name           string
		kernel, rootfs time.Time
	}{
		{name: "newer kernel", kernel: newer, rootfs: older},
		{name: "newer rootfs", kernel: older, rootfs: newer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			created := buildDate(t, build.BuildOpts{
				Runtime: push("runtime", tc.kernel, kernel),
				Rootfs: build.FSOpts{
					Path: push("rootfs", tc.rootfs, rootfs),
					Type: kraftfile.SourceTypeOCI,
				},
			})
			assert.True(t, newer.Equal(created), "want %s, got %s", newer, created)
		})
	}

	t.Run("undated rootfs", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0o644))

		start := time.Now()
		created := buildDate(t, build.BuildOpts{
			Runtime: push("runtime", older, kernel),
			Rootfs:  build.FSOpts{Path: dir, Type: kraftfile.SourceTypeDirectory},
		})
		assert.False(t, created.Before(start), "a rootfs built now must date the image now")
	})
}
