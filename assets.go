// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import "embed"

// frontendDist carries the built React frontend (frontend/dist) into the binary.
// The `all:` prefix includes files Vite emits that begin with `.` or `_`, so the
// embedded tree matches what `make frontend` produced byte for byte. frontend/dist
// is git-ignored except for `.gitkeep`, which keeps the directory present so this
// directive compiles on a fresh clone; a missing build is reported by the page
// handler as 503 rather than failing the build.
//
// The page handler is mounted at `/` in main.go. `go run main.go` compiles this
// one file alone and fails with `undefined: frontendDist`, so run the package:
// `go run .`.
//
//go:embed all:frontend/dist
var frontendDist embed.FS
