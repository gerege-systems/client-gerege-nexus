package integrations

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every error from the integration manager used to come back as a 400 carrying
// its own text. That answered a database outage with "bad request", and handed
// the browser whatever the driver happened to say — including the name of the
// constraint that rejected the row.
func TestIntegrationErrorsAreAnsweredByWhatTheyAre(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		// wantMessage is what the caller should be told; empty means only the
		// status is being asserted.
		wantMessage string
		// mustNotLeak is text that belongs in the log, never in the response.
		mustNotLeak string
	}{
		{
			name:        "another tenant's connector",
			err:         ErrNotFound,
			wantStatus:  http.StatusNotFound,
			wantMessage: "integration not found",
		},
		{
			name:       "a name already in use",
			err:        ErrDuplicateName,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "no key to seal credentials with",
			err:        ErrNoEncryptionKey,
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "a provider this deployment never configured",
			err:        fmt.Errorf("%w: provider dropbox needs X and Y", ErrProviderUnavailable),
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:        "something the administrator typed",
			err:         &InvalidError{Message: "a connector needs a name"},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "a connector needs a name",
		},
		{
			// The one that was wrong in both directions: reported as the
			// caller's fault, and answered with the table's internals.
			name: "the database refusing",
			err: errors.New(`ERROR: duplicate key value violates unique constraint ` +
				`"integrations_name_unique_per_tenant" (SQLSTATE 23505)`),
			wantStatus:  http.StatusInternalServerError,
			mustNotLeak: "integrations_name_unique_per_tenant",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			integrationError(rec, tc.err)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d", rec.Code, tc.wantStatus)
			}

			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("the response is not JSON (%q): %v", rec.Body.String(), err)
			}
			if body.Error == "" {
				t.Fatal("the response carries no message at all")
			}
			if tc.wantMessage != "" && body.Error != tc.wantMessage {
				t.Fatalf("message %q, want %q", body.Error, tc.wantMessage)
			}
			if tc.mustNotLeak != "" && strings.Contains(body.Error, tc.mustNotLeak) {
				t.Fatalf("the response leaks %q: %s", tc.mustNotLeak, body.Error)
			}
		})
	}
}
