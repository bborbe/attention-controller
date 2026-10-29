// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// AnswerRoute is the part of an answer that says where the answer came from
// and which rule produced it, as distinct from what it decided.
//
// It travels as one value rather than as further positional parameters on
// Answer. That is a size argument — Answer already takes seven, and every
// field added there costs every caller and every test — but the stronger one
// is what happens next: this task exists because a route had to carry more
// than a verdict, and a bundle makes the following field free.
//
// ⚠️ The bundle carries fields only, and deliberately no behaviour. The store's
// single-writer property is what keeps an item's state unambiguous: these
// values land with the open -> answered transition, never beside it. A method
// here that wrote without settling would be a second write path wearing this
// struct's name, which is the shape that was rejected on the way to this design.
type AnswerRoute struct {
	// RequestID is the permission gate the answer settled — the wrapper's own
	// request id, not the store's item id. The two name different things: the
	// item id names the card, the request id names the pending question inside
	// the process that can answer it, so an answer recording only the card
	// cannot be audited against the gate it moved.
	RequestID string
	// Policy is the rule that decided the answer, named by the arm that applied
	// it — `routine-tool:Read`, or `no-routine-rule` on a gate that arm declined
	// to settle. It is what separates a record produced by a policy from one
	// produced by a constant: an arm that stamped a literal would be
	// indistinguishable from one that decided nothing.
	Policy string
}

// firstRoute is the route a caller supplied, or the zero value when it supplied
// none. Extracted rather than inlined so the store's Answer keeps its cognitive
// complexity under the lint's ceiling — the branch is real but belongs here, not
// in the transition itself.
func firstRoute(route []AnswerRoute) AnswerRoute {
	if len(route) > 0 {
		return route[0]
	}
	return AnswerRoute{}
}

// stampAnswerRoute writes the route members the caller actually supplied.
//
// Present-only rather than a plain assignment, for the same reason the Decision
// guard in the store exists: a second write on an already-answered item — an arm
// attaching the resolver the release path reads — posts no route, and assigning
// there would blank what the arm that actually decided the gate recorded.
//
// It mutates the in-memory item inside the caller's transaction. It is not a
// write path of its own, which is the property that keeps the store
// single-writer.
func (i *Item) stampAnswerRoute(route AnswerRoute) {
	if route.RequestID != "" {
		i.RequestID = route.RequestID
	}
	if route.Policy != "" {
		i.Policy = route.Policy
	}
}
