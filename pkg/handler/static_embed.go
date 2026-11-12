// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"embed"
	"fmt"
	"io/fs"
)

// attentionPageStatic carries the board's HTML document and its inline script
// into the binary. The directive names the directory, so the whole directory is
// compiled in at build time: there is no runtime path, mount or flag, and no
// file is read from disk when the process serves a page.
//
//go:embed static
var attentionPageStatic embed.FS

// mustReadAttentionPageAsset reads one embedded asset by its file name inside
// static/. A file missing from the directory still builds — the directive embeds
// whatever is there — so the failure is a startup panic that names the asset
// rather than a request that serves an empty document.
func mustReadAttentionPageAsset(name string) string {
	content, err := fs.ReadFile(attentionPageStatic, "static/"+name)
	if err != nil {
		panic(fmt.Sprintf("attention page: read embedded asset %q: %v", name, err))
	}
	return string(content)
}
