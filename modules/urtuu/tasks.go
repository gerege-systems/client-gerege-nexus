/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * A task's life: raising one, moving it, and splitting it downward.
 *
 * Three shapes of row live in urtuu_tasks and the migration explains them; the
 * one to keep in mind while reading this file is the *mirror*. When work is
 * sent to a subordinate installation, a row is written here that stands for
 * that work over there. It is never moved by anybody on this side — its status
 * changes only when a task_update arrives from the installation actually doing
 * it — and it is what makes a fan-out visible as a tree rather than as a
 * message that left.
 */

package urtuu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/urtuu"
	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/jackc/pgx/v5"
)

// Task is one row as the API returns it.
type Task struct {
	ID string `json:"id"`
	// Number is this installation's own register number — "Д2026-00412". What
	// a person quotes; never what an API takes.
	Number string `json:"number,omitempty"`
	// OriginNumber is the number the sending installation registered it under,
	// cited the way an incoming letter cites the sender's reference. Rendered
	// beside the link's name, which is where the "whose number is this" half
	// of the answer already lives.
	OriginNumber string `json:"origin_number,omitempty"`
	Code         string `json:"code"`
	// Line is which of Өртөө's two promises this task is under — a state
	// service somebody applied for, or an assignment a superior organisation
	// gave. Copied from the code when the task is raised, so that a code being
	// withdrawn later cannot change what a task in flight was.
	Line    string          `json:"line"`
	Title   string          `json:"title"`
	Payload json.RawMessage `json:"payload"`
	// Applicant is who asked, on the service line. Empty on the assignment
	// line, where there is nobody outside the platform waiting.
	Applicant json.RawMessage `json:"applicant,omitempty"`
	// Answer is what is being told back to the applicant. A service task
	// cannot be completed without one — the schema itself refuses it.
	Answer string `json:"answer,omitempty"`
	// Direction is derived rather than stored: "incoming" is work this
	// organisation owes somebody, "outgoing" is work it is owed, and "local" is
	// its own. One field the screens can filter on instead of three nullable
	// ids they would each have to interpret.
	Direction      string     `json:"direction"`
	OriginPeerID   string     `json:"origin_peer_id,omitempty"`
	OriginPeerName string     `json:"origin_peer_name,omitempty"`
	TargetPeerID   string     `json:"target_peer_id,omitempty"`
	TargetPeerName string     `json:"target_peer_name,omitempty"`
	ParentTaskID   string     `json:"parent_task_id,omitempty"`
	OriginChain    []string   `json:"origin_chain"`
	Status         string     `json:"status"`
	Deadline       *time.Time `json:"deadline,omitempty"`
	// Overdue is computed on every read. Storing it would leave a stale mark
	// behind the moment a deadline is edited.
	Overdue        bool            `json:"overdue"`
	AssignedUserID string          `json:"assigned_user_id,omitempty"`
	AssignedName   string          `json:"assigned_name,omitempty"`
	Note           string          `json:"note,omitempty"`
	Evidence       json.RawMessage `json:"evidence,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`

	// originTaskID is this task's id on the installation that sent it, quoted
	// back on every update. Unexported and unserialised: another installation's
	// internal identifier has no business in this one's API, and nothing on a
	// screen could do anything with it. Read it through OriginTaskRef.
	originTaskID string
}

// TaskEvent is one transition.
type TaskEvent struct {
	FromStatus string    `json:"from_status,omitempty"`
	ToStatus   string    `json:"to_status"`
	ActorName  string    `json:"actor_name,omitempty"`
	PeerName   string    `json:"peer_name,omitempty"`
	Note       string    `json:"note,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// taskColumns is the one place the select list lives. Three queries read tasks
// — the list, the detail and the tree — and a column added to one of them and
// not the others is how a screen quietly stops showing something.
const taskColumns = `
	t.id::text, t.number, t.origin_number, t.code, t.line, t.title, t.payload,
	t.applicant, t.answer,
	coalesce(t.origin_peer_id::text, ''), t.origin_task_id,
	coalesce(t.target_peer_id::text, ''),
	coalesce(t.parent_task_id::text, ''), t.origin_chain, t.status, t.deadline,
	coalesce(t.assigned_user_id::text, ''), '',
	t.note, t.evidence, t.created_at, t.updated_at`

// taskFrom no longer joins the channel's peer table. The two names it used to
// fetch per row are filled by namePeers from one read per page — see peers.go.
const taskFrom = `
	FROM urtuu_tasks t`

func scanTask(rows pgx.Rows, now time.Time) (Task, error) {
	var task Task
	if err := rows.Scan(&task.ID, &task.Number, &task.OriginNumber,
		&task.Code, &task.Line, &task.Title, &task.Payload,
		&task.Applicant, &task.Answer,
		&task.OriginPeerID, &task.originTaskID,
		&task.TargetPeerID,
		&task.ParentTaskID, &task.OriginChain, &task.Status, &task.Deadline,
		&task.AssignedUserID, &task.AssignedName, &task.Note, &task.Evidence,
		&task.CreatedAt, &task.UpdatedAt); err != nil {
		return Task{}, err
	}
	task.Direction = domain.DirectionOf(task.OriginPeerID, task.TargetPeerID)
	task.Overdue = contract.Overdue(contract.TaskStatus(task.Status), task.Deadline, now)
	return task, nil
}

// taskFilter is what the two queue screens ask for.
type taskFilter struct {
	Direction string
	Line      string
	Status    string
	Code      string
	Overdue   bool
	ParentID  string
}

func (m *Module) listTasks(ctx context.Context, tenantID string, filter taskFilter) ([]Task, error) {
	// The clauses are built rather than a single query with five OR'd
	// "$n IS NULL" conditions, because that form stops the planner using the
	// partial indexes the migration created for exactly these two screens.
	where := []string{"t.tenant_id = $1"}
	args := []any{tenantID}
	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}

	switch filter.Direction {
	case "incoming":
		where = append(where, "t.origin_peer_id IS NOT NULL")
	case "outgoing":
		where = append(where, "t.target_peer_id IS NOT NULL")
	case "local":
		where = append(where, "t.origin_peer_id IS NULL AND t.target_peer_id IS NULL")
	}
	if filter.Line != "" {
		add("t.line = $%d", filter.Line)
	}
	if filter.Status != "" {
		add("t.status = $%d", filter.Status)
	}
	if filter.Code != "" {
		add("t.code = $%d", filter.Code)
	}
	if filter.ParentID != "" {
		add("t.parent_task_id = $%d", filter.ParentID)
	}
	if filter.Overdue {
		// The same rule as contract.Overdue, in SQL. Two expressions of one
		// rule is a risk, and the alternative — reading every task and
		// filtering in Go — is worse for a screen whose whole job is to be a
		// short list drawn from a long table.
		where = append(where, "t.deadline IS NOT NULL AND t.deadline < NOW() AND t.status <> 'CLOSED'")
	}

	rows, err := m.db.Query(nexus.WithWorkspaceID(ctx, tenantID),
		`SELECT`+taskColumns+taskFrom+` WHERE `+strings.Join(where, " AND ")+
			` ORDER BY t.created_at DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now()
	tasks := make([]Task, 0, 64)
	for rows.Next() {
		task, err := scanTask(rows, now)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	m.nameAssignees(ctx, tenantID, tasks)
	m.namePeers(ctx, tenantID, tasks)
	return tasks, nil
}

// nameAssignees turns the assigned user ids on a page of tasks into names.
//
// The query stopped joining `users`: that is the platform's table, and a module
// reading it is a dependency no compiler sees. One directory read per page
// rather than one join per row — a board of five hundred tasks was five hundred
// joins for an answer that repeats.
func (m *Module) nameAssignees(ctx context.Context, tenantID string, tasks []Task) {
	assigned := false
	for i := range tasks {
		if tasks[i].AssignedUserID != "" {
			assigned = true
			break
		}
	}
	if !assigned {
		return
	}
	people := m.directory(ctx, tenantID)
	for i := range tasks {
		if tasks[i].AssignedUserID != "" {
			tasks[i].AssignedName = people[tasks[i].AssignedUserID].Name
		}
	}
}

func (m *Module) getTask(ctx context.Context, tenantID, id string) (Task, error) {
	rows, err := m.db.Query(nexus.WithWorkspaceID(ctx, tenantID),
		`SELECT`+taskColumns+taskFrom+` WHERE t.tenant_id = $1 AND t.id = $2`, tenantID, id)
	if err != nil {
		return Task{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Task{}, pgx.ErrNoRows
	}
	task, err := scanTask(rows, time.Now())
	if err != nil {
		return Task{}, err
	}
	// nameAssignees fills the slice in place, so the copy is what to return.
	named := []Task{task}
	m.nameAssignees(ctx, tenantID, named)
	m.namePeers(ctx, tenantID, named)
	return named[0], nil
}

func (m *Module) taskEvents(ctx context.Context, tenantID, id string) ([]TaskEvent, error) {
	rows, err := m.db.Query(nexus.WithWorkspaceID(ctx, tenantID), `
		SELECT e.from_status, e.to_status, coalesce(e.actor_user_id::text, ''),
		       coalesce(e.actor_peer_id::text, ''), e.note, e.created_at
		  FROM urtuu_task_events e
		 WHERE e.task_id = $1
		 ORDER BY e.created_at`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]TaskEvent, 0, 16)
	for rows.Next() {
		var event TaskEvent
		if err := rows.Scan(&event.FromStatus, &event.ToStatus, &event.ActorName,
			&event.PeerName, &event.Note, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// ActorName and PeerName hold ids at this point, not names: the query joins
	// neither `users` nor `urtuu_peers`, which belong to the platform and to
	// the channel. Two reads per page turn every id into a name — an event has
	// one actor or the other, never both, so each loop skips what the other
	// filled.
	people := m.directory(ctx, tenantID)
	var peers map[string]string
	for i := range events {
		if events[i].ActorName != "" {
			events[i].ActorName = people[events[i].ActorName].Name
			continue
		}
		if events[i].PeerName == "" {
			continue
		}
		if peers == nil {
			peers = m.peerNames(ctx, tenantID)
		}
		events[i].PeerName = peers[events[i].PeerName]
	}
	return events, nil
}

// ---------------------------------------------------------------- moving one

// move applies one transition and records it, inside one transaction.
//
// The guard is the domain's, which asks the contract's table: the same table is
// what the transport, the app and the migration's CHECK are all held against.
//
// It returns the row as it now stands, so the caller can decide what to report
// upward without a second read.
func (m *Module) move(ctx context.Context, tenantID, id string, to contract.TaskStatus,
	actorUserID, actorPeerID, note string) (Task, error) {

	ctx = nexus.WithWorkspaceID(ctx, tenantID)
	tx, err := m.db.Begin(ctx)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// FOR UPDATE, because two things move a task: a person pressing a button
	// and an envelope arriving from a peer. Without the lock a child's
	// completion and an operator's return can both read RECEIVED and both
	// write, and the loser's event row would describe a transition that never
	// happened.
	var from string
	if err := tx.QueryRow(ctx,
		`SELECT status FROM urtuu_tasks WHERE id = $1 AND tenant_id = $2 FOR UPDATE`,
		id, tenantID).Scan(&from); err != nil {
		return Task{}, err
	}
	if err := domain.CheckTransition(contract.TaskStatus(from), to); err != nil {
		return Task{}, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE urtuu_tasks
		   SET status = $3, note = CASE WHEN $4 = '' THEN note ELSE $4 END, updated_at = NOW()
		 WHERE id = $1 AND tenant_id = $2`, id, tenantID, string(to), note); err != nil {
		return Task{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO urtuu_task_events
		    (tenant_id, task_id, from_status, to_status, actor_user_id, actor_peer_id, note)
		VALUES ($1, $2, $3, $4, NULLIF($5, '')::uuid, NULLIF($6, '')::uuid, $7)`,
		tenantID, id, from, string(to), actorUserID, actorPeerID, note); err != nil {
		return Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, err
	}
	tasksTotal.WithLabelValues(string(to)).Inc()
	return m.getTask(ctx, tenantID, id)
}

// record writes an event with no transition behind it — the birth of a task.
func (m *Module) record(ctx context.Context, tx pgx.Tx, tenantID, taskID, status, actorUserID, actorPeerID, note string) error {
	// Counted with the transitions: a task being raised is a task reaching
	// RECEIVED, and a series that only counted moves would undercount the
	// intake by exactly the tasks that were never moved.
	tasksTotal.WithLabelValues(status).Inc()
	_, err := tx.Exec(ctx, `
		INSERT INTO urtuu_task_events
		    (tenant_id, task_id, from_status, to_status, actor_user_id, actor_peer_id, note)
		VALUES ($1, $2, '', $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, $6)`,
		tenantID, taskID, status, actorUserID, actorPeerID, note)
	return err
}

// ------------------------------------------------------------- the vocabulary

// lookupCode asks the channel what a code means.
//
// It read urtuu_request_codes directly until 2026-08-23 on the argument that
// "the two packages are one product split by layer, sharing one schema" — true
// while both live in one repository and the reason ADR 0004 gave for this app
// staying in it. contract.PeerDirectory is that argument turned into a contract.
//
// What comes back is the domain's RequestCode: what the rules ask of a code —
// its name in every language, its norm, its promise and whether it is in use.
func (m *Module) lookupCode(ctx context.Context, tenantID, code string) (domain.RequestCode, error) {
	found, ok, err := m.peers.RequestCode(ctx, tenantID, code)
	if err != nil {
		return domain.RequestCode{}, err
	}
	if !ok {
		return domain.RequestCode{}, pgx.ErrNoRows
	}
	// The wire record carries the norm as a Duration and the domain wants
	// seconds-or-nothing, because a norm of zero and no norm at all are
	// different facts to a deadline. Zero is what "no norm" looks like on the
	// wire — see wire.RequestCode.DefaultSLA — so this is where the two
	// spellings meet.
	var sla *int64
	if found.DefaultSLA > 0 {
		seconds := int64(found.DefaultSLA.Seconds())
		sla = &seconds
	}
	return domain.RequestCode{
		Code: found.Code, Names: found.Names, SLA: sla,
		Line: found.Line, Active: found.Active, Source: found.Source,
	}, nil
}

// codeOpenOn reports whether a code has been announced on a link. A parent that
// has not opened a code on a link must not be able to send work under it: the
// child would receive a task naming a code it has never been told about, and
// the whole point of announcing the vocabulary is that it does not have to
// guess.
//
// Through the contract rather than urtuu_peer_codes, for the reason above.
func (m *Module) codeOpenOn(ctx context.Context, tenantID, peerID, code string) (bool, error) {
	return m.peers.CodeOpenOn(ctx, tenantID, peerID, code)
}
