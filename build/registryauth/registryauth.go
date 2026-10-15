// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package registryauth gives registry credentials to BuildKit, as a session
// attachable, and to containerd's registry resolver, as an authorizer.
package registryauth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/containerd/containerd/v2/core/remotes/docker"
)

// Credentials gives the username and secret for a registry host, both empty
// for a host it has none for. A secret without a username is a token the
// registry issued.
type Credentials func(host string) (username, secret string, err error)

// registries are the hosts of Unikraft Cloud's registry.
var registries = []string{"unikraft.io", "index.unikraft.io"}

// FromToken returns the credentials that a Unikraft Cloud token gives for
// Unikraft Cloud's registry, index.unikraft.io. The token is the user and
// password in base64, like the auth of a docker config.
func FromToken(token string) (Credentials, error) {
	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("token is not base64: %w", err)
	}
	username, password, ok := strings.Cut(string(decoded), ":")
	if !ok || username == "" {
		return nil, errors.New("token is not a user and password")
	}

	return func(host string) (string, string, error) {
		if slices.Contains(registries, host) {
			return username, password, nil
		}
		return "", "", nil
	}, nil
}

// Authorizer returns an authorizer for containerd's registry resolver that
// logs in to each host with c.
func (c Credentials) Authorizer() docker.Authorizer {
	return docker.NewDockerAuthorizer(docker.WithAuthCreds(c))
}
