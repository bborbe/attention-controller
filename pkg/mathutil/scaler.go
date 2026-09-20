// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package mathutil

import "context"

//counterfeiter:generate -o ../../mocks/scaler.go --fake-name Scaler . Scaler

// Scaler rescales a numeric value from one range to another. It is the
// canonical example of an exported interface paired with a generated mock —
// the counterfeiter directive above this doc comment is intentionally placed
// above the doc block to exercise the reviewer's directive detection.
type Scaler interface {
	// Scale maps value from [fromMin, fromMax] onto [toMin, toMax].
	Scale(value, fromMin, fromMax, toMin, toMax int) int
}

// ParseScalerDefault parses a scaler configuration, falling back to the
// provided default when the input is not a valid scaler description. The
// paired-default naming is mandated by the paired-parse-and-parsedefault
// rule and must not be flagged as a non-New constructor.
func ParseScalerDefault(ctx context.Context, s string, defaultValue Scaler) Scaler {
	return defaultValue
}
