// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os"
	"strings"
)

// childEnvAllowlist names the parent environment variables a child process
// inherits.
//
// ⚠️ It is an allowlist, never os.Environ(). This service's environment carries
// ATTENTION_STORE_TOKEN and ATTENTION_UPSTREAM_TOKEN, and every job here spawns a
// child on a timer — ~60 an hour across the five jobs. Handing a child the whole
// environment copies both service credentials into a short-lived process that
// has no use for either, and turns every child's crash dump, its argv and any
// diagnostic it prints into a place a token can surface.
//
// The three that remain are what a child needs to be itself: PATH to resolve the
// tools a script shells out to, HOME for teamvault-cli's config and a Python
// script's `~` expansions, TMPDIR for the temporary files a script writes.
// KUBECONFIG is deliberately NOT here — the one child that reads a cluster sets
// it explicitly as an extra, so an inherited value can never shadow the path that
// child was built with.
var childEnvAllowlist = []string{
	"PATH",
	"HOME",
	"TMPDIR",
}

// childEnv builds a child process's environment from the allowlist plus extra
// `KEY=value` pairs.
//
// ⚠️ An extra pair overrides its allowlisted twin rather than being appended
// beside it: a duplicate key resolves to whichever entry the child's libc finds
// first, which is the inherited one, so the value the job meant to set would be
// silently ignored.
func childEnv(extra ...string) []string {
	overridden := make(map[string]struct{}, len(extra))
	for _, pair := range extra {
		if index := strings.IndexByte(pair, '='); index > 0 {
			overridden[pair[:index]] = struct{}{}
		}
	}
	env := make([]string, 0, len(childEnvAllowlist)+len(extra))
	for _, key := range childEnvAllowlist {
		if _, ok := overridden[key]; ok {
			continue
		}
		if value, found := os.LookupEnv(key); found {
			env = append(env, key+"="+value)
		}
	}
	return append(env, extra...)
}
