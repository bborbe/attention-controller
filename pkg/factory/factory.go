// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"net/http"

	libsentry "github.com/bborbe/sentry"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// CreateAttentionPushHandler creates the handler a producer calls to declare
// that a human is needed.
func CreateAttentionPushHandler(store pkg.AttentionStore) http.Handler {
	return handler.NewAttentionPushHandler(store)
}

// CreateAttentionReadHandler creates the handler every arm reads.
func CreateAttentionReadHandler(store pkg.AttentionStore) http.Handler {
	return handler.NewAttentionReadHandler(store)
}

// CreateAttentionAnswerHandler creates the handler that applies an answer as an
// atomic compare-and-set.
func CreateAttentionAnswerHandler(store pkg.AttentionStore) http.Handler {
	return handler.NewAttentionAnswerHandler(store)
}

// CreateTestLoglevelHandler creates an HTTP handler that tests different glog verbosity levels.
func CreateTestLoglevelHandler() http.Handler {
	return handler.NewTestLoglevelHandler()
}

// CreateSentryAlertHandler creates an HTTP handler that sends test alerts to Sentry.
func CreateSentryAlertHandler(sentryClient libsentry.Client) http.Handler {
	return handler.NewSentryAlertHandler(sentryClient)
}

// CreateHealthzHandler creates an HTTP handler that serves the canonical
// `/healthz` liveness response (HTTP 200, body `{"status":"ok"}`,
// Content-Type: application/json).
func CreateHealthzHandler() http.Handler {
	return handler.NewHealthzHandler()
}
