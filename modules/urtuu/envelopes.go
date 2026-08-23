/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * What crosses the boundary: work going down, and news coming back up.
 *
 * Two kinds and no more. A task assignment goes from a parent to one child; a
 * task update goes from a child back to the parent for one task. Everything
 * else the board does — assigning somebody, editing a note, closing a
 * completed task — is local, and deliberately: an organisation's internal
 * arrangements are its own (§2.4), and only the state of the work travels.
 */

package urtuu

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/urtuu"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	contract "github.com/gerege-systems/open-gerege-nexus/backend/pkg/urtuu"
	"github.com/jackc/pgx/v5"
)

// assignment is the payload of a task.assigned envelope.
type assignment struct {
	// TaskID is the *sender's* row id — the mirror it keeps of this work. It
	// comes back on every update, which is how the two sides stay matched
	// without either of them having to know the other's database.
	TaskID string `json:"task_id"`
	// Number is the sender's own register number for this dispatch. Display
	// only — the receiving installation cites it the way an incoming letter
	// cites the sender's reference, and matches on TaskID as before.
	Number string `json:"number,omitempty"`
	Code   string `json:"code"`
	// Line is which promise this work is under. It travels because the
	// receiving installation has to know: a service request it accepts is one
	// it cannot close without an answer for somebody outside the platform.
	Line    string          `json:"line"`
	Title   string          `json:"title"`
	Payload json.RawMessage `json:"payload"`
	// Applicant is who asked, on the service line. It moves downward with the
	// work — the office that has to issue a certificate cannot issue it to
	// nobody — and nothing else the sending installation knows about them
	// travels with it.
	Applicant json.RawMessage `json:"applicant,omitempty"`
	// Deadline is the sender's, and it is absolute rather than a duration:
	// the two clocks disagree, and a duration would be measured from whichever
	// of them happened to be reading.
	Deadline *time.Time `json:"deadline,omitempty"`
	// OriginChain is every installation this work has passed through. It is the
	// cycle guard (§9) and it is why a graph rather than a tree is safe.
	OriginChain []string `json:"origin_chain"`
	// Evidence is what backs the work up — in practice a signed order, filed at
	// the installation that raised it. References only; see evidence.go.
	Evidence []contract.Evidence `json:"evidence,omitempty"`
}

// update is the payload of a task.update envelope.
type update struct {
	// TaskID is the *recipient's* row id, taken from the assignment that
	// started this. The sender of an update is naming a row in the receiver's
	// database, which is the only way an answer can find its question.
	TaskID string `json:"task_id"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
	// Answer is what is being told back to the applicant, on the service line.
	// It is the reason that line exists: the fulfilment has to arrive at the
	// installation the person applied to, or their question was answered
	// somewhere they will never see.
	Answer   string          `json:"answer,omitempty"`
	Evidence json.RawMessage `json:"evidence,omitempty"`
}

// -------------------------------------------------------------- sending down

// sendDown writes the mirror row for one subordinate and queues the work.
//
// The row is written first and inside the caller's transaction: if the enqueue
// fails, everything unwinds, because a mirror with nothing behind it would show
// on the tree as work in progress that nobody was ever asked to do.
func (m *Module) sendDown(ctx context.Context, tx pgx.Tx, tenantID, actorUserID string,
	parent Task, peerID string, deadline *time.Time) error {

	open, err := m.codeOpenOn(ctx, tenantID, peerID, parent.Code)
	if err != nil {
		return err
	}
	if !open {
		return domain.CodeNotOpenOn(parent.Code)
	}

	// Each dispatch is registered here as well, the way outgoing mail is: this
	// is the number the subordinate will cite back.
	mirrorNumber, err := nextNumber(ctx, tx, tenantID, parent.Line, time.Now())
	if err != nil {
		return err
	}

	var mirrorID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO urtuu_tasks
		    (tenant_id, number, code, line, title, payload, applicant, target_peer_id,
		     parent_task_id, origin_chain, status, deadline, evidence, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'RECEIVED', $11, $12, NULLIF($13, '')::uuid)
		RETURNING id`,
		tenantID, mirrorNumber, parent.Code, parent.Line, parent.Title, parent.Payload,
		domain.ApplicantOrEmpty(parent.Applicant), peerID, parent.ID,
		parent.OriginChain, deadline, parent.Evidence, actorUserID).Scan(&mirrorID); err != nil {
		return err
	}
	if err := m.record(ctx, tx, tenantID, mirrorID, string(contract.StatusReceived), actorUserID, "", ""); err != nil {
		return err
	}

	_, err = m.link.EnqueueTx(ctx, tx, tenantID, contract.KindTaskAssigned, assignment{
		TaskID:      mirrorID,
		Number:      mirrorNumber,
		Code:        parent.Code,
		Line:        parent.Line,
		Title:       parent.Title,
		Payload:     parent.Payload,
		Applicant:   parent.Applicant,
		Deadline:    deadline,
		OriginChain: parent.OriginChain,
		Evidence:    readEvidence(parent.Evidence),
	}, peerID)
	return err
}

// reportUp tells the installation that gave us this task where it has got to.
//
// Best effort in the sense that a failure here does not undo the local move:
// the work really has been accepted, and the enqueue is the one part that gets
// retried by the transport anyway. What it must not do is silently succeed —
// hence the log line, and hence the undelivered count on the links screen.
func (m *Module) reportUp(ctx context.Context, tenantID string, task Task, note string) {
	if task.OriginPeerID == "" || task.OriginTaskRef() == "" {
		return
	}
	// Refreshed before it is sent, and written back. A completion report is
	// signed while the task is already moving, so the count that travels has to
	// be the one as of now rather than the one as of when the document was
	// first attached.
	evidence := m.refreshEvidence(ctx, tenantID, readEvidence(task.Evidence))
	m.saveEvidence(ctx, tenantID, task.ID, evidence)
	encoded, err := evidenceJSON(evidence)
	if err != nil {
		slog.Warn("urtuu: could not render a task's attachments", "task_id", task.ID, "error", err)
		return
	}
	if _, err := m.link.Enqueue(ctx, tenantID, contract.KindTaskUpdate, update{
		TaskID: task.OriginTaskRef(),
		Status: task.Status,
		Note:   note,
		// The answer travels with every update rather than only with the
		// completion: a service request that was answered and then returned for
		// a correction has two answers to show, in order.
		Answer:   task.Answer,
		Evidence: encoded,
	}, task.OriginPeerID); err != nil {
		slog.Warn("urtuu: could not report a task's state upward",
			"task_id", task.ID, "status", task.Status, "error", err)
	}
}

// ------------------------------------------------------------ receiving down

// receiveAssignment turns an envelope into a task.
//
// Two refusals happen here and both answer upward rather than dropping the
// message. A cycle is refused because the work has already been through this
// installation and going round again would never terminate; an unknown code is
// refused because a task that cannot be read is a task nobody can do. In both
// cases the parent is told, with a reason, which is the whole difference
// between a refusal and a silence.
func (m *Module) receiveAssignment(ctx context.Context, message nexus.LinkMessage) error {
	var work assignment
	if err := json.Unmarshal(message.Payload, &work); err != nil {
		// From a verified sender, so retrying will not make it parse. Marked
		// read rather than left to fail in the log for ever.
		slog.Warn("urtuu: an assignment could not be read", "peer_id", message.PeerID, "error", err)
		return nil
	}

	ctx = nexus.WithTenantID(ctx, message.TenantID)

	if refusal := m.refuseAssignment(ctx, message, work); refusal != "" {
		if _, err := m.link.Enqueue(ctx, message.TenantID, contract.KindTaskUpdate, update{
			TaskID: work.TaskID,
			Status: string(contract.StatusReturned),
			Note:   refusal,
		}, message.PeerID); err != nil {
			return err
		}
		return nil
	}

	code, err := m.lookupCode(ctx, message.TenantID, work.Code)
	if err != nil {
		return err
	}

	tx, err := m.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The deadline the sender set, or this code's own norm measured from the
	// envelope's stamp — the sender's clock, because that is the moment the
	// work was promised against.
	deadline := domain.DeadlineFor(code, work.Deadline, message.CreatedAt)
	// Stored exactly as it arrived. These references are to documents at the
	// sending installation and cannot be read from here; a count re-derived
	// locally would be a lie with a number in it.
	incomingEvidence, err := evidenceJSON(work.Evidence)
	if err != nil {
		return err
	}

	// Registered on arrival, under this installation's own year and sequence.
	// The sender's number is kept beside it rather than reused: two registers,
	// two numbers, and the second cites the first.
	number, err := nextNumber(ctx, tx, message.TenantID, domain.LineOf(work.Line), time.Now())
	if err != nil {
		return err
	}

	var taskID string
	err = tx.QueryRow(ctx, `
		INSERT INTO urtuu_tasks
		    (tenant_id, number, origin_number, code, line, title, payload, applicant,
		     origin_peer_id, origin_task_id, origin_chain, status, deadline, evidence)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'RECEIVED', $12, $13)
		-- The unique index on (origin_peer_id, origin_task_id). A reader has to
		-- be safe to repeat: the envelope is идемпотент by message id, but the
		-- write that marks it read can fail after this one succeeded.
		ON CONFLICT (origin_peer_id, origin_task_id) WHERE origin_peer_id IS NOT NULL DO NOTHING
		RETURNING id`,
		message.TenantID, number, work.Number, work.Code, domain.LineOf(work.Line), work.Title,
		domain.PayloadOrEmpty(work.Payload), domain.ApplicantOrEmpty(work.Applicant),
		message.PeerID, work.TaskID, append(work.OriginChain, m.link.InstallationID()),
		deadline, incomingEvidence).Scan(&taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already here. Nothing to do and nothing wrong.
		return nil
	}
	if err != nil {
		return err
	}
	if err := m.record(ctx, tx, message.TenantID, taskID,
		string(contract.StatusReceived), "", message.PeerID, ""); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// refuseAssignment returns the reason this task cannot be taken, or empty.
//
// The lookup is here and the judgement is the domain's. A database failure is
// not a refusal in the ordinary sense — returning empty would have the caller
// create the task, and refusing outright would turn a transient fault into a
// permanent no — so it travels as its own sentence, which the parent can tell
// apart when it retries.
func (m *Module) refuseAssignment(ctx context.Context, message nexus.LinkMessage, work assignment) string {
	code, err := m.lookupCode(ctx, message.TenantID, work.Code)
	found := err == nil
	failed := err != nil && !errors.Is(err, pgx.ErrNoRows)
	if failed {
		slog.Warn("urtuu: could not check an incoming task's code", "code", work.Code, "error", err)
	}
	return domain.AssignmentRefusal(work.OriginChain, m.link.InstallationID(),
		work.Code, code, found, failed)
}

// ------------------------------------------------------------- receiving back

// receiveUpdate applies a subordinate's news to the mirror this side keeps.
func (m *Module) receiveUpdate(ctx context.Context, message nexus.LinkMessage) error {
	var news update
	if err := json.Unmarshal(message.Payload, &news); err != nil {
		slog.Warn("urtuu: an update could not be read", "peer_id", message.PeerID, "error", err)
		return nil
	}
	if !contract.KnownStatus(contract.TaskStatus(news.Status)) {
		slog.Warn("urtuu: an update carried an unknown status",
			"peer_id", message.PeerID, "status", news.Status)
		return nil
	}

	ctx = nexus.WithTenantID(ctx, message.TenantID)
	tx, err := m.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// `target_peer_id = $2` is the whole authorization: a link may move the
	// mirrors of the work it was actually given and nothing else.
	var from, parentID string
	err = tx.QueryRow(ctx, `
		SELECT status, coalesce(parent_task_id::text, '')
		  FROM urtuu_tasks
		 WHERE id = $1 AND tenant_id = $3 AND target_peer_id = $2
		 FOR UPDATE`, news.TaskID, message.PeerID, message.TenantID).Scan(&from, &parentID)
	if errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("urtuu: an update named a task this link was never given",
			"peer_id", message.PeerID, "task_id", news.TaskID)
		return nil
	}
	if err != nil {
		return err
	}

	// A mirror is not a state machine this side owns — it is somebody else's
	// state copied here — so the transition table is not applied to it. What is
	// applied is monotonicity: deliveries retry and can arrive out of order,
	// and an older update must not walk a finished task backwards.
	if !domain.SupersededBy(from, news.Status) {
		return nil
	}

	if _, err := tx.Exec(ctx, `
		UPDATE urtuu_tasks
		   SET status = $2, note = CASE WHEN $3 = '' THEN note ELSE $3 END,
		       answer = CASE WHEN $5 = '' THEN answer ELSE $5 END,
		       evidence = coalesce($4, evidence), updated_at = NOW()
		 WHERE id = $1`, news.TaskID, news.Status, news.Note,
		evidenceOrNil(news.Evidence), news.Answer); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO urtuu_task_events
		    (tenant_id, task_id, from_status, to_status, actor_peer_id, note)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		message.TenantID, news.TaskID, from, news.Status, message.PeerID, news.Note); err != nil {
		return err
	}
	tasksTotal.WithLabelValues(news.Status).Inc()
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	if parentID != "" {
		m.rollUp(ctx, message.TenantID, parentID)
	}
	return nil
}

// rollUp completes a delegated task once every branch of it has finished.
//
// This is what makes a fan-out a single piece of work rather than a list of
// them: the ministry does not have to watch twenty-one provinces and decide for
// itself when the count is done.
//
// A returned branch does not complete the parent. Somebody has to read the
// reason and decide, which is the difference between a refusal and a delay.
func (m *Module) rollUp(ctx context.Context, tenantID, parentID string) {
	ctx = nexus.WithTenantID(ctx, tenantID)

	var outstanding int
	if err := m.db.QueryRow(ctx, `
		SELECT count(*) FROM urtuu_tasks
		 WHERE parent_task_id = $1 AND target_peer_id IS NOT NULL AND status <> 'COMPLETED'`,
		parentID).Scan(&outstanding); err != nil {
		slog.Warn("urtuu: could not check a delegated task's branches", "task_id", parentID, "error", err)
		return
	}
	if outstanding > 0 {
		return
	}

	// On the service line the answer has to arrive where the person applied,
	// not merely where the work was done. So the branches' answers are gathered
	// onto the task they were split from before it completes — which is also
	// what makes the roll-up possible at all: the schema refuses a completed
	// service request with nothing to tell the applicant (migration 00065).
	m.gatherAnswers(ctx, tenantID, parentID)

	parent, err := m.move(ctx, tenantID, parentID, contract.StatusCompleted, "", "",
		"every delegated branch is complete")
	if err != nil {
		// Ordinary when a parent has already been completed or returned by
		// hand, which is why this is a debug line and not a warning.
		slog.Debug("urtuu: a delegated task did not roll up", "task_id", parentID, "error", err)
		return
	}
	m.reportUp(ctx, tenantID, parent, "every delegated branch is complete")
	// And upward again: a chain four installations deep completes from the
	// bottom, one link at a time.
	if parent.ParentTaskID != "" {
		m.rollUp(ctx, tenantID, parent.ParentTaskID)
	}
}

// gatherAnswers copies what the branches said back onto the task above them.
//
// One branch answers as itself; several are joined and each is named, because
// "the certificate was issued" from one office and "no record found" from
// another are two different answers to the same request and the person who
// asked is entitled to both.
//
// Nothing is done on the assignment line, and nothing is overwritten: an
// answer already written here was written by somebody at this installation,
// and a roll-up is not the thing that should replace it.
func (m *Module) gatherAnswers(ctx context.Context, tenantID, parentID string) {
	ctx = nexus.WithTenantID(ctx, tenantID)

	var line, existing string
	if err := m.db.QueryRow(ctx,
		`SELECT line, answer FROM urtuu_tasks WHERE id = $1`, parentID).Scan(&line, &existing); err != nil {
		return
	}
	if line != contract.LineService || strings.TrimSpace(existing) != "" {
		return
	}

	rows, err := m.db.Query(ctx, `
		SELECT coalesce(t.target_peer_id::text, ''), t.answer
		  FROM urtuu_tasks t
		 WHERE t.parent_task_id = $1 AND t.target_peer_id IS NOT NULL AND t.answer <> ''
		 ORDER BY t.created_at`, parentID)
	if err != nil {
		slog.Warn("urtuu: could not gather a request's answers", "task_id", parentID, "error", err)
		return
	}
	defer rows.Close()

	branches := make([]domain.BranchAnswer, 0, 8)
	for rows.Next() {
		var branch domain.BranchAnswer
		if err := rows.Scan(&branch.Peer, &branch.Answer); err != nil {
			return
		}
		branches = append(branches, branch)
	}
	if err := rows.Err(); err != nil {
		return
	}
	// Peer holds an id at this point: the query stopped joining the channel's
	// table (see peers.go). The joined answer names who said what, so a link
	// that has been revoked since keeps its id rather than losing the
	// attribution — a dash beside one of three answers is worse than an id.
	if len(branches) > 0 {
		names := m.peerNames(ctx, nexus.TenantOf(ctx))
		for i := range branches {
			if name := names[branches[i].Peer]; name != "" {
				branches[i].Peer = name
			}
		}
	}
	answer := domain.JoinAnswers(branches)
	if answer == "" {
		return
	}
	if _, err := m.db.Exec(ctx,
		`UPDATE urtuu_tasks SET answer = $2, updated_at = NOW() WHERE id = $1`,
		parentID, answer); err != nil {
		slog.Warn("urtuu: could not record a request's answer", "task_id", parentID, "error", err)
	}
}

// OriginTaskRef is the id this task has on the installation that sent it.
//
// A method rather than a field on Task because it is never shown: the id means
// nothing here, it is only ever quoted back. Keeping it off the JSON keeps
// another installation's internal identifier out of this one's API.
func (t Task) OriginTaskRef() string { return t.originTaskID }

func evidenceOrNil(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}
