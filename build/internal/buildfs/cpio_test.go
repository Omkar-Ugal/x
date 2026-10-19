// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildfs

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unikraft/go-cpio"
)

// readCPIO returns the headers of a CPIO archive in archive order.
func readCPIO(t *testing.T, data []byte) []*cpio.Header {
	t.Helper()

	var headers []*cpio.Header
	r := cpio.NewReader(bytes.NewReader(data))
	for {
		hdr, err := r.Next()
		if errors.Is(err, io.EOF) {
			return headers
		}
		require.NoError(t, err)
		headers = append(headers, hdr)
	}
}

func createCPIO(t *testing.T, fsys fs.FS, opts ...CreateOption) []byte {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, CreateCPIO(t.Context(), &buf, fsys, opts...))
	return buf.Bytes()
}

func TestCreateCPIOInodesFollowWalkOrder(t *testing.T) {
	var tarball bytes.Buffer
	tw := tar.NewWriter(&tarball)
	for _, hdr := range []*tar.Header{
		{Typeflag: tar.TypeDir, Name: "etc/", Mode: 0o755},
		{Typeflag: tar.TypeReg, Name: "etc/hosts", Mode: 0o644, Size: 4},
		{Typeflag: tar.TypeReg, Name: "a.txt", Mode: 0o644, Size: 3},
		{Typeflag: tar.TypeLink, Name: "b.txt", Linkname: "a.txt"},
		{Typeflag: tar.TypeSymlink, Name: "link", Linkname: "a.txt"},
	} {
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write(bytes.Repeat([]byte("x"), int(hdr.Size)))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())

	fsys, err := TarballFS(bytes.NewReader(tarball.Bytes()))
	require.NoError(t, err)

	headers := readCPIO(t, createCPIO(t, fsys))

	var names []string
	var inodes []int64
	for _, hdr := range headers {
		names = append(names, hdr.Name)
		inodes = append(inodes, hdr.Inode)
	}
	assert.Equal(t, []string{"./a.txt", "./b.txt", "./etc", "./etc/hosts", "./link"}, names)
	assert.Equal(t, []int64{1, 1, 2, 3, 4}, inodes, "links to one file must share one inode")
	assert.Equal(t, int64(3), headers[0].Size)
	assert.Zero(t, headers[1].Size, "only the first link carries the data")
}

func TestCreateCPIOIdenticalTreesAreIdentical(t *testing.T) {
	mtime := time.Unix(1700000000, 0)
	newTree := func() string {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "etc"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "etc", "hosts"), []byte("127.0.0.1 localhost\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("aaa"), 0o644))
		require.NoError(t, os.Link(filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")))
		for _, p := range []string{"etc/hosts", "a.txt", "etc"} {
			require.NoError(t, os.Chtimes(filepath.Join(root, p), mtime, mtime))
		}
		return root
	}

	first := createCPIO(t, os.DirFS(newTree()))
	second := createCPIO(t, os.DirFS(newTree()))
	require.Equal(t, first, second, "identical trees in different locations must give identical archives")
}
