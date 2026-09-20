// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/bborbe/errors"
)

//counterfeiter:generate -o ../mocks/item-id-generator.go --fake-name ItemIDGenerator . ItemIDGenerator

// ItemIDGenerator creates the stable unique id for a new item.
type ItemIDGenerator interface {
	Generate(ctx context.Context) (ItemID, error)
}

// NewItemIDGenerator creates a random ItemIDGenerator.
func NewItemIDGenerator() ItemIDGenerator {
	return &itemIDGenerator{}
}

type itemIDGenerator struct{}

// Generate returns a random 128-bit id, hex encoded.
func (i *itemIDGenerator) Generate(ctx context.Context) (ItemID, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.Wrap(ctx, err, "read random bytes failed")
	}
	return ItemID(hex.EncodeToString(buf)), nil
}
