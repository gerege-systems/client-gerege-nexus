/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * ring.dgov.mn — the register the request codes come from.
 *
 * The vocabulary a task may be raised under is not this platform's to author
 * (§2.5). What is here is a client for a register that publishes it, and the
 * format it speaks is written down in docs/RING_STANDARD.md — a standard this
 * platform proposes rather than one it waits for.
 *
 * That is the whole of the decision this file rests on. The alternative was to
 * leave the parser unwritten until somebody else described their wire format,
 * which is how an integration stays "nearly done" for a year. Writing it the
 * other way round costs nothing if they disagree: the shape below is one
 * document, one signature and eight fields, and adapting to a published format
 * later is a smaller change than the waiting was.
 *
 * # Why it looks exactly like the app catalogue
 *
 * Because it is the same problem, and this repository has already solved it
 * once: an authority publishes a document, every instance fetches it with an
 * ETag, and nothing is believed without a signature over the raw bytes. A
 * second signed-document format would be a second thing to keep in step, and
 * the reviewer who has understood pkg/catalog would have to learn this one
 * from scratch. The signing input — generated_at, a newline, the raw list —
 * is the same rule in all three places it appears here: the catalogue, the
 * Өртөө envelope, and this.
 *
 * # What is deliberately absent
 *
 * A disk cache. The catalogue needs one because it is read at boot, before the
 * database is known to be there. Codes are not: they are written into
 * urtuu_request_codes on import and read from there afterwards, so the
 * database *is* the cache and a register that is down costs an out-of-date
 * vocabulary rather than an outage.
 *
 * A background sync. The import runs when an administrator asks for it. A code
 * arrives carrying a time norm, and a norm is what the people doing the work
 * are measured against — adopting a new national one is a decision somebody
 * makes, not something that happens overnight.
 */

package channel

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

const (
	ringBaseURLEnv = "RING_BASE_URL"
	// #nosec G101 -- the name of an environment variable, not a credential.
	ringAPIKeyEnv = "RING_API_KEY"
	// ringPublicKeyEnv verifies what the register publishes. Without it the
	// import does not run: a register that has been compromised or
	// impersonated would otherwise be able to change what every government
	// installation in the country believes a code means and how long it is
	// allowed to take.
	ringPublicKeyEnv = "RING_PUBLIC_KEY"

	// ringMockURL is what an operator sets RING_BASE_URL to in order to develop
	// against the shape of the feature without credentials. Spelled out rather
	// than inferred from an empty value, because "unconfigured" and "pretend"
	// must not be the same state: the first is off, the second serves invented
	// codes and has to be a choice somebody made.
	ringMockURL = "mock"

	// ringTimeout bounds one import.
	ringTimeout = 30 * time.Second

	// maxRingBytes bounds the register's answer. The whole national vocabulary
	// with a JSON Schema per process is hundreds of kilobytes; this is the
	// ceiling that keeps a hostile or broken endpoint from being an
	// out-of-memory.
	maxRingBytes = 8 << 20
)

// RingImporter is the register, as this package needs it.
//
// One method, because one question is asked: what processes are there.
type RingImporter interface {
	Processes(ctx context.Context) ([]contract.RequestCode, error)
}

// ErrRingUnchanged is what a conditional fetch answers when the register has
// published nothing new. It is not a failure and the screen says so.
var ErrRingUnchanged = errors.New("ring.dgov.mn has published nothing new")

// newRingImporter builds whichever importer the environment names, or nil.
func newRingImporter(client *http.Client) RingImporter {
	base := strings.TrimSuffix(strings.TrimSpace(os.Getenv(ringBaseURLEnv)), "/")
	switch base {
	case "":
		return nil
	case ringMockURL:
		slog.Warn("urtuu: " + ringBaseURLEnv + " is set to \"" + ringMockURL +
			"\", so request codes are invented for development and are not the state register's")
		return ringMock{}
	}

	key := strings.TrimSpace(os.Getenv(ringAPIKeyEnv))
	if key == "" {
		slog.Error("urtuu: " + ringBaseURLEnv + " is set but " + ringAPIKeyEnv +
			" is not, so the request-code import is off")
		return nil
	}
	publicKey, err := ringPublicKey()
	if err != nil {
		slog.Error("urtuu: the request-code import is off because the register's signature cannot be checked",
			"error", err)
		return nil
	}

	slog.Info("urtuu: request codes will be imported from the state register", "base_url", base)
	return &ringHTTP{base: base, key: key, publicKey: publicKey, client: client}
}

// ringPublicKey reads the key the register's documents are verified with.
func ringPublicKey() (ed25519.PublicKey, error) {
	raw := strings.TrimSpace(os.Getenv(ringPublicKeyEnv))
	if raw == "" {
		return nil, errors.New(ringPublicKeyEnv + " is not set, and an unsigned register is never accepted")
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%s is not a base64 Ed25519 public key (%d bytes): %w",
			ringPublicKeyEnv, len(decoded), err)
	}
	return ed25519.PublicKey(decoded), nil
}

// ---------------------------------------------------------------- the wire

// ringDocument is what the register publishes — docs/RING_STANDARD.md §3.
//
// Processes is held as raw JSON because that is what the signature covers.
// Decoding and re-encoding it to verify would mean trusting this program's
// field order, number formatting and escaping to reproduce the publisher's
// bytes exactly, which is how a signature check quietly stops checking
// anything. pkg/catalog holds its apps the same way and for the same reason.
type ringDocument struct {
	GeneratedAt string          `json:"generated_at"`
	KeyID       string          `json:"key_id"`
	Processes   json.RawMessage `json:"processes"`
	Signature   string          `json:"signature"`
}

// signedMessage is what the register signs and this verifies:
//
//	generated_at "\n" <the processes array, verbatim>
func (d ringDocument) signedMessage() []byte {
	message := make([]byte, 0, len(d.GeneratedAt)+1+len(d.Processes))
	message = append(message, d.GeneratedAt...)
	message = append(message, '\n')
	return append(message, d.Processes...)
}

// ringProcess is one entry — docs/RING_STANDARD.md §3.3.
type ringProcess struct {
	Code  string            `json:"code"`
	Line  string            `json:"line"`
	Names map[string]string `json:"names"`
	// Schema is passed through untouched. This platform is not the thing that
	// decides what a valid JSON Schema looks like.
	Schema json.RawMessage `json:"schema,omitempty"`
	// SLAHours is calendar hours. Working days would need a holiday calendar
	// this platform does not have and should not own — see the standard for
	// why the conversion belongs to the publisher.
	SLAHours   int    `json:"sla_hours,omitempty"`
	Version    int    `json:"version"`
	Active     *bool  `json:"active,omitempty"`
	ProcessRef string `json:"process_ref,omitempty"`
}

// ---------------------------------------------------------------- the client

type ringHTTP struct {
	base      string
	key       string
	publicKey ed25519.PublicKey
	client    *http.Client

	// etag is the last document's tag, so an import that changes nothing costs
	// a 304 rather than the whole national vocabulary. Guarded because the
	// import can be started from two tenants' screens at once.
	mu   sync.Mutex
	etag string
}

func (r *ringHTTP) Processes(ctx context.Context) ([]contract.RequestCode, error) {
	ctx, cancel := context.WithTimeout(ctx, ringTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/processes", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.key)
	r.mu.Lock()
	etag := r.etag
	r.mu.Unlock()
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	res, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()

	switch res.StatusCode {
	case http.StatusNotModified:
		return nil, ErrRingUnchanged
	case http.StatusOK:
	default:
		return nil, fmt.Errorf("ring.dgov.mn answered %s", res.Status)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxRingBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxRingBytes {
		return nil, fmt.Errorf("ring.dgov.mn answered more than %d bytes", maxRingBytes)
	}

	codes, err := r.parse(body)
	if err != nil {
		return nil, err
	}

	// Remembered only after the document was accepted: a tag kept for a
	// document that failed verification would make the next import a 304 and
	// leave the bad answer standing as the current one.
	r.mu.Lock()
	r.etag = res.Header.Get("ETag")
	r.mu.Unlock()
	return codes, nil
}

// parse verifies a document and reads what it carries.
//
// A signature that does not check out is not a reason to read the document
// more carefully — it is a reason to stop reading it. Nothing from an
// unverified register reaches the vocabulary, not even one field.
func (r *ringHTTP) parse(body []byte) ([]contract.RequestCode, error) {
	var document ringDocument
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, fmt.Errorf("unmarshal the register's answer: %w", err)
	}
	if len(document.Processes) == 0 {
		return nil, errors.New("ring.dgov.mn published a document carrying no processes")
	}

	signature, err := base64.StdEncoding.DecodeString(document.Signature)
	if err != nil {
		return nil, fmt.Errorf("decode the register's signature: %w", err)
	}
	if !ed25519.Verify(r.publicKey, document.signedMessage(), signature) {
		return nil, fmt.Errorf("the register's signature does not verify (key_id %q)", document.KeyID)
	}

	var published []ringProcess
	if err := json.Unmarshal(document.Processes, &published); err != nil {
		return nil, fmt.Errorf("unmarshal the register's processes: %w", err)
	}
	return convertRingProcesses(published, document.KeyID), nil
}

// convertRingProcesses turns the wire form into the contract's.
//
// A record this platform cannot act on is dropped with a line rather than
// failing the whole import: a register with one malformed entry among four
// hundred should cost that entry, not the country's vocabulary.
func convertRingProcesses(published []ringProcess, keyID string) []contract.RequestCode {
	codes := make([]contract.RequestCode, 0, len(published))
	for _, entry := range published {
		code := strings.TrimSpace(entry.Code)
		switch {
		case code == "":
			slog.Warn("urtuu: the register published a process with no code", "key_id", keyID)
			continue
		case strings.HasPrefix(code, contract.LocalPrefix):
			// The prefixed namespace belongs to the organisations. A register
			// record claiming one would overwrite something somebody authored
			// for themselves.
			slog.Warn("urtuu: the register published a code in the local namespace; ignored", "code", code)
			continue
		case strings.TrimSpace(entry.Names["mn"]) == "":
			// Mongolian is the source language of the register and what every
			// other locale falls back to. Without it the code renders as its
			// own identifier in every list it appears in.
			slog.Warn("urtuu: the register published a code with no Mongolian name; ignored", "code", code)
			continue
		}

		line := strings.TrimSpace(entry.Line)
		if !contract.KnownLine(line) {
			// The register is the state's list of *services*, so that is what
			// an unstated line means here.
			line = contract.LineService
		}
		active := true
		if entry.Active != nil {
			active = *entry.Active
		}
		codes = append(codes, contract.RequestCode{
			Code:           code,
			Names:          entry.Names,
			Schema:         entry.Schema,
			DefaultSLA:     time.Duration(max(entry.SLAHours, 0)) * time.Hour,
			Line:           line,
			Source:         contract.SourceRing,
			RingProcessRef: entry.ProcessRef,
			Version:        max(entry.Version, 1),
			Active:         active,
		})
	}
	return codes
}

// ------------------------------------------------------------------ the mock

// ringMock is a handful of plausible codes for developing against.
//
// They are labelled as invented in their own names, in every language, because
// the one failure this must not have is a mock code reaching a real deployment
// and being read as a national one.
type ringMock struct{}

func (ringMock) Processes(_ context.Context) ([]contract.RequestCode, error) {
	return []contract.RequestCode{
		{
			Code: "D-101",
			Names: map[string]string{
				"mn": "Тооллого явуулах (жишиг)", "en": "Carry out a count (sample)",
				"ar": "إجراء إحصاء (عينة)", "zh": "开展清点（示例）",
				"fr": "Réaliser un recensement (exemple)", "ru": "Провести перепись (образец)",
				"es": "Realizar un recuento (muestra)",
			},
			Schema: []byte(`{"type":"object","required":["period"],"properties":` +
				`{"period":{"type":"string","title":"Хамрах хугацаа"},` +
				`"scope":{"type":"string","title":"Хамрах хүрээ"}}}`),
			DefaultSLA:     14 * 24 * time.Hour,
			Line:           contract.LineService,
			Source:         contract.SourceRing,
			RingProcessRef: "mock/D-101",
			Version:        1,
			Active:         true,
		},
		{
			Code: "D-204",
			Names: map[string]string{
				"mn": "Мэдээлэл гаргуулах (жишиг)", "en": "Provide information (sample)",
				"ar": "تقديم معلومات (عينة)", "zh": "提供信息（示例）",
				"fr": "Fournir des informations (exemple)", "ru": "Предоставить сведения (образец)",
				"es": "Proporcionar información (muestra)",
			},
			Schema: []byte(`{"type":"object","required":["subject"],"properties":` +
				`{"subject":{"type":"string","title":"Асуулгын сэдэв"}}}`),
			DefaultSLA:     3 * 24 * time.Hour,
			Line:           contract.LineService,
			Source:         contract.SourceRing,
			RingProcessRef: "mock/D-204",
			Version:        1,
			Active:         true,
		},
	}, nil
}

// ----------------------------------------------------------------- importing

// importRing reads the register and writes what it says into one
// organisation's vocabulary.
//
// Imported codes are not switched on for anybody by themselves: `active` is
// left alone on an update, and a newly imported code still has to be opened on
// a link before a child ever hears of it.
func (s *Service) importRing(ctx context.Context, tenantID string) (int, error) {
	if s.ring == nil {
		return 0, ErrRingUnconfigured
	}
	codes, err := s.ring.Processes(ctx)
	if err != nil {
		return 0, err
	}

	ctx = nexus.WithWorkspaceID(ctx, tenantID)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, code := range codes {
		if err := upsertCode(ctx, tx, tenantID, contract.SourceRing, "", code); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(codes), nil
}

func (s *Service) handleRingSync(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	imported, err := s.importRing(r.Context(), tenantID)
	switch {
	case errors.Is(err, ErrRingUnchanged):
		// Not a failure, and the screen must not report one: the register
		// publishing nothing new is the ordinary answer to asking twice.
		nexus.JSON(w, http.StatusOK, map[string]any{"imported": 0, "unchanged": true})
		return
	case err != nil:
		// 503 rather than 500: the register is somebody else's service, and the
		// answer to "it is not configured" or "it did not answer" is to try
		// again or to configure it, not to report a fault in this platform.
		nexus.Error(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	nexus.Audit(r.Context(), tenantID, actorOf(r), "urtuu.ring_imported", "urtuu_request_code",
		map[string]any{"count": imported})
	nexus.JSON(w, http.StatusOK, map[string]any{"imported": imported})
}
