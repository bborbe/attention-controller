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
// `pick` — the safety property does not depend on a vault being present. It has
// no directory to write the page into (the cluster backend's case), so the card
// announces the class and blast radius without pointing at a page it cannot
// write; the operator records and acknowledges it out of band.
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
