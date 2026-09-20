// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package textwrap provides dependency-free helpers for reflowing plain
// text to a fixed column width.
package textwrap

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
)

// Wrap reflows text so that every returned line is at most width runes
// long. Every line break in text starts a new output line and blank input
// lines stay blank. Runs of spaces, tabs, and other whitespace collapse to
// a single space. Words longer than width are hard-split so the width
// guarantee always holds.
//
// Wrap returns an error when width is smaller than one.
func Wrap(ctx context.Context, text string, width int) ([]string, error) {
	if width < 1 {
		return nil, errors.Errorf(ctx, "width must be at least 1, got %d", width)
	}
	var lines []string
	for paragraph := range strings.SplitSeq(text, "\n") {
		lines = append(lines, wrapParagraph(paragraph, width)...)
	}
	return lines, nil
}

// WrapString reflows text exactly like Wrap and joins the resulting lines
// with newline characters.
func WrapString(ctx context.Context, text string, width int) (string, error) {
	lines, err := Wrap(ctx, text, width)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "wrap text failed")
	}
	return strings.Join(lines, "\n"), nil
}

// wrapParagraph wraps a paragraph without embedded line breaks. An empty
// paragraph yields a single empty line so paragraph spacing survives a
// wrap-and-join round trip.
func wrapParagraph(paragraph string, width int) []string {
	words := strings.Fields(paragraph)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	current := ""
	for _, word := range words {
		for _, piece := range splitLongWord(word, width) {
			switch {
			case current == "":
				current = piece
			case runeLen(current)+1+runeLen(piece) <= width:
				current += " " + piece
			default:
				lines = append(lines, current)
				current = piece
			}
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

// splitLongWord splits word into width-sized pieces when it does not fit
// on a single line. Words that already fit are returned unchanged.
func splitLongWord(word string, width int) []string {
	runes := []rune(word)
	if len(runes) <= width {
		return []string{word}
	}
	pieces := make([]string, 0, len(runes)/width+1)
	for len(runes) > width {
		pieces = append(pieces, string(runes[:width]))
		runes = runes[width:]
	}
	if len(runes) > 0 {
		pieces = append(pieces, string(runes))
	}
	return pieces
}

// runeLen reports the number of runes in s.
func runeLen(s string) int {
	return len([]rune(s))
}
