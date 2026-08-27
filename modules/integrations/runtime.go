/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package integrations

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// The two helpers this package took from the platform's kernel, which a module
// outside that repository cannot import. Both are three lines, and copying
// three lines is cheaper than a contract for them.

// decodeLimited reads a JSON body with a ceiling on it. Was httpx.DecodeLimited.
func decodeLimited(r *http.Request, dst any, max int64) error {
	return json.NewDecoder(io.LimitReader(r.Body, max)).Decode(dst)
}

// background starts fn in a goroutine and keeps a panic in it from taking the
// process with it. Was async.Go.
//
// It matters here more than most places: delivery runs after the request that
// caused it has returned, so a panic in a webhook POST has nothing above it on
// the stack to recover.
func background(name string, fn func()) {
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("background task panicked", "task", name,
					"panic", recovered, "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}
