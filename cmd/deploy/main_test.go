// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"testing"

	"github.com/bborbe/attention-controller/pkg/deploy"
)

// TestExitCode pins the contract the callers act on: `make install` fails on any
// non-zero, and alerting tells "rolled back" (board up) from "board dark" from
// "aborted before anything restarted". A collapsed mapping would either hide a
// dark board or fire a false alarm for a deploy that touched nothing.
func TestExitCode(t *testing.T) {
	cases := []struct {
		outcome deploy.Outcome
		want    int
	}{
		{deploy.Healthy, 0},
		{deploy.RolledBack, 1},
		{deploy.RollbackFailed, 2},
		{deploy.Aborted, 3},
	}
	for _, c := range cases {
		if got := exitCode(c.outcome); got != c.want {
			t.Errorf("exitCode(%d) = %d, want %d", c.outcome, got, c.want)
		}
	}
}
