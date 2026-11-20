// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("manager wake repost", func() {
	// ⚠️ The obvious six-field form is rejected by PushRequest.Validate and the
	// arm would silently never post a card, so every field Validate names is
	// pinned here: a field a future edit forgets fails this test rather than a
	// silent log line.
	It("TestRepostRequestPassesValidate", func() {
		request := ManagerRepostRequest{
			ProducerID:      "abcdef12-3456-4789-8abc-def012345678",
			DedupKey:        DedupKey("park:abcdef12"),
			Payload:         Payload("worker: this session is parked on the operator"),
			AnswerMechanism: MessageAnswerMechanism,
		}
		push := managerRepostPushRequest(request, managerGateHistoryStart)
		Expect(push.Validate(context.Background())).To(Succeed())
		Expect(push.LivenessRef).To(Equal(
			LivenessRef("session:abcdef12-3456-4789-8abc-def012345678"),
		))
		Expect(push.ExpiresAt).NotTo(BeNil())
	})
})
