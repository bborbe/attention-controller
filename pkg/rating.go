// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/collection"
	"github.com/bborbe/errors"
)

// Rating is the operator's one-click judgement of a card: did it deserve to
// reach them (up) or not (down). It is a measurement only — it is not an
// answer, not a transition and not a flag, and nothing reads it to route,
// hide, reorder or notify. See docs/attention-item-rating.md.
type Rating string

const (
	// NoRating is the cleared state: the card carries no judgement. Rating a
	// card with NoRating removes an earlier rating.
	NoRating Rating = ""
	// UpRating records that the card deserved to reach the operator.
	UpRating Rating = "up"
	// DownRating records that the card should not have reached the operator.
	DownRating Rating = "down"
)

// Ratings is a collection of Rating.
type Ratings []Rating

// AvailableRatings holds every legal rating, the cleared NoRating included —
// a rate call with NoRating is how a rating is removed.
var AvailableRatings = Ratings{
	NoRating,
	UpRating,
	DownRating,
}

// String returns the stored wire value.
func (r Rating) String() string {
	return string(r)
}

// Validate accepts exactly the values in AvailableRatings. Anything else is
// rejected rather than normalized — a typo such as "Up" or "thumbs-up" is a
// caller bug, and storing a fourth value would make every reader's count
// silently drop it.
func (r Rating) Validate(ctx context.Context) error {
	if !AvailableRatings.Contains(r) {
		return errors.Wrapf(ctx, ErrRatingInvalid, "rating %q is not up, down or empty", string(r))
	}
	return nil
}

// Contains reports whether the collection holds the given rating.
func (r Ratings) Contains(rating Rating) bool {
	return collection.Contains(r, rating)
}
