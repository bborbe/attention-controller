// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	libkv "github.com/bborbe/kv"
)

// historyMatcher is a merged HistoryFilter, prepared for the walk. The zero
// value matches everything.
type historyMatcher struct {
	ids             map[ProducerID]struct{}
	producerNeedles [][]byte
	prefix          DedupKey
	prefixNeedle    []byte
}

// newHistoryMatcher merges the filters and builds the raw-byte needles.
//
// ⚠️ Each needle is the field as the store ENCODED it — the key plus the value
// through json.Marshal — so its escaping matches the stored bytes exactly. A
// needle built by string concatenation would miss a value JSON escapes, and a
// miss here silently drops a real match. The prefix needle is the marshalled
// prefix with its closing quote removed, so it matches any value that STARTS
// with the prefix.
func newHistoryMatcher(filters []HistoryFilter) historyMatcher {
	m := historyMatcher{}
	for _, f := range filters {
		for _, p := range f.Producers {
			if p == "" {
				continue
			}
			if m.ids == nil {
				m.ids = map[ProducerID]struct{}{}
			}
			if _, seen := m.ids[p]; seen {
				continue
			}
			m.ids[p] = struct{}{}
			if value, err := json.Marshal(string(p)); err == nil {
				m.producerNeedles = append(
					m.producerNeedles,
					append([]byte(`"producer_id":`), value...),
				)
			}
		}
		if f.DedupKeyPrefix != "" {
			m.prefix = f.DedupKeyPrefix
		}
	}
	if m.prefix != "" {
		if value, err := json.Marshal(string(m.prefix)); err == nil {
			m.prefixNeedle = append([]byte(`"dedup_key":`), value[:len(value)-1]...)
		}
	}
	return m
}

// mayMatch reports whether the raw entry could pass every set filter. It can
// only rule an entry OUT; a read error answers true so the decode that follows
// reports it.
func (m historyMatcher) mayMatch(it libkv.Item) bool {
	if len(m.ids) == 0 && m.prefix == "" {
		return true
	}
	ok := true
	err := it.Value(func(v []byte) error {
		if m.prefixNeedle != nil && !bytes.Contains(v, m.prefixNeedle) {
			ok = false
			return nil
		}
		if len(m.producerNeedles) > 0 {
			found := false
			for _, needle := range m.producerNeedles {
				if bytes.Contains(v, needle) {
					found = true
					break
				}
			}
			ok = found
		}
		return nil
	})
	return err != nil || ok
}

// matches is the exact check on the decoded item.
func (m historyMatcher) matches(item Item) bool {
	if len(m.ids) > 0 {
		if _, ok := m.ids[item.ProducerID]; !ok {
			return false
		}
	}
	if m.prefix != "" && !strings.HasPrefix(string(item.DedupKey), string(m.prefix)) {
		return false
	}
	return true
}

// decode returns the entry's item and whether it passes the filter.
//
// ⚠️ The pre-check reads the RAW bytes, before the decode. The decode is most
// of what a page costs, and a poller looking for its own cards matches a
// handful of rows among thousands, so deciding from the bytes is what makes the
// filtered read cheap. The bytes can only rule a row OUT; a row they keep is
// decoded and checked exactly.
func (m historyMatcher) decode(ctx context.Context, it libkv.Item) (Item, bool, error) {
	if !m.mayMatch(it) {
		return Item{}, false, nil
	}
	item, err := decodeHistoryIndexEntry(ctx, it)
	if err != nil {
		return Item{}, false, err
	}
	return item, m.matches(item), nil
}
