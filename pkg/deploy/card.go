// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package deploy

import (
	"fmt"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/buildidentity"
)

// failureProducerID is the producer the driver's cards belong to. It is a
// stable id rather than a label: the store suppresses a duplicate push on
// producer id plus dedup key, so a renamed producer re-opens the whole history.
const failureProducerID pkg.ProducerID = "attention-controller-deploy"

// Failure is one rolled-back deploy, as the card renders it.
type Failure struct {
	// Failed is the build that was installed and did not serve.
	Failed buildidentity.Identity
	// Prev is the build the rollback restored.
	Prev buildidentity.Identity
	// Class is which failed-start class the driver observed.
	Class FailureClass
	// Mode is which entry point drove the deploy.
	Mode Mode
	// Status is what launchd said about the job at the failure.
	Status ProcessStatus
	// BinaryPath is the installed binary's path, used as the liveness ref.
	BinaryPath string
}

// Card builds the operator-facing card for this failure.
//
// ⚠️ The two classes must not be conflated: only an alive-no-listener start
// carries the consent literal, because only that observable matches the TCC
// signature. An exited start names its version and says nothing about consent —
// a consent prompt on a crash would send the operator to a settings pane that
// cannot help.
//
// ⚠️ The short commit goes in Context, not Payload. Payload's contract is plain
// operator-facing sentences with no machine handles; the commit is background
// an operator greps the stored item for, and it has to be IN the item for that
// grep to mean anything.
func (f Failure) Card() pkg.PushRequest {
	return pkg.PushRequest{
		ProducerID:      failureProducerID,
		ProducerKind:    pkg.CronProducerKind,
		ProvenanceClass: pkg.ExplicitProvenanceClass,
		LivenessRef:     pkg.LivenessRef("heartbeat:" + f.BinaryPath),
		DedupKey:        pkg.DedupKey("attention-controller-deploy-failed-" + f.Failed.Commit),
		InterruptClass:  pkg.InterruptClass("pick"),
		Payload:         pkg.Payload(f.payload()),
		Context:         pkg.ItemContext(f.context()),
		AnswerMechanism: pkg.AckAnswerMechanism,
	}
}

// payload is the operator-facing sentence.
func (f Failure) payload() string {
	if f.Class == AliveNoListener {
		return fmt.Sprintf(
			"attention-controller deploy failed: %s never opened the board port. %s",
			f.Failed.Version, ConsentAction,
		)
	}
	return fmt.Sprintf(
		"attention-controller deploy failed: %s exited%s before opening the board port.",
		f.Failed.Version, exitClause(f.Status.LastExit),
	)
}

// context is the background: which build failed, which one serves now.
func (f Failure) context() string {
	return fmt.Sprintf(
		"The failed build is commit %s. The board rolled back to %s and is serving again. "+
			"Deploy mode: %s.",
		f.Failed.Commit, f.Prev.Commit, f.Mode,
	)
}

// exitClause renders launchd's exit code when it recorded one.
//
// "(never exited)" is a real state, not a missing value, and it is the one a
// start killed by a signal or refused by launchd leaves behind — so it gets no
// clause rather than a sentence claiming a status nobody has.
func exitClause(lastExit string) string {
	if lastExit == "" || lastExit == "(never exited)" {
		return ""
	}
	return " with status " + lastExit
}
