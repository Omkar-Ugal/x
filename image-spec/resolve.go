// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package imagespec

import (
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"unikraft.com/x/image-spec/reference"
)

// Resolved is a location pinned to the digest it named when it was resolved.
// It is what an [Accessor] loads, and what an [Image] carries to say where it
// came from.
type Resolved struct {
	// Reference is the reference as requested, or zero for a local layout or
	// archive.
	Reference reference.Reference
	// ResolvedReference names the image by digest where it was found, or zero
	// for a local layout or archive. For a registry it is Reference without its
	// tag and with its digest; for a layout served over HTTP it is the location
	// the origin redirected to, or zero if it served the tag directly.
	ResolvedReference reference.Reference
	// Location is where Load reads from: the pinned location of a remote, or
	// the local layout or archive as given, which Resolve leaves alone.
	Location Location

	// Descriptor is the content Location was pinned to, which Load reads
	// instead of whatever Location names by then. For a registry, layout or
	// archive it is the root index or manifest; for a layout served over HTTP
	// it is the tarball as served, with only its media type and digest. It is
	// zero if nothing was pinned: Resolve leaves it zero for a local layout or
	// archive, and Save always sets it.
	Descriptor ocispec.Descriptor
}
