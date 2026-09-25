// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"strings"

	"github.com/bborbe/errors"
)

//counterfeiter:generate -o ../mocks/jump-token-reader.go --fake-name JumpTokenReader . JumpTokenReader

// JumpTokenReader reads the shared secret the fleet-jump server requires.
//
// ⚠️ The token is a credential. It is never logged, never carried inside an
// error message, and never rendered — a caller reporting why a jump is
// unavailable reports the failure, never the value. That is the operator's
// "credentials never leak" rule, and it is the reason this reader returns the
// token rather than handing back an already-built URL a caller might log.
//
// It is read per use rather than cached at construction, so a token rotated or
// removed while the service runs takes effect on the next page load rather than
// at the next restart.
type JumpTokenReader interface {
	// Read returns the token, or an error when the file is missing, unreadable
	// or empty.
	//
	// ⚠️ An error means "no jump", never a failed page. Every caller degrades on
	// it — the board renders no button and the redirect refuses — because a
	// host with no token file is an ordinary deployment, not a fault.
	Read(ctx context.Context) (string, error)
}

// NewJumpTokenReader creates a reader for the token file at path.
//
// The path is operator configuration rather than producer input, so it is read
// directly instead of through the os.Root confinement the event-log reader
// uses: nothing a producer sends can influence it.
func NewJumpTokenReader(path string) JumpTokenReader {
	return &jumpTokenReader{path: path}
}

type jumpTokenReader struct {
	path string
}

func (r *jumpTokenReader) Read(ctx context.Context) (string, error) {
	if r.path == "" {
		return "", errors.New(ctx, "jump token path is empty")
	}
	raw, err := os.ReadFile(r.path)
	if err != nil {
		// The path is safe to report; the contents never are.
		return "", errors.Wrapf(ctx, err, "read jump token %s failed", r.path)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errors.New(ctx, "jump token file is empty")
	}
	return token, nil
}
