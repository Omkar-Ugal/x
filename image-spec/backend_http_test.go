// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/platforms"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func serveTestTarball(t *testing.T) (*httptest.Server, digest.Digest) {
	t.Helper()
	tarPath := filepath.Join(t.TempDir(), "image.tar")
	_, err := SaveTarball(t.Context(), tarPath, testImage)
	require.NoError(t, err)
	data, err := os.ReadFile(tarPath)
	require.NoError(t, err)
	dgst := digest.FromBytes(data)

	mux := http.NewServeMux()
	serve := func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "yes" {
			http.Error(w, "missing header", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", MediaTypeOCILayoutTar)
		_, _ = w.Write(data)
	}
	mux.HandleFunc("/ns/direct/latest", serve)
	mux.HandleFunc("/ns/img/@"+dgst.String(), serve)
	mux.HandleFunc("/ns/redirect/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ns/img/@"+dgst.String(), http.StatusFound)
	})
	// http.Redirect would make these absolute, so set the location by hand.
	mux.HandleFunc("/ns/img/relative", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "./@"+dgst.String())
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/ns/other/parent", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "../img/@"+dgst.String())
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/ns/gzip/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", MediaTypeOCILayoutTar)
		w.Header().Set("Content-Encoding", "gzip")
		gw := gzip.NewWriter(w)
		_, _ = gw.Write(data)
		_ = gw.Close()
	})
	mux.HandleFunc("/ns/deflate/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", MediaTypeOCILayoutTar)
		w.Header().Set("Content-Encoding", "deflate")
		zw := zlib.NewWriter(w)
		_, _ = zw.Write(data)
		_ = zw.Close()
	})
	mux.HandleFunc("/ns/bad/@"+digest.FromString("other").String(), serve)
	mux.HandleFunc("/ns/denied/@"+dgst.String(), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	})
	mux.HandleFunc("/ns/soft404/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>not found</html>"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, dgst
}

func TestHTTPAccessor(t *testing.T) {
	srv, dgst := serveTestTarball(t)
	host := srv.Listener.Addr().String()

	tests := []struct {
		name         string
		path         string
		wantLocation string
		wantPinned   bool
	}{
		{
			name:         "redirect",
			path:         "/ns/redirect/latest",
			wantLocation: host + "/ns/img/@" + dgst.String(),
			wantPinned:   true,
		},
		{
			name:         "redirect-relative",
			path:         "/ns/img/relative",
			wantLocation: host + "/ns/img/@" + dgst.String(),
			wantPinned:   true,
		},
		{
			name:         "redirect-parent",
			path:         "/ns/other/parent",
			wantLocation: host + "/ns/img/@" + dgst.String(),
			wantPinned:   true,
		},
		{
			name:         "gzip",
			path:         "/ns/gzip/latest",
			wantLocation: host + "/ns/gzip/latest",
		},
		{
			name:         "deflate",
			path:         "/ns/deflate/latest",
			wantLocation: host + "/ns/deflate/latest",
		},
		{
			name:         "direct",
			path:         "/ns/direct/latest",
			wantLocation: host + "/ns/direct/latest",
		},
		{
			name:         "digest",
			path:         "/ns/img/@" + dgst.String(),
			wantLocation: host + "/ns/img/@" + dgst.String(),
			wantPinned:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			loc, err := ParseLocation("http+oci://" + host + tt.path)
			require.NoError(t, err)

			accessor := NewAccessor(WithRegistryHeaders(http.Header{"X-Test": {"yes"}}))
			res, err := accessor.Resolve(ctx, loc)
			require.NoError(t, err)
			require.Equal(t, tt.wantLocation, res.Location.Path)
			require.Equal(t, tt.wantPinned, res.ResolvedReference.Digest() != "")

			img, err := accessor.Load(ctx, res, platforms.All)
			require.NoError(t, err)
			require.NoError(t, img.Close())
		})
	}
}

func TestLoadTarballRemoteDigestMismatch(t *testing.T) {
	srv, _ := serveTestTarball(t)
	loc, err := ParseLocation("http+oci://" + srv.Listener.Addr().String() + "/ns/bad/@" + digest.FromString("other").String())
	require.NoError(t, err)

	accessor := NewAccessor(WithRegistryHeaders(http.Header{"X-Test": {"yes"}}))
	res, err := accessor.Resolve(t.Context(), loc)
	require.NoError(t, err)
	_, err = accessor.Load(t.Context(), res, platforms.All)
	require.ErrorContains(t, err, "does not match")
}

func TestHTTPAccessorResolveRefused(t *testing.T) {
	srv, dgst := serveTestTarball(t)
	host := srv.Listener.Addr().String()

	tests := []struct {
		name    string
		path    string
		header  http.Header
		wantErr string
	}{
		{
			name:    "digest-denied",
			path:    "/ns/denied/@" + dgst.String(),
			wantErr: "403",
		},
		{
			name:    "digest-without-header",
			path:    "/ns/img/@" + dgst.String(),
			wantErr: "403",
		},
		{
			name:    "soft-404",
			path:    "/ns/soft404/latest",
			header:  http.Header{"X-Test": {"yes"}},
			wantErr: "unexpected content type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc, err := ParseLocation("http+oci://" + host + tt.path)
			require.NoError(t, err)

			_, err = NewAccessor(WithRegistryHeaders(tt.header)).Resolve(t.Context(), loc)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
