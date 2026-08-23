/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * The reporting API: list, describe, run, export.
 *
 * What is left in this file is the engine's half. Running a report and writing
 * a spreadsheet are things the platform does; the app's own decisions — which
 * reports this organisation may see at all — moved to backend/domain/reports
 * and arrive here as a value or a refusal.
 */

package reports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/reports"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	"github.com/go-chi/chi/v5"
)

// maxRunBody bounds a run request. The body is a parameter object of at most a
// dozen short values; anything larger is not a report request.
const maxRunBody = 16 << 10

// handleList returns the reports this tenant may run, grouped by app.
func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	groups, err := m.svc.Available(r.Context(), tenantID, localeOf(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]any{"groups": groups})
}

// paramView was a parameter as the form needs it — the spec plus the options
// resolved against this tenant's rows — and is nexus.ReportForm now: filling a
// dropdown means running SQL a report declared, which is the engine's to do.

// handleMetadata describes one report: its parameters and its columns.
func (m *Module) handleMetadata(w http.ResponseWriter, r *http.Request) {
	tenantID, report, ok := m.resolve(w, r)
	if !ok {
		return
	}

	form, err := m.engine.Form(r.Context(), tenantID, report.Key, localeOf(r))
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not describe the report")
		return
	}
	nexus.JSON(w, http.StatusOK, form)
}

// handleRun executes a report and answers with the rows.
func (m *Module) handleRun(w http.ResponseWriter, r *http.Request) {
	tenantID, report, ok := m.resolve(w, r)
	if !ok {
		return
	}

	raw, ok := decodeParams(w, r)
	if !ok {
		return
	}

	// One call rather than bind-then-run: the contract offers them together
	// because a caller that could run without binding would be running a report
	// with whatever the browser sent.
	run, err := m.engine.Run(r.Context(), tenantID, report.Key, raw, localeOf(r))
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "the report could not be produced: "+err.Error())
		return
	}

	m.record(r, tenantID, "reports.run", report.Key, map[string]any{
		"rows":   len(run.Result.Rows),
		"params": raw,
	})
	nexus.JSON(w, http.StatusOK, run)
}

// handleExport runs a report and answers with a file.
func (m *Module) handleExport(w http.ResponseWriter, r *http.Request) {
	tenantID, report, ok := m.resolve(w, r)
	if !ok {
		return
	}

	raw, ok := decodeParams(w, r)
	if !ok {
		return
	}

	// Run and render in one call, for the same reason: the format is parsed,
	// the parameters are bound and the file is named by the engine that knows
	// what it produced.
	export, err := m.engine.Export(r.Context(), tenantID, report.Key, raw,
		localeOf(r), r.URL.Query().Get("format"))
	if err != nil {
		nexus.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	// An export is a copy of the organisation's data leaving the platform, so
	// it is a separate audit entry from the run rather than the same one.
	m.record(r, tenantID, "reports.export", report.Key, map[string]any{
		"rows":   export.Rows,
		"format": r.URL.Query().Get("format"),
		"params": raw,
	})

	w.Header().Set("Content-Type", export.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", export.Filename))
	// The bytes are a spreadsheet built from a tenant's data; a cache anywhere
	// between here and the browser is a copy of it nobody accounted for.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(export.Bytes)
}

// resolve is the guard every report endpoint shares, asked of the domain: a
// report that exists, and an app this organisation has installed.
//
// It answers with the domain's description — a key, an app and the scopes the
// report was written for. Running it is a call to the engine with that key, so
// there is nothing here to hold the engine's own type for.
func (m *Module) resolve(w http.ResponseWriter, r *http.Request) (string, domain.Report, bool) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return "", domain.Report{}, false
	}
	described, err := m.svc.Resolve(r.Context(), tenantID, chi.URLParam(r, "key"))
	if err != nil {
		fail(w, r, err)
		return "", domain.Report{}, false
	}
	return tenantID, described, true
}

// loadOptions was here until 2026-08-23: a dropdown filled by running SQL a
// report declares, with a database handle this app kept for the purpose.
// Running a report's SQL is the engine's to do and ReportEngine.Form does it.

// decodeParams reads the parameter object from the body.
//
// A JSON object of strings rather than typed values: every parameter goes
// through the engine's own binding, which is the one place that knows what a report
// accepts, and giving the client a typed channel would be a second path into
// the same validation.
func decodeParams(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	raw := map[string]string{}
	if r.Body == nil {
		return raw, true
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRunBody)

	var body struct {
		Params map[string]string `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// An empty body is a run with the defaults, which is what opening a
		// report does before anybody touches the form.
		if err.Error() == "EOF" {
			return raw, true
		}
		nexus.Error(w, http.StatusBadRequest, "invalid report parameters")
		return nil, false
	}
	if body.Params != nil {
		raw = body.Params
	}
	return raw, true
}

// localeOf is the caller's language, as every screen-facing handler reads it.
func localeOf(r *http.Request) string { return nexus.LocaleFromRequest(r) }

// record writes the audit entry. Every run and every export, by name.
//
// §3.5 requires this before one tenant may see anything of another's, and it is
// worth having on ordinary runs too: "who read the payroll figures" is a
// question an organisation is entitled to an answer to.
func (m *Module) record(r *http.Request, tenantID, action, key string, details map[string]any) {
	nexus.Audit(r.Context(), tenantID, actorOf(r), action, key, details)
}

// acting names the person making the request, for the one row that records it.
func acting(r *http.Request) context.Context {
	return domain.WithActor(r.Context(), actorOf(r))
}

func actorOf(r *http.Request) string {
	if claims, err := nexus.UserFromContext(r.Context()); err == nil {
		return claims.UserID
	}
	return ""
}

// fail answers with the domain's own words and the status that refusal has
// always had. Anything that is not a refusal is a platform that could not
// answer, which is a 500 and a log line.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	code := status(err)
	if code >= http.StatusInternalServerError {
		slog.Error("reports: "+err.Error(), "error", errors.Unwrap(err), "path", r.URL.Path)
	}
	nexus.Error(w, code, err.Error())
}

// status is the whole of what this app says in HTTP that it does not say in Go.
//
// Not found is 404 rather than 403 throughout: whether a report, a schedule, an
// agreement or an organisation exists on the other side of a boundary is not
// this caller's business, and the two answers together would enumerate the
// deployment.
func status(err error) int {
	switch {
	case errors.Is(err, domain.ErrReportUnavailable),
		errors.Is(err, domain.ErrNoSuchSchedule),
		errors.Is(err, domain.ErrNoSuchGrant),
		errors.Is(err, domain.ErrNoPendingRequest),
		errors.Is(err, domain.ErrNoSuchTenant):
		return http.StatusNotFound
	case errors.Is(err, domain.ErrGrantExists):
		return http.StatusConflict
	case domain.IsRefusal(err):
		// Every other refusal is a field the caller got wrong: an unknown
		// report key in a body, a scope that is not one, an address list
		// nobody can be reached at.
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
