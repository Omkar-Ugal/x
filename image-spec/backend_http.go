// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/containerd/platforms"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"unikraft.com/x/image-spec/reference"
	"unikraft.com/x/image-spec/schemes"
	xio "unikraft.com/x/io"
)

// This file provides functions for loading images from an OCI image layout
// tarball served over HTTP, which is how the platform fetches an image from a
// user's own origin. The origin may answer a tag with a redirect to the digest
// it currently names, or serve the tarball under the tag directly.

// MediaTypeOCILayoutTar is the media type of an OCI image layout served as a
// tarball.
const MediaTypeOCILayoutTar = "application/vnd.oci.layout.v1+tar"

// ResolveTarballRemote resolves the URL of a layout served over HTTP to the
// URL of the digest it currently names. An origin that answers a tag with a
// redirect, under the same host, to that digest pins it; one that serves the
// tag directly leaves it unpinned, and u is returned as is. A URL that already
// names a digest is checked with a HEAD and returned as is.
//
// The origin is always asked, so that it can refuse access: knowing a digest
// is not enough to fetch it.
func ResolveTarballRemote(ctx context.Context, u *url.URL, client *http.Client, header http.Header) (*url.URL, error) {
	c := http.Client{}
	if client != nil {
		c = *client
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	method := http.MethodGet
	_, err := tarballDigest(u)
	pinned := err == nil
	if pinned {
		method = http.MethodHead
	}
	rsp, err := requestTarball(ctx, &c, method, u, header)
	if err != nil {
		return nil, fmt.Errorf("resolving %q: %w", u, err)
	}
	defer rsp.Body.Close()

	switch {
	case rsp.StatusCode == http.StatusOK:
		if err := checkLayoutContentType(rsp); err != nil {
			return nil, fmt.Errorf("resolving %q: %w", u, err)
		}
		return u, nil
	case pinned:
		return nil, fmt.Errorf("resolving %q: unexpected status %s", u, rsp.Status)
	}

	switch rsp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return nil, fmt.Errorf("resolving %q: unexpected status %s", u, rsp.Status)
	}

	location := rsp.Header.Get("Location")
	if location == "" {
		return nil, fmt.Errorf("resolving %q: redirect without a location", u)
	}
	target, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("resolving %q: invalid location %q: %w", u, location, err)
	}
	if target.Host != "" && target.Host != u.Host {
		return nil, fmt.Errorf("resolving %q: redirect to another host %q is not supported", u, target.Host)
	}

	target = &url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.ResolveReference(target).Path}
	if _, err := tarballDigest(target); err != nil {
		return nil, fmt.Errorf("resolving %q: location %q: %w", u, location, err)
	}
	return target, nil
}

// checkLayoutContentType rejects a response that does not claim to be a
// layout, since a soft-404 or catch-all page answers 200 to anyone.
func checkLayoutContentType(rsp *http.Response) error {
	mediaType, _, err := mime.ParseMediaType(rsp.Header.Get("Content-Type"))
	if err != nil {
		return fmt.Errorf("invalid content type: %w", err)
	}
	if mediaType != MediaTypeOCILayoutTar {
		return fmt.Errorf("unexpected content type %q", mediaType)
	}
	return nil
}

// LoadTarballRemote loads the layout tarball at u, checking the bytes as
// served, before any content encoding is undone, against dgst - or, if dgst
// is empty, against the digest the final path segment of u names, if any.
func LoadTarballRemote(ctx context.Context, u *url.URL, dgst digest.Digest, client *http.Client, header http.Header, platform platforms.MatchComparer) (*Image, error) {
	body, err := openTarballRemote(ctx, u, dgst, client, header)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	img, err := LoadTarballReader(ctx, body, ocispec.Descriptor{}, platform)
	if err != nil {
		return nil, err
	}
	if err := body.verify(); err != nil {
		return nil, errors.Join(err, img.Close())
	}
	img.Resolved = tarballResolved(u)
	return img, nil
}

// LoadAllTarballsRemote loads all images from the layout tarball at u,
// checking the bytes as LoadTarballRemote does.
func LoadAllTarballsRemote(ctx context.Context, u *url.URL, dgst digest.Digest, client *http.Client, header http.Header, platform platforms.MatchComparer) ([]*Image, error) {
	body, err := openTarballRemote(ctx, u, dgst, client, header)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	imgs, err := LoadAllTarballReaders(ctx, body, ocispec.Descriptor{}, platform)
	if err != nil {
		return nil, err
	}
	if err := body.verify(); err != nil {
		for _, img := range imgs {
			err = errors.Join(err, img.Close())
		}
		return nil, err
	}
	for _, img := range imgs {
		img.Resolved = tarballResolved(u)
	}
	return imgs, nil
}

// tarballDigest is the digest the final path segment of u names.
func tarballDigest(u *url.URL) (digest.Digest, error) {
	_, identifier := path.Split(u.Path)
	if !strings.HasPrefix(identifier, "@") {
		return "", fmt.Errorf("%q does not name a digest", u.Path)
	}
	dgst, err := digest.Parse(identifier[1:])
	if err != nil {
		return "", fmt.Errorf("%q does not name a digest: %w", u.Path, err)
	}
	return dgst, nil
}

// tarballResolved is as much of a Resolved as the URL of a layout gives: the
// Accessor keeps the one it was handed instead.
func tarballResolved(u *url.URL) Resolved {
	scheme := schemes.Scheme(u.Scheme + "+oci")
	res := Resolved{Location: Location{Scheme: scheme, Path: u.Host + u.Path}}
	if ref, err := reference.Parse(res.Location.String()); err == nil {
		res.Reference = ref
		if ref.Digest() != "" {
			res.ResolvedReference = ref
		}
	}
	if dgst, err := tarballDigest(u); err == nil {
		res.Descriptor = tarballDescriptor(dgst)
	}
	return res
}

func tarballDescriptor(dgst digest.Digest) ocispec.Descriptor {
	return ocispec.Descriptor{MediaType: MediaTypeOCILayoutTar, Digest: dgst}
}

// remoteTarball is the body of a tarball response: the decoded bytes to read,
// and the served bytes still to be verified against the digest, if any.
type remoteTarball struct {
	io.ReadCloser
	served   io.Reader
	verifier digest.Verifier
	expected digest.Digest
}

// verify drains the served bytes, since the archive may end before the body
// does, and checks them against the digest.
func (t *remoteTarball) verify() error {
	if t.verifier == nil {
		return nil
	}
	if _, err := io.Copy(io.Discard, t.served); err != nil {
		return fmt.Errorf("draining response body: %w", err)
	}
	if !t.verifier.Verified() {
		return fmt.Errorf("fetched layout does not match %s", t.expected)
	}
	return nil
}

func openTarballRemote(ctx context.Context, u *url.URL, dgst digest.Digest, client *http.Client, header http.Header) (*remoteTarball, error) {
	if named, err := tarballDigest(u); err == nil {
		if dgst == "" {
			dgst = named
		} else if dgst != named {
			return nil, fmt.Errorf("fetching %q: pinned to %s, but it names %s", u, dgst, named)
		}
	}

	rsp, err := requestTarball(ctx, client, http.MethodGet, u, header)
	if err != nil {
		return nil, fmt.Errorf("fetching %q: %w", u, err)
	}
	if rsp.StatusCode != http.StatusOK {
		rsp.Body.Close()
		return nil, fmt.Errorf("fetching %q: unexpected status %s", u, rsp.Status)
	}

	var (
		verifier digest.Verifier
		served   io.Reader = rsp.Body
	)
	if dgst != "" {
		verifier = dgst.Verifier()
		served = io.TeeReader(rsp.Body, verifier)
	}

	decoded, err := decodeContent(rsp.Header.Get("Content-Encoding"), served)
	if err != nil {
		rsp.Body.Close()
		return nil, fmt.Errorf("fetching %q: %w", u, err)
	}

	return &remoteTarball{
		ReadCloser: struct {
			io.Reader
			io.Closer
		}{decoded, xio.Closers{decoded, rsp.Body}},
		served:   served,
		verifier: verifier,
		expected: dgst,
	}, nil
}

// acceptEncoding lists the content encodings decodeContent undoes.
var acceptEncoding = []string{"zstd", "gzip", "deflate", "identity"}

// requestTarball asks for the layout tarball at u.
func requestTarball(ctx context.Context, client *http.Client, method string, u *url.URL, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if header != nil {
		req.Header = header.Clone()
	}
	req.Header.Set("Accept", MediaTypeOCILayoutTar)
	req.Header.Set("Accept-Encoding", strings.Join(acceptEncoding, ", "))

	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

// decodeContent undoes the content encodings applied to r, last first.
func decodeContent(contentEncoding string, r io.Reader) (io.ReadCloser, error) {
	encodings := strings.Split(contentEncoding, ",")
	slices.Reverse(encodings)

	rc := io.NopCloser(r)
	for _, encoding := range encodings {
		switch strings.TrimSpace(encoding) {
		case "", "identity":
		case "gzip":
			g, err := gzip.NewReader(rc)
			if err != nil {
				return nil, fmt.Errorf("decoding gzip content: %w", err)
			}
			rc = g
		case "zstd":
			z, err := zstd.NewReader(rc)
			if err != nil {
				return nil, fmt.Errorf("decoding zstd content: %w", err)
			}
			rc = z.IOReadCloser()
		case "deflate":
			z, err := zlib.NewReader(rc)
			if err != nil {
				return nil, fmt.Errorf("decoding deflate content: %w", err)
			}
			rc = z
		default:
			return nil, fmt.Errorf("unsupported content encoding %q", strings.TrimSpace(encoding))
		}
	}
	return rc, nil
}
