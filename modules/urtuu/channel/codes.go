/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * The request codes a task may be raised under, and which of them are open on
 * which link.
 *
 * A task is never free text (§2.5). Somebody chooses a code, and the code says
 * what has to be filled in and how long the work is allowed. The vocabulary is
 * not this platform's invention: the state's own register of service processes
 * is at ring.dgov.mn and is imported from there, a parent announces its chosen
 * subset down each link, and only work with no national equivalent gets a local
 * code — in a namespace, so the two can never be mistaken for each other.
 */

package channel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// maxCodeBody bounds a code definition. A JSON Schema for a form somebody fills
// in by hand is kilobytes; this is the ceiling that keeps one from being a way
// to fill the table.
const maxCodeBody = 256 << 10

// Code is one entry in the vocabulary, as the screens and the API see it.
type Code struct {
	ID    string            `json:"id"`
	Code  string            `json:"code"`
	Names map[string]string `json:"names"`
	// Schema is passed through as raw JSON in both directions. It is authored
	// elsewhere — by ring, or by an administrator — and this platform is not
	// the thing that decides what a valid JSON Schema looks like.
	Schema json.RawMessage `json:"schema"`
	// Line is which of Өртөө's two lines this code belongs to — a state
	// service, or an internal assignment. See pkg/contract.LineService.
	Line string `json:"line"`
	// DefaultSLASeconds is null where the code names no norm, which is a
	// different fact from a norm of zero.
	DefaultSLASeconds *int64 `json:"default_sla_seconds"`
	Source            string `json:"source"`
	SourcePeerID      string `json:"source_peer_id,omitempty"`
	SourcePeerName    string `json:"source_peer_name,omitempty"`
	RingProcessRef    string `json:"ring_process_ref,omitempty"`
	Version           int    `json:"version"`
	Active            bool   `json:"active"`
	// OpenTo lists the links this code has been announced on. Empty on a child,
	// which announces nothing until it has children of its own.
	OpenTo    []string  `json:"open_to"`
	UpdatedAt time.Time `json:"updated_at"`
}

// listCodes reads the whole vocabulary of one organisation.
//
// One query with the open links aggregated rather than a second round trip per
// code: the screen shows both together, and a vocabulary is tens of rows, not
// thousands.
func (s *Service) listCodes(ctx context.Context, tenantID string) ([]Code, error) {
	rows, err := s.db.Query(nexus.WithWorkspaceID(ctx, tenantID), `
		SELECT c.id::text, c.code, c.names, c.schema, c.line,
		       EXTRACT(EPOCH FROM c.default_sla)::bigint, c.source,
		       coalesce(c.source_peer_id::text, ''), coalesce(p.name, ''),
		       c.ring_process_ref, c.version, c.active, c.updated_at,
		       coalesce((SELECT array_agg(pc.peer_id::text)
		                   FROM urtuu_peer_codes pc
		                  WHERE pc.tenant_id = c.tenant_id AND pc.code = c.code), '{}')
		  FROM urtuu_request_codes c
		  LEFT JOIN urtuu_peers p ON p.id = c.source_peer_id
		 WHERE c.tenant_id = $1
		 ORDER BY c.code`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	codes := make([]Code, 0, 64)
	for rows.Next() {
		var code Code
		if err := rows.Scan(&code.ID, &code.Code, &code.Names, &code.Schema, &code.Line,
			&code.DefaultSLASeconds, &code.Source, &code.SourcePeerID, &code.SourcePeerName,
			&code.RingProcessRef, &code.Version, &code.Active, &code.UpdatedAt,
			&code.OpenTo); err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, rows.Err()
}

func (s *Service) handleListCodes(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}
	codes, err := s.listCodes(r.Context(), tenantID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the request codes")
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]any{
		"codes": codes,
		// Whether the Ring button can do anything. Said here rather than
		// discovered by pressing it: a deployment that has not been given
		// credentials should see why, not a failure.
		"ring_configured": s.ring != nil,
	})
}

type codeRequest struct {
	Code              string            `json:"code"`
	Names             map[string]string `json:"names"`
	Schema            json.RawMessage   `json:"schema"`
	DefaultSLASeconds *int64            `json:"default_sla_seconds"`
	Line              string            `json:"line"`
	Active            *bool             `json:"active"`
}

// handleCreateCode registers a code this organisation has authored itself.
//
// Only local codes are created here. A ring code is imported and a link code is
// announced; letting either be typed in would produce a code that looks
// national and answers to nobody.
func (s *Service) handleCreateCode(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxCodeBody)
	var request codeRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid request")
		return
	}

	code := strings.TrimSpace(request.Code)
	if !strings.HasPrefix(code, contract.LocalPrefix) {
		nexus.Error(w, http.StatusBadRequest,
			"a code authored here must start with "+contract.LocalPrefix+
				": the unprefixed namespace belongs to ring.dgov.mn")
		return
	}
	line := strings.TrimSpace(request.Line)
	if line == "" {
		// A code authored here is an assignment unless it is said to be a
		// service. The unstated case is the common one — an organisation's own
		// internal order — and defaulting the other way would attach the
		// service line's promise (an applicant, and an answer that must come
		// back) to work that has neither.
		line = contract.LineAssignment
	}
	if !contract.KnownLine(line) {
		nexus.Error(w, http.StatusBadRequest, "a code belongs to the service line or the assignment line")
		return
	}
	if strings.TrimSpace(request.Names["mn"]) == "" {
		// Mongolian is the source language of the register and what every
		// other locale falls back to. A code with no name at all renders as
		// its own identifier in every list it appears in.
		nexus.Error(w, http.StatusBadRequest, "a Mongolian name is required")
		return
	}

	var id string
	err := s.db.QueryRow(nexus.WithWorkspaceID(r.Context(), tenantID), `
		INSERT INTO urtuu_request_codes
		    (tenant_id, code, names, schema, default_sla, line, source, created_by)
		VALUES ($1, $2, $3, $4,
		        CASE WHEN $5::bigint IS NULL THEN NULL ELSE make_interval(secs => $5::bigint) END,
		        $6, 'local', NULLIF($7, '')::uuid)
		RETURNING id`,
		tenantID, code, request.Names, schemaOrEmpty(request.Schema),
		request.DefaultSLASeconds, line, actorOf(r)).Scan(&id)
	if err != nil {
		nexus.Error(w, http.StatusConflict, "that code already exists here")
		return
	}

	nexus.Audit(r.Context(), tenantID, actorOf(r), "urtuu.code_created", "urtuu_request_code",
		map[string]any{"code": code})
	nexus.JSON(w, http.StatusCreated, map[string]any{"id": id, "code": code})
}

// handleUpdateCode edits a local code, or switches any code on and off.
//
// A code that came from ring or from a parent is not editable here: whatever
// was changed would be overwritten by the next import or announcement, and an
// edit that silently reverts is worse than one that is refused. Its `active`
// flag is this organisation's own decision and stays editable — deciding not to
// use a code is not the same as redefining it.
func (s *Service) handleUpdateCode(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid code id")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxCodeBody)
	var request codeRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid request")
		return
	}

	ctx := nexus.WithWorkspaceID(r.Context(), tenantID)
	var source, code string
	if err := s.db.QueryRow(ctx,
		`SELECT source, code FROM urtuu_request_codes WHERE id = $1`, id).Scan(&source, &code); err != nil {
		nexus.Error(w, http.StatusNotFound, "no such code")
		return
	}

	if source != contract.SourceLocal {
		if request.Active == nil {
			nexus.Error(w, http.StatusForbidden,
				"a code from "+source+" is defined elsewhere; only whether this organisation uses it can be changed here")
			return
		}
		if _, err := s.db.Exec(ctx,
			`UPDATE urtuu_request_codes SET active = $2, updated_at = NOW() WHERE id = $1`,
			id, *request.Active); err != nil {
			nexus.Error(w, http.StatusInternalServerError, "could not update the code")
			return
		}
	} else {
		if _, err := s.db.Exec(ctx, `
			UPDATE urtuu_request_codes
			   SET names = coalesce($2, names),
			       schema = coalesce($3, schema),
			       default_sla = CASE WHEN $4::bigint IS NULL THEN default_sla
			                          ELSE make_interval(secs => $4::bigint) END,
			       active = coalesce($5, active),
			       -- Every edit is a new version, so an announcement carrying an
			       -- older one cannot overwrite it downstream.
			       version = version + 1,
			       updated_at = NOW()
			 WHERE id = $1`,
			id, namesOrNil(request.Names), schemaOrNil(request.Schema),
			request.DefaultSLASeconds, request.Active); err != nil {
			nexus.Error(w, http.StatusInternalServerError, "could not update the code")
			return
		}
	}

	nexus.Audit(r.Context(), tenantID, actorOf(r), "urtuu.code_updated", "urtuu_request_code",
		map[string]any{"code": code, "source": source})
	nexus.JSON(w, http.StatusOK, map[string]any{"id": id})
}

// handleSetPeerCodes decides which codes a link may carry, and tells the other
// side.
//
// The whole set is replaced rather than added to, because that is what the
// announcement is: a child stores what it was last told, so an update that only
// added would leave a code open downstream that this screen no longer shows.
func (s *Service) handleSetPeerCodes(w http.ResponseWriter, r *http.Request) {
	tenantID, peerID, ok := s.peerParty(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxCodeBody)
	var request struct {
		Codes []string `json:"codes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid request")
		return
	}

	ctx := nexus.WithWorkspaceID(r.Context(), tenantID)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not open the link's vocabulary")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM urtuu_peer_codes WHERE peer_id = $1`, peerID); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not update the link's vocabulary")
		return
	}
	for _, code := range request.Codes {
		if _, err := tx.Exec(ctx, `
			INSERT INTO urtuu_peer_codes (tenant_id, peer_id, code, opened_by)
			VALUES ($1, $2, $3, NULLIF($4, '')::uuid)
			ON CONFLICT DO NOTHING`,
			tenantID, peerID, strings.TrimSpace(code), actorOf(r)); err != nil {
			// The foreign key: only a code registered here can be opened.
			nexus.Error(w, http.StatusBadRequest, "no such code: "+code)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not update the link's vocabulary")
		return
	}

	if err := s.announceCodes(r.Context(), tenantID, peerID); err != nil {
		// The rows are committed; the announcement is a delivery like any
		// other and will be retried. Saying so is better than failing a change
		// that has already been made.
		nexus.Error(w, http.StatusAccepted, "the vocabulary was saved but could not be announced: "+err.Error())
		return
	}

	nexus.Audit(r.Context(), tenantID, actorOf(r), "urtuu.codes_opened", "urtuu_peer",
		map[string]any{"peer_id": peerID, "count": len(request.Codes)})
	nexus.JSON(w, http.StatusOK, map[string]any{"peer_id": peerID, "codes": len(request.Codes)})
}

// upsertCode writes a code that came from somewhere else — ring, or a parent.
//
// The version guard is the whole point: envelopes arrive out of order and an
// import can run beside an announcement, so a definition is only overwritten by
// one that claims to be newer. `active` is deliberately not touched on update —
// whether this organisation uses a code is its own decision, and an import must
// not switch back on something an administrator switched off.
func upsertCode(ctx context.Context, tx pgx.Tx, tenantID, source, peerID string, code contract.RequestCode) error {
	var sla *int64
	if code.DefaultSLA > 0 {
		seconds := int64(code.DefaultSLA / time.Second)
		sla = &seconds
	}
	names := code.Names
	if names == nil {
		names = map[string]string{}
	}
	line := code.Line
	if !contract.KnownLine(line) {
		// A peer on an older build sends no line at all. Its codes are
		// assignments, because that is the only thing that build could raise.
		line = contract.LineAssignment
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO urtuu_request_codes
		    (tenant_id, code, names, schema, default_sla, line, source, source_peer_id,
		     ring_process_ref, version)
		VALUES ($1, $2, $3, $4,
		        CASE WHEN $5::bigint IS NULL THEN NULL ELSE make_interval(secs => $5::bigint) END,
		        $10, $6, NULLIF($7, '')::uuid, $8, $9)
		ON CONFLICT (tenant_id, code) DO UPDATE
		   SET names = EXCLUDED.names,
		       schema = EXCLUDED.schema,
		       default_sla = EXCLUDED.default_sla,
		       line = EXCLUDED.line,
		       source = EXCLUDED.source,
		       source_peer_id = EXCLUDED.source_peer_id,
		       ring_process_ref = EXCLUDED.ring_process_ref,
		       version = EXCLUDED.version,
		       updated_at = NOW()
		 WHERE urtuu_request_codes.version <= EXCLUDED.version
		   AND urtuu_request_codes.source <> 'local'`,
		tenantID, code.Code, names, schemaOrEmpty(code.Schema), sla,
		source, peerID, code.RingProcessRef, max(code.Version, 1), line)
	return err
}

func schemaOrEmpty(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte(`{}`)
	}
	return raw
}

func schemaOrNil(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func namesOrNil(names map[string]string) any {
	if len(names) == 0 {
		return nil
	}
	return names
}

// ErrRingUnconfigured is what the import answers on a deployment that has not
// been given credentials for the register.
var ErrRingUnconfigured = errors.New("ring.dgov.mn is not configured on this installation")
