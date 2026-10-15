// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build_test

import (
	"testing"

	"github.com/moby/buildkit/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	imagespec "unikraft.com/x/image-spec"
	"unikraft.com/x/image-spec/schemes"

	"unikraft.com/x/build"
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
