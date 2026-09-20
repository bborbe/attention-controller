// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package textwrap

import "strings"

// Indent prefixes every non-empty line of text with prefix. Blank lines
// are left untouched so paragraph structure stays visible.
func Indent(text, prefix string) string {
	return strings.Join(IndentLines(strings.Split(text, "\n"), prefix), "\n")
}

// IndentLines returns a copy of lines with prefix prepended to every
// non-empty entry. The input slice is never modified.
func IndentLines(lines []string, prefix string) []string {
	result := make([]string, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			result[i] = line
			continue
		}
		result[i] = prefix + line
	}
	return result
}

// Dedent removes the longest common leading whitespace shared by all
// non-blank lines of text. Blank lines never constrain the calculation,
// and lines without the common prefix are returned unchanged.
func Dedent(text string) string {
	lines := strings.Split(text, "\n")
	prefix := commonLeadingWhitespace(lines)
	if prefix == "" {
		return text
	}
	result := make([]string, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			result[i] = line
			continue
		}
		result[i] = strings.TrimPrefix(line, prefix)
	}
	return strings.Join(result, "\n")
}

// commonLeadingWhitespace returns the longest whitespace prefix shared by
// every non-blank line.
func commonLeadingWhitespace(lines []string) string {
	prefix := ""
	initialized := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := leadingWhitespace(line)
		if !initialized {
			prefix = indent
			initialized = true
			continue
		}
		prefix = sharedPrefix(prefix, indent)
		if prefix == "" {
			return ""
		}
	}
	return prefix
}

// leadingWhitespace returns the run of spaces and tabs at the start of
// line. A line made up entirely of whitespace returns itself.
func leadingWhitespace(line string) string {
	for i, r := range line {
		if r != ' ' && r != '\t' {
			return line[:i]
		}
	}
	return line
}

// sharedPrefix returns the longest common prefix of a and b.
func sharedPrefix(a, b string) string {
	limit := min(len(a), len(b))
	n := 0
	for n < limit && a[n] == b[n] {
		n++
	}
	return a[:n]
}
