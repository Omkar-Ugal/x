// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildkit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unikraft.com/cloud/sdk/platform"
)

// fakePlatform answers every platform request with success, and sends the
// requests to create an instance on the returned channel.
func fakePlatform(t *testing.T) (platform.Client, <-chan platform.CreateInstanceRequest) {
	t.Helper()

	created := make(chan platform.CreateInstanceRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/instances" {
			var req platform.CreateInstanceRequest
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			created <- req
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status": "success"}`))
	}))
	t.Cleanup(srv.Close)

	return platform.NewClient(platform.WithDefaultEndpoint(srv.URL), platform.WithToken("token")), created
}

func TestPlatformCreate(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []Option
		want string
	}{
		{
			name: "the default cache volume is kept half free",
			want: "2147,10737,10737",
		},
		{
			name: "a smaller cache volume is kept half free too",
			opts: []Option{WithCacheSize(4096)},
			want: "429,2147,2147",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, created := fakePlatform(t)
			require.NoError(t, NewPlatform(c, tc.opts...).create(t.Context(), "buildkit-default"))

			req := <-created
			assert.Equal(t, tc.want, req.Env[EnvOCIWorkerGCKeepStorage])
		})
	}
}
