package documents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/documents"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	coreeid "github.com/gerege-systems/open-gerege-core/pkg/eid"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// E-ID signing is not a form the platform fills in on the citizen's behalf. eID
// Mongolia has no document-signing endpoint: what it has is an approval a citizen
// gives on their own registered device, with their own credentials, against a
// display text we choose. That approval *is* the signature, so the ceremony is
//
//	start  — push "sign <document>" to the citizen's eID app
//	poll   — wait for them to approve, refuse, or let it expire
//
// and the signature is recorded the moment the approval comes back COMPLETE.
//
// The provider mints the session id, so the pairing of session to document can
// only be written after the push. It is written with a context that outlives the
// caller's, because that pairing is what stops an approval given for one document
// from being presented as a signature on another — a citizen must never be able to
// approve something nothing can attach their signature to. It is single-use, and
// it is spent in the same transaction as the signature it produces.

// signSessionGrace is how far past eID's own deadline a session is still
// accepted, so a small clock difference between us and the provider does not
// throw away an approval the citizen gave in time.
const signSessionGrace = 2 * time.Minute

// signSessionBackstop bounds a session eID gave NO deadline for. eID states none
// for a push session — it decides when one dies and says so with EXPIRED — so
// there is nothing to count down, only a limit past which an approval id sitting
// in a log must stop being redeemable.
//
// It is set well beyond any real ceremony on purpose. A push session was measured
// still RUNNING nine minutes after it was created, and treating a shorter figure
// as a deadline is what made the sign-in flow walk away from sessions eID was
// still waiting on: a citizen who takes longer than that to unlock a phone, find
// the notification and enter a PIN loses the approval they were about to give.
// This is a backstop, not a deadline.
const signSessionBackstop = 15 * time.Minute

// parseExpiry reads the provider's deadline, and reports the zero time when the
// provider states none.
//
// It used to invent "two minutes from now" in that case. eID gives no deadline
// for a push session, so that figure was not a deadline but a cutoff of our own
// making — and it would have thrown away an approval the citizen was still in the
// middle of giving. A session with no stated deadline is bounded by
// signSessionBackstop instead, and ended by eID's own EXPIRED.
func parseExpiry(value string) time.Time {
	if at, err := time.Parse(time.RFC3339, strings.TrimSpace(value)); err == nil {
		return at
	}
	return time.Time{}
}

// ErrSignSessionUnknown is returned for a session this tenant did not start for
// this document. Whether it never existed, belongs elsewhere, or was already
// spent is deliberately not distinguished.
var ErrSignSessionUnknown = errors.New("signature session not found for this document")

// Approval states, as the caller sees them. They mirror eID's own vocabulary so a
// screen can report exactly what happened.
const (
	ApprovalRunning  = coreeid.StateRunning  // pushed, waiting for the citizen
	ApprovalComplete = coreeid.StateComplete // approved and signed
	ApprovalRefused  = coreeid.StateRefused  // the citizen declined
	ApprovalExpired  = coreeid.StateExpired  // nobody answered in time
)

// EIDSignSession is what the citizen's device needs in order to be found, plus
// what the operator reads out to them so both sides know it is the same request.
type EIDSignSession struct {
	SessionID        string `json:"session_id"`
	VerificationCode string `json:"verification_code"`
	// Empty when eID states no deadline, which is the normal case for a push
	// session. Omitted rather than sent empty, so the screen reads it as "nobody
	// said when this dies" instead of as a deadline it failed to parse.
	ExpiresAt     string `json:"expires_at,omitempty"`
	DeviceLinkURL string `json:"device_link_url,omitempty"`
	// DisplayText is what the citizen is being shown. Returned so the screen can
	// display the same words rather than a paraphrase of them.
	DisplayText string `json:"display_text"`
}

// EIDSignProgress is one answer to "has the citizen approved yet?". Document is
// filled in only once the signature has been recorded.
type EIDSignProgress struct {
	State    string    `json:"state"`
	Document *Document `json:"document,omitempty"`
}

// eID shows the display text as Smart-ID's displayText60, and the core client
// truncates it to 60 BYTES with a raw slice (pkg/eid newAuthBody). Cyrillic is two
// bytes a letter, so a Mongolian sentence is cut at about half its length and can
// be cut mid-letter, leaving invalid UTF-8 in the request body.
//
// Two consequences shape signatureDisplayText: the purpose has to come FIRST,
// because it is the tail that gets lost, and the cut has to be ours, on rune
// boundaries, so core's byte slice never fires. An earlier version put the title
// first and budgeted 120 — so the words saying a signature was being given were
// exactly what disappeared, and the citizen was asked to approve a bare fragment
// of a title.
const eidDisplayTextBytes = 60

// signatureDisplayText is what the citizen reads on their own device. It names the
// document, because approving "Gerege Nexus-д нэвтрэх" is not consent to sign a
// contract.
func signatureDisplayText(title string) string {
	const prefix = "Гарын үсэг: "
	const ellipsis = "…"

	title = strings.TrimSpace(title)
	if title == "" {
		return "Баримтад гарын үсэг зурах"
	}
	if len(prefix)+len(title) <= eidDisplayTextBytes {
		return prefix + title
	}

	budget := eidDisplayTextBytes - len(prefix) - len(ellipsis)
	if budget <= 0 {
		// Cannot happen with the prefix above, but a shorter budget than the
		// marker must not produce a negative slice.
		return prefix
	}

	var b strings.Builder
	b.WriteString(prefix)
	used := 0
	for _, r := range title {
		size := utf8.RuneLen(r)
		if used+size > budget {
			break
		}
		b.WriteRune(r)
		used += size
	}
	b.WriteString(ellipsis)
	return b.String()
}

// StartEIDSignature pushes an approval request for this document to the citizen's
// eID app and remembers which document it was for.
func (m *DocumentsModule) StartEIDSignature(ctx context.Context, tenantID, docID, regNumber string) (*EIDSignSession, error) {
	// What this module can see is wrong is refused before any work is done on the
	// caller's behalf — and so that anything the provider refuses later can be
	// treated as the provider's trouble, which a polling client must retry rather
	// than abandon a ceremony over.
	// On the RAW value, before normalising: strings.ToUpper replaces an invalid byte
	// with U+FFFD, so checking afterwards would find a perfectly storable string of
	// replacement characters and let a nonsense number through as if it were a name.
	if fault := textFault(regNumber); fault != "" {
		return nil, fmt.Errorf("%w: that registration number cannot be stored — %s",
			ErrSignatureRejected, fault)
	}
	regNumber = domain.NormaliseRegNumber(regNumber)
	if !domain.PlausibleRegNumber(regNumber) {
		return nil, fmt.Errorf("%w: %q is not a registration number", ErrSignatureRejected, regNumber)
	}

	pre, err := m.preflightSignature(ctx, tenantID, docID, SignerEID)
	if err != nil {
		return nil, err
	}
	// Refusing here saves pushing a request that could never be turned into a
	// signature, and tells the operator why before the citizen is involved.
	if err := checkSigner(pre.Position, pre.DocType, regNumber, pre.Policy.RequireNamedSigner); err != nil {
		return nil, err
	}
	// Asking somebody to approve on their own device and then discarding it because
	// they had already signed is worse than not asking.
	signed, err := m.alreadySigned(ctx, m.db, tenantID, docID, regNumber)
	if err != nil {
		return nil, err
	}
	if signed {
		return nil, ErrAlreadySigned
	}

	var title string
	if err := m.db.QueryRow(ctx,
		`SELECT title FROM document_records WHERE id = $1 AND tenant_id = $2`,
		docID, tenantID).Scan(&title); err != nil {
		if isNoRows(err) {
			return nil, ErrNotSignable
		}
		return nil, fmt.Errorf("read document title: %w", err)
	}

	// Housekeeping happens BEFORE the citizen is troubled. An approval nobody
	// answers is never polled again, so its row would sit here for good; but a
	// failure to tidy up must not cost a live approval prompt, which is what
	// happened when this ran after the push and returned its error.
	if _, err := m.db.Exec(ctx,
		// Both halves: a session eID gave a deadline for, and one it did not — the
		// second is the normal case for a push session, and keying only on
		// expires_at would have left those rows for good.
		`DELETE FROM document_eid_sign_sessions
		  WHERE tenant_id = $1 AND document_id = $2 AND consumed_at IS NULL
		    AND COALESCE(expires_at, created_at) < NOW() - INTERVAL '1 hour'`,
		tenantID, docID); err != nil {
		slog.WarnContext(ctx, "could not clear stale document signature sessions",
			"tenant_id", tenantID, "document_id", docID, "error", err)
	}

	displayText := signatureDisplayText(title)

	// What the document carries decides which ceremony it gets. A file is
	// signed over its own digest through the platform's signing rail; a
	// document carrying nothing is approved on the sign-in rail below, which
	// proves identity and intent and not content. See ADR 0003.
	artifact, err := m.ArtifactOf(ctx, tenantID, docID)
	if err != nil {
		return nil, err
	}
	if artifact.Present() {
		return m.startContentSignature(ctx, tenantID, docID, regNumber, artifact, pre, displayText)
	}

	started, err := m.eidSvc.StartSignature(ctx, regNumber, displayText, "")
	if err != nil {
		return nil, fmt.Errorf("%w: E-ID could not reach the signer: %w", ErrProviderUnavailable, err)
	}

	// The citizen has been asked by this point, so that is recorded before anything
	// else can fail. Who asked for a signature is not who gave it, and the ledger
	// only ever knows the latter — this is the other half of the trail, and it must
	// not go missing because the bookkeeping below did.
	nexus.Audit(ctx, tenantID, actorFor(ctx), "documents.signature_requested", docID, map[string]any{
		"signer_reg_number": regNumber,
		"display_text":      displayText,
		"session_id":        started.SessionID,
	})

	// The citizen's phone is now showing the request, so the pairing that makes it
	// redeemable has to be written even if the caller has gone away — otherwise
	// they could approve a document nothing can attach their signature to.
	// NULL when eID stated no deadline, so nothing downstream mistakes our own
	// arithmetic for the provider's.
	var expiresAt *time.Time
	if at := parseExpiry(started.ExpiresAt); !at.IsZero() {
		expiresAt = &at
	}
	// Uncancellable, because the citizen's phone is already showing the request — but on
	// its own deadline, the way recordSignature is. Without one there is nothing to stop
	// a stalled database holding this handler, and the connection it is waiting on, for
	// as long as the stall lasts.
	bookkeeping, cancelBookkeeping := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancelBookkeeping()
	if _, err := m.db.Exec(bookkeeping,
		`INSERT INTO document_eid_sign_sessions
		        (session_id, tenant_id, document_id, reg_number, display_text, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		started.SessionID, tenantID, docID, regNumber, displayText, expiresAt); err != nil {
		return nil, fmt.Errorf("record signature session: %w", err)
	}

	// The citizen has been asked again, so the previous asking is over: only the newest
	// request for this document and this citizen stays redeemable. Thirty retries used
	// to leave thirty live rows, every one of them an approval id that could still be
	// turned into a signature.
	//
	// After the insert, deliberately. Retiring the old one first would mean a push that
	// then failed left the citizen holding a prompt for a session nothing could redeem —
	// their approval given and lost. Best-effort for the same reason the sweep is: the
	// prompt on their phone is real whatever this row does.
	if _, err := m.db.Exec(bookkeeping,
		`DELETE FROM document_eid_sign_sessions
		  WHERE tenant_id = $1 AND document_id = $2 AND reg_number = $3
		    AND session_id <> $4 AND consumed_at IS NULL`,
		tenantID, docID, regNumber, started.SessionID); err != nil {
		slog.WarnContext(ctx, "started a new signature session but could not retire the previous one",
			"document_id", docID, "error", err)
	}

	return &EIDSignSession{
		SessionID:        started.SessionID,
		VerificationCode: started.VerificationCode,
		ExpiresAt:        started.ExpiresAt,
		DeviceLinkURL:    started.DeviceLinkURL,
		DisplayText:      displayText,
	}, nil
}

// PollEIDSignature asks eID whether the citizen has approved, and records the
// signature the moment they have. It is safe to call repeatedly: the session is
// spent on the first completion, and a spent session answers with the document it
// already produced rather than signing twice.
func (m *DocumentsModule) PollEIDSignature(ctx context.Context, tenantID, docID, sessionID string) (*EIDSignProgress, error) {
	if uuid.Validate(docID) != nil {
		return nil, ErrSignSessionUnknown
	}

	var regNumber string
	var consumed, expired bool
	err := m.db.QueryRow(ctx,
		`SELECT reg_number, consumed_at IS NOT NULL,
		        CASE WHEN expires_at IS NOT NULL
		             -- eID stated a deadline: it decides, plus a little for clock skew.
		             THEN expires_at < NOW() - $4::interval
		             -- It stated none. Not expired for as long as the backstop allows;
		             -- eID's own EXPIRED is what ends the wait.
		             ELSE created_at < NOW() - $5::interval END
		   FROM document_eid_sign_sessions
		  WHERE session_id = $1 AND tenant_id = $2 AND document_id = $3`,
		sessionID, tenantID, docID,
		signSessionGrace.String(), signSessionBackstop.String()).Scan(&regNumber, &consumed, &expired)
	if isNoRows(err) {
		return nil, ErrSignSessionUnknown
	}
	if err != nil {
		return nil, fmt.Errorf("load signature session: %w", err)
	}

	if consumed {
		doc, err := m.getDocument(ctx, tenantID, docID)
		if err != nil {
			return nil, err
		}
		return &EIDSignProgress{State: ApprovalComplete, Document: doc}, nil
	}

	// A ceremony that named a digest was a signature over the document's file,
	// and it is finished on the rail that started it. The two cannot be mixed:
	// polling a signing session on the sign-in rail would find nothing and
	// report an expiry over a signature the citizen had given.
	if session, err := m.loadSignSession(ctx, tenantID, docID, sessionID); err == nil && session.RequestedDigest != "" {
		return m.pollContentSignature(ctx, tenantID, docID, sessionID, session)
	}

	// An approval the citizen gave days ago is not one to turn into a signature
	// dated today. eID's own deadline decides, with a little grace for clock skew.
	if expired {
		// A session that produced a signature is never deleted, and never reported
		// expired. Both used to be possible in the same breath: this poll reads the row
		// as expired while another one, a second ahead of it, is committing the
		// signature and the spending of that same row. The DELETE would then remove a
		// row the other transaction was writing to — losing the record of the ceremony
		// — and this poll would tell the operator the request expired over a document
		// their citizen had just signed.
		//
		// So the delete asks for the row it means: unspent. If it finds none, the
		// ceremony happened, and the answer is what it produced.
		tag, err := m.db.Exec(ctx,
			`DELETE FROM document_eid_sign_sessions
			  WHERE session_id = $1 AND tenant_id = $2 AND consumed_at IS NULL`,
			sessionID, tenantID)
		if err != nil {
			slog.WarnContext(ctx, "could not delete expired document signature session",
				"session_id", sessionID, "error", err)
			return &EIDSignProgress{State: ApprovalExpired}, nil
		}
		if tag.RowsAffected() == 0 {
			signed, load := m.getDocument(ctx, tenantID, docID)
			if load != nil {
				return nil, load
			}
			return &EIDSignProgress{State: ApprovalComplete, Document: signed}, nil
		}
		return &EIDSignProgress{State: ApprovalExpired}, nil
	}

	result, err := m.eidSvc.Poll(ctx, sessionID)
	if err != nil {
		// A dropped connection to eID is not an answer. Reporting it as the caller's
		// mistake made the dialog give up on a ceremony the citizen was still holding
		// their phone for.
		return nil, fmt.Errorf("%w: E-ID could not be reached: %w", ErrProviderUnavailable, err)
	}

	if result.State != ApprovalComplete {
		// A refused or expired request is over; dropping the row keeps a dead
		// session from being polled forever. The citizen's answer is the one thing
		// the operator needs, so it is returned whether or not the tidy-up worked —
		// making it contingent on housekeeping turned "they declined" into a 500.
		if result.State == ApprovalRefused || result.State == ApprovalExpired {
			if _, err := m.db.Exec(ctx,
				`DELETE FROM document_eid_sign_sessions WHERE session_id = $1 AND tenant_id = $2`,
				sessionID, tenantID); err != nil {
				slog.WarnContext(ctx, "could not clear finished document signature session",
					"session_id", sessionID, "error", err)
			}
		}
		return &EIDSignProgress{State: result.State}, nil
	}

	if result.Identity == nil {
		return nil, fmt.Errorf("%w: E-ID reported an approval without an identity", ErrProviderUnavailable)
	}

	// The approval has to come from the citizen the request was addressed to.
	approved := domain.NormaliseRegNumber(result.Identity.RegNumber)
	if approved != regNumber {
		return nil, fmt.Errorf("%w: the request was sent to %s but %s approved it",
			ErrSignatureRejected, regNumber, approved)
	}

	// Every refusal from here on asks one question first: did THIS session already
	// produce a signature?
	//
	// Two polls of one session can reach this together — two tabs, or a retry that
	// overlapped its predecessor. They queue on the document's row lock, so the loser
	// arrives after the winner has committed both the signature and the spending of
	// this session, and every guard below then refuses it for a reason that is true of
	// the document but false of the ceremony: the document is no longer pending
	// (ErrNotSignable) because this approval approved it, or the citizen has already
	// signed (ErrAlreadySigned) because this approval is their signature. Reporting
	// either to the operator's second tab calls a completed ceremony a failure.
	//
	// consumed_at is set only inside the transaction that records a signature FOR this
	// session, so it is safe in the other direction too: if this session produced
	// nothing, the refusal is the truth and it is returned unchanged.
	refuse := func(err error) (*EIDSignProgress, error) {
		var consumed bool
		if check := m.db.QueryRow(ctx,
			`SELECT consumed_at IS NOT NULL FROM document_eid_sign_sessions
			  WHERE session_id = $1 AND tenant_id = $2 AND document_id = $3`,
			sessionID, tenantID, docID).Scan(&consumed); check != nil || !consumed {
			return nil, err
		}
		signed, load := m.getDocument(ctx, tenantID, docID)
		if load != nil {
			return nil, load
		}
		return &EIDSignProgress{State: ApprovalComplete, Document: signed}, nil
	}

	// The policy and chain are re-read here rather than trusted from start time:
	// minutes may have passed, and it is this moment that decides.
	pre, err := m.preflightSignature(ctx, tenantID, docID, SignerEID)
	if err != nil {
		return refuse(err)
	}
	if err := checkSigner(pre.Position, pre.DocType, approved, pre.Policy.RequireNamedSigner); err != nil {
		return refuse(err)
	}

	identity := result.Identity
	signature := &verifiedSignature{
		SignerName:        strings.TrimSpace(identity.FirstName+" "+identity.LastName) + " (E-ID баталгаажсан)",
		RegNumber:         approved,
		Hash:              "eid_session_" + sessionID,
		CertificateSerial: identity.CertificateSerial,
		CertificateIssuer: identity.CertificateIssuer,
	}

	// The session is spent in the same transaction as the signature, so a recorded
	// signature and an unspent session can never disagree. Doing it afterwards let
	// a dropped connection report a 500 for a signature that had in fact landed,
	// and left the session redeemable a second time.
	doc, err := m.recordSignature(ctx, tenantID, docID, SignerEID, signature, sessionID)
	if err != nil {
		return refuse(err)
	}

	return &EIDSignProgress{State: ApprovalComplete, Document: doc}, nil
}

func (m *DocumentsModule) startEIDSignatureHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	var req struct {
		RegNumber string `json:"reg_number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.RegNumber) == "" {
		nexus.Error(w, http.StatusBadRequest, "invalid signature request: reg_number is required")
		return
	}

	session, err := m.StartEIDSignature(r.Context(), tenantID, chi.URLParam(r, "id"), req.RegNumber)
	switch {
	case errors.Is(err, ErrNotSignable), errors.Is(err, ErrAlreadySigned):
		nexus.Error(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, ErrProviderUnavailable):
		nexus.Error(w, http.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, ErrSignatureRejected):
		nexus.Error(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		nexus.Error(w, http.StatusInternalServerError, "failed to start the signature request")
		return
	}
	nexus.JSON(w, http.StatusOK, session)
}

func (m *DocumentsModule) pollEIDSignatureHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.SessionID) == "" {
		nexus.Error(w, http.StatusBadRequest, "invalid poll request: session_id is required")
		return
	}

	progress, err := m.PollEIDSignature(r.Context(), tenantID, chi.URLParam(r, "id"), req.SessionID)
	switch {
	case errors.Is(err, ErrSignSessionUnknown):
		nexus.Error(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, ErrNotSignable), errors.Is(err, ErrAlreadySigned):
		nexus.Error(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, ErrProviderUnavailable):
		nexus.Error(w, http.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, ErrSignatureRejected):
		nexus.Error(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		nexus.Error(w, http.StatusInternalServerError, "failed to complete the signature")
		return
	}
	nexus.JSON(w, http.StatusOK, progress)
}
