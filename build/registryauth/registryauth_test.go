// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package registryauth_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/session/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unikraft.com/x/build/registryauth"
)

// token is a Unikraft Cloud token, "alice:s3cret" in base64.
const token = "YWxpY2U6czNjcmV0"

func TestFromToken(t *testing.T) {
	creds, err := registryauth.FromToken(token)
	require.NoError(t, err)

	for _, tc := range []struct {
		name, host, user, secret string
	}{
		{"the registry gets the user and password", "index.unikraft.io", "alice", "s3cret"},
		{"so does its short name", "unikraft.io", "alice", "s3cret"},
		{"another registry gets nothing", "ghcr.io", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user, secret, err := creds(tc.host)
			require.NoError(t, err)
			assert.Equal(t, tc.user, user)
			assert.Equal(t, tc.secret, secret)
		})
	}
}

func TestFromTokenMalformed(t *testing.T) {
	for _, tc := range []struct {
		name, token string
	}{
		{"not base64", "not base64!"},
		{"no separator", "czNjcmV0"},   // "s3cret"
		{"empty user", "OnMzY3JldA=="}, // ":s3cret"
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := registryauth.FromToken(tc.token)
			assert.Error(t, err)
		})
	}
}

// daemon opens a session that serves creds to a session manager, as the
// client does to buildkitd, and returns the manager and the session's group.
func daemon(t *testing.T, creds registryauth.Credentials) (*session.Manager, session.Group) {
	t.Helper()

	sm, err := session.NewManager()
	require.NoError(t, err)
	s, err := session.NewSession(t.Context(), "")
	require.NoError(t, err)
	s.Allow(creds.Attachable())
	t.Cleanup(func() { _ = s.Close() })

	dialer := testutil.TestStream(func(ctx context.Context, conn net.Conn, meta map[string][]string) error {
		return sm.HandleConn(ctx, conn, meta)
	})
	go func() { _ = s.Run(t.Context(), session.Dialer(dialer)) }()

	return sm, session.NewGroup(s.ID())
}

// startRealm serves a token endpoint that issues a token to alice with her
// password, and returns its URL. With oauth it takes the OAuth POST as well
// as the GET, without it only the GET, like a Harbor.
func startRealm(t *testing.T, oauth bool) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var user, pass string
		switch {
		case r.Method == http.MethodPost && oauth:
			user, pass = r.FormValue("username"), r.FormValue("password")
		case r.Method == http.MethodGet:
			user, pass, _ = r.BasicAuth()
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if user != "alice" || pass != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		assert.Equal(t, "registry", r.FormValue("service"))
		assert.Equal(t, "repository:acme/app:push", r.FormValue("scope"))
		if r.Method == http.MethodPost {
			_, _ = fmt.Fprint(w, `{"access_token":"0auth","expires_in":300}`)
		} else {
			_, _ = fmt.Fprint(w, `{"token":"t0k3n","expires_in":300}`)
		}
	}))
	t.Cleanup(srv.Close)

	return srv.URL + "/token"
}

func TestAttachable(t *testing.T) {
	creds, err := registryauth.FromToken(token)
	require.NoError(t, err)
	sm, g := daemon(t, creds)
	ctx := t.Context()

	t.Run("shares the credentials of the registry", func(t *testing.T) {
		_, user, secret, err := auth.CredentialsFunc(ctx, sm, g)("index.unikraft.io")
		require.NoError(t, err)
		assert.Equal(t, "alice", user)
		assert.Equal(t, "s3cret", secret)
	})

	t.Run("leaves other registries to the daemon", func(t *testing.T) {
		_, pubKey, err := auth.GetTokenAuthority(ctx, "ghcr.io", sm, g)
		require.NoError(t, err)
		assert.Nil(t, pubKey, "the daemon fetches anonymous tokens itself")
	})

	t.Run("fetches the registry's tokens itself", func(t *testing.T) {
		_, pubKey, err := auth.GetTokenAuthority(ctx, "index.unikraft.io", sm, g)
		require.NoError(t, err)
		require.NotNil(t, pubKey, "the attachable signs token requests")
		_, ok, err := auth.VerifyTokenAuthority(ctx, "index.unikraft.io", pubKey, sm, g)
		require.NoError(t, err)
		assert.True(t, ok)

		for _, tc := range []struct {
			name  string
			oauth bool
			token string
		}{
			{"from a realm with the OAuth endpoint", true, "0auth"},
			{"from one without it, like Harbor", false, "t0k3n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				resp, err := auth.FetchToken(ctx, &auth.FetchTokenRequest{
					ClientID: "buildkit-client",
					Host:     "index.unikraft.io",
					Realm:    startRealm(t, tc.oauth),
					Service:  "registry",
					Scopes:   []string{"repository:acme/app:push"},
				}, sm, g)
				require.NoError(t, err)
				assert.Equal(t, tc.token, resp.Token)
				assert.EqualValues(t, 300, resp.ExpiresIn)
			})
		}
	})
}
