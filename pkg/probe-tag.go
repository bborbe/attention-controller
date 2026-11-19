// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "strings"

// ProbeTagPrefix is the payload prefix that marks an item as a synthetic probe:
// a card posted to exercise the attention stack, never a question for the
// operator.
//
// The convention is documented in docs/probe-tag.md. It is a payload prefix
// rather than a field because [[Attention Item Schema]] carries no tag field,
// and redefining the schema from the store is forbidden — a field would be a
// schema change, while a prefix is a convention in the same class as the
// production-touching marker (docs/production-touching-marker.md), which the
// schema is likewise silent about.
const ProbeTagPrefix = "SC2 burst probe"

// IsSyntheticProbePayload reports whether a payload carries the synthetic-probe
// tag, and so must not be rendered on the operator's board.
//
// ⚠️ The match is a PREFIX, never a substring. A real card may legitimately
// discuss the probes — a card reading "the `SC2 burst probe` burst filled the
// board" is an operator question and must render. Only a payload that OPENS
// with the tag is a probe, which is the shape every probe writes and the shape
// no ordinary card has. This is the same discipline
// docs/production-touching-marker.md records for its marker: "The match is
// list-item form only, never a bare substring."
//
// ⚠️ Leading whitespace is trimmed before the prefix test so an indented probe
// payload is still recognised, but nothing else is normalised: the comparison
// is case-sensitive, exactly as the convention is written. A probe authored as
// `sc2 burst probe` is NOT tagged and WILL reach the board — the convention
// says write it exactly, and a silent case-fold would hide that mistake rather
// than report it.
func IsSyntheticProbePayload(payload Payload) bool {
	return strings.HasPrefix(strings.TrimLeft(payload.String(), " \t\r\n"), ProbeTagPrefix)
}
