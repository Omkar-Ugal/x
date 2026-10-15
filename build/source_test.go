// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/kraftfile"
)

func TestResolveSourceStateRejects(t *testing.T) {
	st := StateSource{State: llb.Scratch()}

	for _, tc := range []struct {
		name string
		src  FSOpts
		err  string
	}{
		{"outside", FSOpts{Source: st, Path: "../rootfs"}, "outside the source"},
		{"absolute", FSOpts{Source: st, Path: "/rootfs"}, "must be relative"},
		{"tarball", FSOpts{Source: st, Path: "rootfs.tar", Type: kraftfile.SourceTypeTarball}, "cannot come from an LLB state"},
		{"pointer", FSOpts{Source: &st, Path: "rootfs"}, "unsupported source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorContains(t, resolveSource(&tc.src), tc.err)
		})
	}
}

func TestApplyBuildOptsStateSource(t *testing.T) {
	st := llb.Git("https://example.com/repo.git", "")

	for _, tc := range []struct {
		name   string
		rootfs FSOpts
		attrs  map[string]string
	}{
		{
			name:   "dockerfile path",
			rootfs: FSOpts{Path: "app/Dockerfile.prod"},
			attrs:  map[string]string{"filename": "app/Dockerfile.prod"},
		},
		{
			name:   "dockerfile field",
			rootfs: FSOpts{Path: "app", Dockerfile: "docker/Dockerfile"},
			attrs:  map[string]string{"filename": "app/docker/Dockerfile"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.rootfs.Source = StateSource{State: st}
			attrs := map[string]string{}
			localDirs := map[string]string{}
			inputs := map[string]llb.State{}

			err := applyBuildOpts(attrs, localDirs, inputs, new([]session.Attachable), BuildOpts{Rootfs: tc.rootfs})
			require.NoError(t, err)
			require.Equal(t, tc.attrs, attrs)
			require.Empty(t, localDirs)
			require.Len(t, inputs, 2)
			require.Equal(t, st.Output(), inputs["dockerfile"].Output())

			want, err := subdir(st, "app").Marshal(t.Context())
			require.NoError(t, err)
			got, err := inputs["context"].Marshal(t.Context())
			require.NoError(t, err)
			require.Equal(t, want.Def, got.Def)
		})
	}
}
