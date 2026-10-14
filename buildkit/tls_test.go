// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildkit

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// handshake runs buildkitd's side of mTLS with server's env against client.
func handshake(t *testing.T, server map[string]string, client TLS) error {
	t.Helper()

	cert, err := tls.X509KeyPair([]byte(server[EnvTLSServerCert]), []byte(server[EnvTLSServerKey]))
	require.NoError(t, err)
	cas := x509.NewCertPool()
	require.True(t, cas.AppendCertsFromPEM([]byte(server[EnvTLSCA])))
	cfg, err := client.config()
	require.NoError(t, err)

	sc, cc := net.Pipe()
	defer sc.Close()
	defer cc.Close()
	srv := tls.Server(sc, &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    cas,
	})
	done := make(chan error, 1)
	go func() {
		err := srv.HandshakeContext(t.Context())
		// Unblock the client if the server rejects it.
		sc.Close()
		done <- err
	}()
	cerr := tls.Client(cc, cfg).HandshakeContext(t.Context())
	if err := <-done; err != nil {
		return err
	}
	return cerr
}

func TestGeneratedTLS(t *testing.T) {
	env, err := generateCerts()
	require.NoError(t, err)
	client, err := clientTLS(env)
	require.NoError(t, err)

	assert.NoError(t, handshake(t, env, client))

	other, err := generateCerts()
	require.NoError(t, err)
	stranger, err := clientTLS(other)
	require.NoError(t, err)
	assert.Error(t, handshake(t, env, stranger))
}

func TestClientTLSMasked(t *testing.T) {
	env, err := generateCerts()
	require.NoError(t, err)
	env[EnvTLSClientKey] = "*"

	_, err = clientTLS(env)
	assert.ErrorContains(t, err, EnvTLSClientKey)
}
