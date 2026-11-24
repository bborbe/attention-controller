// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	libtime "github.com/bborbe/time"

	"github.com/bborbe/attention-controller/pkg"
)

// decisionRecordStoreOptions attaches the Decision Record writer when a vault
// is configured.
//
// ⚠️ Without a writer the store still suppresses an irreversible-infrastructure
// `pick` — the safety property does not depend on a vault being present — but
// it has no directory to write the page into (the cluster backend's case) and
// points the acknowledgement at the record's title instead of a page it cannot
// write.
func decisionRecordStoreOptions(vaultDir string) []pkg.AttentionStoreOption {
	if vaultDir == "" {
		return nil
	}
	return []pkg.AttentionStoreOption{
		pkg.WithDecisionRecordWriter(
			pkg.NewVaultDecisionRecordWriter(vaultDir, libtime.NewCurrentDateTime()),
		),
	}
}
