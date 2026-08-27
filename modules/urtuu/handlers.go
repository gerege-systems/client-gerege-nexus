/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * The board's HTTP surface.
 *
 * Every handler that moves a task does the same three things in the same order:
 * move it locally, tell the installation that gave it to us, and answer. The
 * order matters — the local move is the fact, and the envelope is the report of
 * it, so a channel that is down delays the news rather than the work.
 */

package urtuu

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/urtuu"
	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// maxTaskBody bounds a task body. The payload is a filled-in form, and the
// contract's own envelope ceiling is what it has to fit inside anyway.
const maxTaskBody = contract.MaxPayloadBytes

func (m *Module) handleListTasks(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	query := r.URL.Query()
	filter := taskFilter{
		Direction: query.Get("direction"),
		Line:      strings.TrimSpace(query.Get("line")),
		Status:    strings.ToUpper(strings.TrimSpace(query.Get("status"))),
		Code:      strings.TrimSpace(query.Get("code")),
		Overdue:   query.Get("overdue") == "true",
		ParentID:  strings.TrimSpace(query.Get("parent_id")),
	}
	// A status the machine does not know would match nothing and read as "there
	// is no work", which is the wrong answer to a typo.
	if filter.Status != "" && !contract.KnownStatus(contract.TaskStatus(filter.Status)) {
		nexus.Error(w, http.StatusBadRequest, domain.UnknownStatus(filter.Status).Error())
		return
	}
	if filter.Line != "" && !contract.KnownLine(filter.Line) {
		nexus.Error(w, http.StatusBadRequest, domain.UnknownLine(filter.Line).Error())
		return
	}
	if filter.ParentID != "" {
		if _, err := uuid.Parse(filter.ParentID); err != nil {
			nexus.Error(w, http.StatusBadRequest, "invalid parent id")
			return
		}
	}

	tasks, err := m.listTasks(r.Context(), tenantID, filter)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the tasks")
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

// handleGetTask answers with the task, its whole history and its branches.
//
// Three things in one response because the detail screen shows all three and a
// timeline fetched separately is a timeline that can be a version behind the
// status above it.
func (m *Module) handleGetTask(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := m.taskParty(w, r)
	if !ok {
		return
	}

	task, err := m.getTask(r.Context(), tenantID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		nexus.Error(w, http.StatusNotFound, "no such task")
		return
	}
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the task")
		return
	}

	// The attachments as they stand now, for anything filed here. What the
	// other installation was told is on the row; what the person looking at
	// this screen needs is the current signature count.
	evidence := m.refreshEvidence(r.Context(), tenantID, readEvidence(task.Evidence))

	events, err := m.taskEvents(r.Context(), tenantID, id)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the task's history")
		return
	}
	branches, err := m.listTasks(r.Context(), tenantID, taskFilter{ParentID: id})
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the task's branches")
		return
	}

	nexus.JSON(w, http.StatusOK, map[string]any{
		"task": task, "events": events, "branches": branches, "evidence": evidence,
		// What this task may do next, so the screen offers buttons rather than
		// guessing at the state machine a second time.
		"next": contract.TaskStatus(task.Status).Next(),
	})
}

type createRequest struct {
	Code     string          `json:"code"`
	Title    string          `json:"title"`
	Payload  json.RawMessage `json:"payload"`
	Deadline *time.Time      `json:"deadline"`
	// PeerIDs are the subordinate links this is being sent to. Empty means the
	// work stays here, which is a real case — an organisation raising its own
	// task under a code so that it is counted and timed like any other.
	PeerIDs []string `json:"peer_ids"`
	Note    string   `json:"note"`
	// Document attaches the official paperwork — an order that has been signed
	// with eID, or one filed here and now so that it can be. Optional: most
	// work needs no instrument, and the ones that do are the ones that have to
	// be able to prove it.
	Document *documentRequest `json:"document"`
	// Applicant is who asked. Required on the service line and refused on the
	// assignment line: an order from a ministry has no applicant, and letting
	// one be attached would invent a citizen behind an internal instruction.
	Applicant *contract.Applicant `json:"applicant"`
}

// handleCreateTask raises work and, if it names any links, sends it.
//
// One transaction for the root, the mirrors and the envelopes together: a
// fan-out that half happened would leave provinces doing work the ministry has
// no record of asking for.
func (m *Module) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxTaskBody)
	var request createRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	// A task kept here needs no channel at all, which is why this is checked
	// against what was asked for rather than at the door.
	if len(request.PeerIDs) > 0 && !m.link.Enabled() {
		nexus.Error(w, http.StatusServiceUnavailable, "Өртөө is not configured on this installation")
		return
	}

	code, err := m.lookupCode(r.Context(), tenantID, strings.TrimSpace(request.Code))
	if errors.Is(err, pgx.ErrNoRows) {
		nexus.Error(w, http.StatusBadRequest, "no such request code")
		return
	}
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the request code")
		return
	}
	if !code.Active {
		nexus.Error(w, http.StatusBadRequest, "that request code is not in use")
		return
	}

	// The line comes from the code, never from the request. A code imported
	// from ring.dgov.mn is a state service; one an organisation authored for
	// its own orders is an assignment. If the raiser could choose, one code
	// would be usable under two different promises — and the promise is the
	// whole distinction between the two lines.
	line := domain.LineOf(code.Line)
	applicant, err := domain.ApplicantFor(line, request.Applicant)
	if err != nil {
		nexus.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	// Before the transaction, because filing a document is somebody else's
	// write and must not be inside one this handler might roll back — a task
	// that failed to send would otherwise leave an orphan document in the
	// register with nothing pointing at it.
	var attached []contract.Evidence
	if !request.Document.empty() {
		evidence, err := m.attachDocument(r.Context(), tenantID, request.Document)
		if err != nil {
			nexus.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		attached = append(attached, evidence)
	}
	encodedEvidence, err := evidenceJSON(attached)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not record the attachment")
		return
	}

	now := time.Now()
	root := Task{
		Code:    code.Code,
		Line:    line,
		Title:   domain.TitleFor(code, request.Title, localeOf(r)),
		Payload: domain.PayloadOrEmpty(request.Payload),
		// The chain starts here. Every installation this work reaches adds
		// itself, and any that finds itself already on it refuses.
		OriginChain: []string{m.link.InstallationID()},
	}
	root.Evidence = encodedEvidence
	root.Applicant = applicant
	deadline := domain.DeadlineFor(code, request.Deadline, now)

	status := domain.InitialStatus(len(request.PeerIDs))

	ctx := nexus.WithWorkspaceID(r.Context(), tenantID)
	tx, err := m.db.Begin(ctx)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not raise the task")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	actor := actorOf(r)
	root.Number, err = nextNumber(ctx, tx, tenantID, line, now)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not allocate a register number")
		return
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO urtuu_tasks
		    (tenant_id, number, code, line, title, payload, applicant, origin_chain, status,
		     deadline, note, evidence, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULLIF($13, '')::uuid)
		RETURNING id`,
		tenantID, root.Number, root.Code, line, root.Title, root.Payload, applicant,
		root.OriginChain, string(status), deadline, strings.TrimSpace(request.Note),
		encodedEvidence, actor).Scan(&root.ID); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not raise the task")
		return
	}
	if err := m.record(ctx, tx, tenantID, root.ID, string(status), actor, "", request.Note); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not record the task")
		return
	}

	for _, peerID := range request.PeerIDs {
		if _, err := uuid.Parse(peerID); err != nil {
			nexus.Error(w, http.StatusBadRequest, "invalid link id")
			return
		}
		if err := m.sendDown(ctx, tx, tenantID, actor, root, peerID, deadline); err != nil {
			nexus.Error(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not raise the task")
		return
	}

	nexus.Audit(r.Context(), tenantID, actor, "urtuu.task_raised", "urtuu_task",
		map[string]any{"task_id": root.ID, "number": root.Number, "code": root.Code,
			"links": len(request.PeerIDs)})
	nexus.JSON(w, http.StatusCreated, map[string]any{
		"id": root.ID, "number": root.Number, "status": status,
	})
}

// handleDelegate splits a task this organisation was given, downward.
//
// The task itself moves to DELEGATED and one branch is created per link, which
// is the proposal's own rule (§4): a separate task per subordinate, joined to
// this one by parent_task_id, so the tree shows which province has finished and
// which has not.
func (m *Module) handleDelegate(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := m.taskParty(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxTaskBody)
	var request struct {
		PeerIDs  []string   `json:"peer_ids"`
		Deadline *time.Time `json:"deadline"`
		Note     string     `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(request.PeerIDs) == 0 {
		nexus.Error(w, http.StatusBadRequest, domain.ErrNoBranches.Error())
		return
	}

	task, err := m.getTask(r.Context(), tenantID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		nexus.Error(w, http.StatusNotFound, "no such task")
		return
	}
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the task")
		return
	}

	// The branches inherit this task's deadline unless a tighter one is set:
	// a province cannot be given longer than the ministry gave the agency.
	deadline := task.Deadline
	if request.Deadline != nil {
		deadline = request.Deadline
	}

	ctx := nexus.WithWorkspaceID(r.Context(), tenantID)
	tx, err := m.db.Begin(ctx)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not delegate the task")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	actor := actorOf(r)
	for _, peerID := range request.PeerIDs {
		if _, err := uuid.Parse(peerID); err != nil {
			nexus.Error(w, http.StatusBadRequest, "invalid link id")
			return
		}
		if err := m.sendDown(ctx, tx, tenantID, actor, task, peerID, deadline); err != nil {
			nexus.Error(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not delegate the task")
		return
	}

	// After the branches, not before: a task that said DELEGATED with nothing
	// under it would be a lie the tree could not correct.
	moved, err := m.move(r.Context(), tenantID, id, contract.StatusDelegated, actor, "", request.Note)
	if err != nil {
		m.refuse(w, err)
		return
	}
	m.reportUp(r.Context(), tenantID, moved, request.Note)

	nexus.Audit(r.Context(), tenantID, actor, "urtuu.task_delegated", "urtuu_task",
		map[string]any{"task_id": id, "links": len(request.PeerIDs)})
	nexus.JSON(w, http.StatusOK, map[string]any{"id": id, "status": moved.Status})
}

// The four moves an organisation makes on work it has been given, and the one
// the originator makes at the end. All five share a shape, so they share a
// helper — what differs is the target status and whether a note is required.

func (m *Module) handleAccept(w http.ResponseWriter, r *http.Request) {
	m.transition(w, r, contract.StatusAccepted, false, "urtuu.task_accepted")
}

// Returning demands a reason. A refusal with no reason is one the parent can
// only answer by asking, which turns a channel into a telephone call.
func (m *Module) handleReturn(w http.ResponseWriter, r *http.Request) {
	m.transition(w, r, contract.StatusReturned, true, "urtuu.task_returned")
}

func (m *Module) handleComplete(w http.ResponseWriter, r *http.Request) {
	m.transition(w, r, contract.StatusCompleted, false, "urtuu.task_completed")
}

// Closing is the originator accepting the outcome, so it never reports upward:
// there is nobody above the side that closes.
func (m *Module) handleClose(w http.ResponseWriter, r *http.Request) {
	m.transition(w, r, contract.StatusClosed, false, "urtuu.task_closed")
}

// handleAssign names who here is doing the work, which moves it to IN_PROGRESS.
//
// The person never travels. Who inside an organisation is doing something is
// that organisation's business (§2.4) — the parent learns that the work is in
// progress and nothing more.
func (m *Module) handleAssign(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := m.taskParty(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxTaskBody)
	var request struct {
		UserID string `json:"user_id"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	if _, err := uuid.Parse(request.UserID); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid user id")
		return
	}

	// Asked of the directory rather than of the memberships table. See
	// people.go for what that check is and why it refuses when it cannot be
	// made.
	if !m.isMember(r.Context(), tenantID, request.UserID) {
		nexus.Error(w, http.StatusBadRequest, "that person is not a member of this organisation")
		return
	}

	if _, err := m.db.Exec(nexus.WithWorkspaceID(r.Context(), tenantID),
		`UPDATE urtuu_tasks SET assigned_user_id = $2, updated_at = NOW() WHERE id = $1`,
		id, request.UserID); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not assign the task")
		return
	}

	moved, err := m.move(r.Context(), tenantID, id, contract.StatusInProgress, actorOf(r), "", request.Note)
	if err != nil {
		m.refuse(w, err)
		return
	}
	m.reportUp(r.Context(), tenantID, moved, "")

	nexus.Audit(r.Context(), tenantID, actorOf(r), "urtuu.task_assigned", "urtuu_task",
		map[string]any{"task_id": id})
	nexus.JSON(w, http.StatusOK, map[string]any{"id": id, "status": moved.Status})
}

// transition is the shared body of accept, return, complete and close.
func (m *Module) transition(w http.ResponseWriter, r *http.Request,
	to contract.TaskStatus, requireNote bool, action string) {

	tenantID, id, ok := m.taskParty(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxTaskBody)
	var request struct {
		Note string `json:"note"`
		// Answer is what the applicant is being told. Required to complete a
		// service-line task and meaningless on the assignment line.
		Answer string `json:"answer"`
		// Document is how a completion carries its proof: the report that was
		// signed, filed here and referenced by the update that goes upward.
		Document *documentRequest `json:"document"`
	}
	// An empty body is a legitimate accept, so a decode failure on no body is
	// not an error worth answering with.
	_ = json.NewDecoder(r.Body).Decode(&request)
	if requireNote && strings.TrimSpace(request.Note) == "" {
		nexus.Error(w, http.StatusBadRequest, domain.ErrReasonRequired.Error())
		return
	}

	// The service line's whole promise, checked here so the caller gets a
	// sentence rather than a constraint violation. The constraint is still
	// there — see migration 00065 — because a check in one handler is a check
	// that can be gone round, and "completed with nothing to tell the person
	// who asked" is the failure this line exists to prevent.
	current, err := m.getTask(r.Context(), tenantID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		nexus.Error(w, http.StatusNotFound, "no such task")
		return
	}
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the task")
		return
	}
	answer := strings.TrimSpace(request.Answer)
	if to == contract.StatusCompleted {
		if err := domain.CanComplete(current.Line, current.TargetPeerID, answer, current.Answer); err != nil {
			nexus.Error(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if answer != "" {
		if _, err := m.db.Exec(nexus.WithWorkspaceID(r.Context(), tenantID),
			`UPDATE urtuu_tasks SET answer = $2, updated_at = NOW() WHERE id = $1`,
			id, answer); err != nil {
			nexus.Error(w, http.StatusInternalServerError, "could not record the answer")
			return
		}
	}

	if !request.Document.empty() {
		evidence, err := m.attachDocument(r.Context(), tenantID, request.Document)
		if err != nil {
			nexus.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		encoded, err := withEvidence(current.Evidence, evidence)
		if err != nil {
			nexus.Error(w, http.StatusInternalServerError, "could not record the attachment")
			return
		}
		if _, err := m.db.Exec(nexus.WithWorkspaceID(r.Context(), tenantID),
			`UPDATE urtuu_tasks SET evidence = $2, updated_at = NOW() WHERE id = $1`,
			id, encoded); err != nil {
			nexus.Error(w, http.StatusInternalServerError, "could not record the attachment")
			return
		}
	}

	moved, err := m.move(r.Context(), tenantID, id, to, actorOf(r), "", request.Note)
	if err != nil {
		m.refuse(w, err)
		return
	}
	m.reportUp(r.Context(), tenantID, moved, request.Note)

	// Completing a branch may be what finishes the whole tree above it.
	if to == contract.StatusCompleted && moved.ParentTaskID != "" {
		m.rollUp(r.Context(), tenantID, moved.ParentTaskID)
	}

	nexus.Audit(r.Context(), tenantID, actorOf(r), action, "urtuu_task",
		map[string]any{"task_id": id, "status": string(to)})
	nexus.JSON(w, http.StatusOK, map[string]any{"id": id, "status": moved.Status})
}

// ------------------------------------------------------------------- board

// handleBoard is the app's front page: what is queued, what is late, and
// whether the links are carrying anything.
func (m *Module) handleBoard(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}
	ctx := nexus.WithWorkspaceID(r.Context(), tenantID)

	// Counts by status and direction in one pass. Two queries would be two
	// moments, and a board that adds up to a different total than its own rows
	// is a board nobody trusts twice.
	rows, err := m.db.Query(ctx, `
		SELECT CASE WHEN origin_peer_id IS NOT NULL THEN 'incoming'
		            WHEN target_peer_id IS NOT NULL THEN 'outgoing'
		            ELSE 'local' END AS direction,
		       line, status,
		       count(*),
		       count(*) FILTER (WHERE deadline IS NOT NULL AND deadline < NOW() AND status <> 'CLOSED')
		  FROM urtuu_tasks
		 WHERE tenant_id = $1
		 GROUP BY 1, 2, 3`, tenantID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the board")
		return
	}
	defer rows.Close()

	type tally struct {
		Direction string `json:"direction"`
		Line      string `json:"line"`
		Status    string `json:"status"`
		Count     int    `json:"count"`
		Overdue   int    `json:"overdue"`
	}
	counts := make([]tally, 0, 16)
	for rows.Next() {
		var item tally
		if err := rows.Scan(&item.Direction, &item.Line, &item.Status, &item.Count, &item.Overdue); err != nil {
			nexus.Error(w, http.StatusInternalServerError, "could not read the board")
			return
		}
		counts = append(counts, item)
	}
	if err := rows.Err(); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the board")
		return
	}

	// The red zone: what is late, soonest-overdue first, because that is the
	// order somebody would work through it.
	overdue, err := m.listTasks(r.Context(), tenantID, taskFilter{Overdue: true})
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the overdue tasks")
		return
	}

	links, err := m.linkHealth(ctx, tenantID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the links")
		return
	}
	trees, err := m.treeProgress(ctx, tenantID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the delegated tasks")
		return
	}

	nexus.JSON(w, http.StatusOK, map[string]any{
		"counts":  counts,
		"overdue": overdue,
		"links":   links,
		"trees":   trees,
		"enabled": m.link.Enabled(),
	})
}

// LinkHealth is one channel as the board shows it: is it speaking, and is
// anything stuck behind it.
type LinkHealth struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	Undelivered int        `json:"undelivered"`
	LastError   string     `json:"last_error,omitempty"`
}

// linkHealth asks the channel how its links are doing.
//
// It read urtuu_peers and urtuu_deliveries directly until 2026-08-23, with a
// note beside it arguing that the app and the transport "are one product split
// by layer, sharing one schema and one tenant binding". They are — while they
// share a repository. contract.PeerDirectory carries the same five columns as a
// contract, which is what lets this app leave.
func (m *Module) linkHealth(ctx context.Context, tenantID string) ([]LinkHealth, error) {
	peers, err := m.peers.Peers(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	links := make([]LinkHealth, 0, len(peers))
	for _, peer := range peers {
		links = append(links, LinkHealth{
			ID: peer.ID, Name: peer.Name, Role: peer.Role, Status: peer.Status,
			LastSeenAt: peer.LastSeenAt, LastError: peer.LastError,
			Undelivered: peer.Undelivered,
		})
	}
	return links, nil
}

// TreeProgress is one delegated task and how far its branches have got.
type TreeProgress struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Code  string `json:"code"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
	// Late counts the branches past their own deadline. A tree can be nearly
	// finished and still be the one to worry about.
	Late      int        `json:"late"`
	Deadline  *time.Time `json:"deadline,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// treeProgress answers the question the ministry actually has: of the
// twenty-one provinces, how many have finished and how many are late.
func (m *Module) treeProgress(ctx context.Context, tenantID string) ([]TreeProgress, error) {
	rows, err := m.db.Query(ctx, `
		SELECT t.id::text, t.title, t.code, t.deadline, t.created_at,
		       count(b.id) FILTER (WHERE b.status = 'COMPLETED'),
		       count(b.id),
		       count(b.id) FILTER (WHERE b.deadline IS NOT NULL AND b.deadline < NOW()
		                             AND b.status <> 'CLOSED')
		  FROM urtuu_tasks t
		  JOIN urtuu_tasks b ON b.parent_task_id = t.id AND b.target_peer_id IS NOT NULL
		 WHERE t.tenant_id = $1 AND t.status = 'DELEGATED'
		 GROUP BY t.id
		 ORDER BY t.created_at DESC
		 LIMIT 50`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	trees := make([]TreeProgress, 0, 16)
	for rows.Next() {
		var tree TreeProgress
		if err := rows.Scan(&tree.ID, &tree.Title, &tree.Code, &tree.Deadline,
			&tree.CreatedAt, &tree.Done, &tree.Total, &tree.Late); err != nil {
			return nil, err
		}
		trees = append(trees, tree)
	}
	return trees, rows.Err()
}

// ------------------------------------------------------------------ helpers

func (m *Module) taskParty(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return "", "", false
	}
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid task id")
		return "", "", false
	}
	return tenantID, id, true
}

// refuse turns a move failure into the right status.
//
// A refused transition is 409 and not 400: the request was well formed and the
// task is simply not where the caller thought it was — usually because
// somebody else, or a subordinate's envelope, moved it first.
func (m *Module) refuse(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrTransition):
		nexus.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, pgx.ErrNoRows):
		nexus.Error(w, http.StatusNotFound, "no such task")
	default:
		nexus.Error(w, http.StatusInternalServerError, "could not move the task")
	}
}

func actorOf(r *http.Request) string {
	if claims, err := nexus.UserFromContext(r.Context()); err == nil {
		return claims.UserID
	}
	return ""
}

// localeOf is what language a task's title is taken in when nobody typed one.
func localeOf(r *http.Request) string {
	locale := strings.TrimSpace(r.Header.Get("Accept-Language"))
	if len(locale) >= 2 {
		return strings.ToLower(locale[:2])
	}
	return "mn"
}
