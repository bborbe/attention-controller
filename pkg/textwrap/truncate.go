// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package textwrap

import (
	"context"

	"github.com/bborbe/errors"
)

// Ellipsis is appended to text that Truncate shortens.
const Ellipsis = "…"

// Truncate shortens text to at most width runes. Text that already fits is
// returned unchanged. Shortened text ends with Ellipsis, which counts
// toward width; when width is not larger than the ellipsis itself the
// ellipsis is cut down to width.
//
// Truncate returns an error when width is smaller than one.
func Truncate(ctx context.Context, text string, width int) (string, error) {
	if width < 1 {
		return "", errors.Errorf(ctx, "width must be at least 1, got %d", width)
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text, nil
	}
	ellipsis := []rune(Ellipsis)
	if width <= len(ellipsis) {
		return string(ellipsis[:width]), nil
	}
	return string(runes[:width-len(ellipsis)]) + Ellipsis, nil
}

// TruncateLines returns a copy of lines with every entry truncated to at
// most width runes. The input slice is never modified.
func TruncateLines(ctx context.Context, lines []string, width int) ([]string, error) {
	result := make([]string, len(lines))
	for i, line := range lines {
		truncated, err := Truncate(ctx, line, width)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "truncate line %d failed", i)
		}
		result[i] = truncated
	}
	return result, nil
}
