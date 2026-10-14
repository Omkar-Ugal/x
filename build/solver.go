// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/moby/buildkit/client"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/progress/progresswriter"

	ukio "unikraft.com/x/io"
)

// solver holds one BuildKit connection for several solves.
type solver struct {
	client   *client.Client
	cleanup  func()
	session  []session.Attachable
	progress func(ctx context.Context, prefix string) (progresswriter.Writer, error)
}

func (b Builder) newSolver(ctx context.Context) (*solver, error) {
	if b.Buildkit == nil {
		return nil, errors.New("no buildkit connector configured")
	}

	var attachables []session.Attachable
	if b.Session != nil {
		var err error
		attachables, err = b.Session(ctx)
		if err != nil {
			return nil, err
		}
	}

	c, cleanup, err := b.Buildkit(ctx)
	if err != nil {
		return nil, err
	}
	if c == nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, errors.New("buildkit connector gave no client")
	}

	return &solver{
		client:   c,
		cleanup:  cleanup,
		session:  slices.Clone(attachables),
		progress: b.Progress,
	}, nil
}

func (s *solver) close() {
	if s.cleanup != nil {
		s.cleanup()
	}
}

// solveToTar runs build, exporting an uncompressed tarball of the result to
// dst, which is left synced and rewound. Progress is printed under prefix.
func (s *solver) solveToTar(ctx context.Context, dst *os.File, prefix string, solveOpt client.SolveOpt, build gateway.BuildFunc) error {
	solveOpt.Ref = identity.NewID()
	solveOpt.Session = s.session
	solveOpt.Exports = []client.ExportEntry{{
		Type: client.ExporterTar,
		Output: func(map[string]string) (io.WriteCloser, error) {
			return ukio.NopWriteCloser(dst), nil
		},
	}}

	var pw progresswriter.Writer
	if s.progress != nil {
		var err error
		pw, err = s.progress(ctx, prefix)
		if err != nil {
			return err
		}
	} else {
		pw = newDiscardProgress()
	}

	if _, err := s.client.Build(ctx, solveOpt, "buildctl", build, pw.Status()); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-pw.Done():
	}
	if err := pw.Err(); err != nil {
		return err
	}

	if err := dst.Sync(); err != nil {
		return fmt.Errorf("could not sync tarball: %w", err)
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("could not rewind tarball: %w", err)
	}

	return nil
}
