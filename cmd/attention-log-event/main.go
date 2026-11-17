// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command attention-log-event is the compiled replacement for the
// attention-log Claude Code hook. It reads one hook payload from stdin and
// writes the attention event store.
//
// ⚠️ The hook contract is exit 0 on malformed or absent input, so a bad payload
// can never block a tool call. The one non-zero exit is ErrUnsupportedEvent: an
// event whose branch a later prompt implements fails loudly rather than
// silently dropping a gate.
package main

import (
	"fmt"
	"os"

	"github.com/bborbe/attention-controller/pkg/attnlog"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "attention-log-event: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	return attnlog.NewApp().Handle(os.Stdin)
}
