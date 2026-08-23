package urtuu

// A task's whole life, across two installations, over the real channel.
//
// The transport's own tests prove an envelope survives the journey. These prove
// what the journey is for: that work raised in one organisation becomes work in
// another, that its answer comes back, that a fan-out completes by itself when
// its branches do, and that a task which finds itself back where it started is
// refused rather than carried round for ever.
//
// Everything goes over HTTP through the real routers, including the handshake.
// Calling the handlers directly would have tested the handlers; what is being
// tested is the arrangement.
//
//	URTUU_TEST_DATABASE_URL=postgres://... go test ./internal/apps/urtuu/...

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	contract "github.com/gerege-systems/open-gerege-nexus/backend/pkg/urtuu"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// openPool connects as the tests' own user.
//
// The core's copy of this file installed internal/platform/dbguard on the pool,
// so every handler under test ran bound to a tenant the way it does in
// production. That package is under internal/ and stayed there, which is the
// right outcome: row-level isolation is the platform's property and the
// platform tests it. What is asserted here is the app's own behaviour, and the
// app names the tenant in every predicate it writes.
func openPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("URTUU_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("neither URTUU_TEST_DATABASE_URL nor DATABASE_URL is set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("database unreachable: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// everyPermission stands in for the RBAC store. What the routes are gated on is
// asserted in internal/apps/access_policy_test.go and in the module's own
// declaration; here the question is whether the work flows, so the caller holds
// everything.
type everyPermission struct{}

func (everyPermission) GetUserPermissions(_ context.Context, _, _ string) (map[string]bool, error) {
	return map[string]bool{"urtuu.read": true, "urtuu.manage": true, "urtuu.process": true}, nil
}

// site is one installation: an organisation, the channel, the app, and an
// address its own screens can be called at.
//
// The other installation is another site in the same process, and the two are
// joined by fakeChannel rather than by HTTP — see channel_fake_test.go.
type site struct {
	link     *fakeChannel
	mod      *Module
	server   *httptest.Server
	tenantID string
	userID   string
	t        *testing.T
}

func newSite(t *testing.T, pool *pgxpool.Pool, name string) *site {
	t.Helper()

	tenantID := uuid.NewString()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO tenants (id, name, slug) VALUES ($1, $2, $3)`,
		tenantID, "Өртөө test "+name, strings.ToLower(name)+"-"+tenantID[:8]); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	// A real person, because created_by is a foreign key and because a
	// permission check refuses a request with no caller at all — which is what
	// it should do.
	userID := uuid.NewString()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO users (id, email, password_hash, name) VALUES ($1, $2, '', $3)`,
		userID, userID+"@urtuu.test", "Өртөө "+name); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id) VALUES ($1, $2)`,
		tenantID, userID); err != nil {
		t.Fatalf("create membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	link := &fakeChannel{t: t, installationID: uuid.NewString(), name: name, tenantID: tenantID}
	module := New(nexus.NewPlatform(pool, everyPermission{}), link, link)

	// The session middleware, in miniature: everything below it acts for this
	// organisation as one of its members.
	asMember := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := nexus.WithTenantID(r.Context(), tenantID)
			ctx = nexus.WithUser(ctx, nexus.UserClaims{UserID: userID, TenantID: tenantID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	router := chi.NewRouter()
	module.RegisterRoutes(router, asMember)

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &site{link: link, mod: module, server: server, tenantID: tenantID, userID: userID, t: t}
}

// call makes one request to this installation's own API and decodes the answer.
func (s *site) call(method, path string, body any, want int) map[string]any {
	s.t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			s.t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, s.server.URL+path, reader)
	if err != nil {
		s.t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := s.server.Client().Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		s.t.Fatalf("%s %s = %d (want %d): %s", method, path, res.StatusCode, want, raw)
	}
	if len(raw) == 0 {
		return nil
	}
	var answer map[string]any
	if err := json.Unmarshal(raw, &answer); err != nil {
		s.t.Fatalf("decode %s: %v", raw, err)
	}
	return answer
}

// tasks reads this installation's queue in one direction.
func (s *site) tasks(direction string) []Task {
	s.t.Helper()
	answer := s.call(http.MethodGet, "/api/v1/urtuu/tasks?direction="+direction, nil, http.StatusOK)
	encoded, err := json.Marshal(answer["tasks"])
	if err != nil {
		s.t.Fatalf("marshal: %v", err)
	}
	var tasks []Task
	if err := json.Unmarshal(encoded, &tasks); err != nil {
		s.t.Fatalf("decode tasks: %v", err)
	}
	return tasks
}

func (s *site) task(id string) Task {
	s.t.Helper()
	answer := s.call(http.MethodGet, "/api/v1/urtuu/tasks/"+id, nil, http.StatusOK)
	encoded, err := json.Marshal(answer["task"])
	if err != nil {
		s.t.Fatalf("marshal: %v", err)
	}
	var task Task
	if err := json.Unmarshal(encoded, &task); err != nil {
		s.t.Fatalf("decode task: %v", err)
	}
	return task
}

// linked joins two installations and opens one code on the link.
//
// The real thing is an invitation, a redemption over HTTP, a confirmation and a
// code announcement — four requests to the channel's own routes, none of them
// this app's. Here it is two rows and a map entry: what the app needs to be
// true is that a peer exists on both sides and that the code is open on the
// link, which is exactly what it asks the directory.
func linked(t *testing.T, pool *pgxpool.Pool, code string) (*site, *site, string) {
	t.Helper()
	parent := newSite(t, pool, "parent")
	child := newSite(t, pool, "child")

	parent.link.other, child.link.other = child.link, parent.link
	// Each side's row for the other, in the channel's own table: a task's
	// origin_peer_id and target_peer_id are foreign keys into it.
	parent.link.peerID = peerRow(t, pool, parent.tenantID, "Ховд аймаг")
	child.link.peerID = peerRow(t, pool, child.tenantID, "Боловсролын яам")

	parent.link.openCode(code, string(contract.LineAssignment), 7*24*3600)
	child.link.openCode(code, string(contract.LineAssignment), 7*24*3600)

	return parent, child, parent.link.peerID
}

// carry moves everything queued in both directions and lets both sides read it.
//
// Twice, because work sent down produces an answer coming back and a test that
// pumped once would be asserting half a round trip.
func carry(t *testing.T, parent, child *site) {
	t.Helper()
	for range 2 {
		child.link.drain()
		parent.link.drain()
	}
}

// refusalTo reads the update the child sent back, at the parent's end of the
// journey rather than the near one: a refusal that was queued and never
// arrived is not a refusal anybody acted on.
func refusalTo(t *testing.T, parent *site) string {
	t.Helper()
	messages := parent.link.received(contract.KindTaskUpdate)
	if len(messages) == 0 {
		t.Fatal("nothing came back to the parent")
	}
	payload := string(messages[0].Payload)
	if !strings.Contains(payload, string(contract.StatusReturned)) {
		t.Errorf("what came back is not a refusal: %s", payload)
	}
	return payload
}

// The journey the whole system exists for.
func TestWorkGoesDownAndTheAnswerComesBack(t *testing.T) {
	pool := openPool(t)
	parent, child, parentPeerID := linked(t, pool, "local.count")

	created := parent.call(http.MethodPost, "/api/v1/urtuu/tasks", map[string]any{
		"code":     "local.count",
		"payload":  map[string]string{"period": "2026 H1"},
		"peer_ids": []string{parentPeerID},
	}, http.StatusCreated)
	rootID := created["id"].(string)

	// The root is delegated from birth and has one branch: the proposal's rule
	// is a separate task per subordinate, joined by parent_task_id.
	if got := parent.task(rootID).Status; got != string(contract.StatusDelegated) {
		t.Errorf("the raised task is %q, want DELEGATED", got)
	}
	sent := parent.tasks("outgoing")
	if len(sent) != 1 {
		t.Fatalf("the parent has %d branches, want 1", len(sent))
	}
	if sent[0].ParentTaskID != rootID {
		t.Error("the branch is not joined to the task it came from")
	}

	carry(t, parent, child)

	arrived := child.tasks("incoming")
	if len(arrived) != 1 {
		t.Fatalf("the child holds %d tasks, want 1", len(arrived))
	}
	work := arrived[0]
	if work.Status != string(contract.StatusReceived) {
		t.Errorf("the arrived task is %q, want RECEIVED", work.Status)
	}
	if work.Code != "local.count" {
		t.Errorf("code = %q", work.Code)
	}
	// The code's own norm, applied at the receiving end from the sender's
	// stamp. Without it a task would arrive with no deadline at all.
	if work.Deadline == nil {
		t.Error("the task arrived with no deadline; the code names a seven-day norm")
	}
	// The chain now names both installations, which is what makes the cycle
	// guard able to say anything.
	if len(work.OriginChain) != 2 {
		t.Errorf("origin chain = %v, want both installations", work.OriginChain)
	}

	// Accepting is a commitment, and it travels.
	child.call(http.MethodPost, "/api/v1/urtuu/tasks/"+work.ID+"/accept",
		map[string]string{"note": "хүлээн авлаа"}, http.StatusOK)
	carry(t, parent, child)
	if got := parent.tasks("outgoing")[0].Status; got != string(contract.StatusAccepted) {
		t.Errorf("the parent's mirror is %q, want ACCEPTED", got)
	}

	// And so does finishing it — which finishes the whole delegated task above,
	// with nobody having to notice.
	child.call(http.MethodPost, "/api/v1/urtuu/tasks/"+work.ID+"/complete",
		map[string]string{"note": "дууслаа"}, http.StatusOK)
	carry(t, parent, child)

	if got := parent.tasks("outgoing")[0].Status; got != string(contract.StatusCompleted) {
		t.Errorf("the parent's mirror is %q, want COMPLETED", got)
	}
	if got := parent.task(rootID).Status; got != string(contract.StatusCompleted) {
		t.Errorf("the delegated task is %q, want COMPLETED — every branch is done", got)
	}

	// The history is the point of the whole thing: who moved it, when, and why.
	detail := parent.call(http.MethodGet, "/api/v1/urtuu/tasks/"+rootID, nil, http.StatusOK)
	events, _ := detail["events"].([]any)
	if len(events) < 2 {
		t.Errorf("the task's history has %d entries; a delegated task that completed has at least two", len(events))
	}

	// And the originator closes it, which is the one move only this side makes.
	parent.call(http.MethodPost, "/api/v1/urtuu/tasks/"+rootID+"/close", nil, http.StatusOK)
	if got := parent.task(rootID).Status; got != string(contract.StatusClosed) {
		t.Errorf("the task is %q after being closed", got)
	}
}

// The board is the app's front page and the one screen an operator opens in the
// morning, so what it counts has to be what is actually there.
func TestTheBoardCountsTheTreeAndTheChannel(t *testing.T) {
	pool := openPool(t)
	parent, child, parentPeerID := linked(t, pool, "local.count")

	parent.call(http.MethodPost, "/api/v1/urtuu/tasks", map[string]any{
		"code": "local.count", "peer_ids": []string{parentPeerID},
	}, http.StatusCreated)
	carry(t, parent, child)

	board := parent.call(http.MethodGet, "/api/v1/urtuu/tasks/board", nil, http.StatusOK)

	trees, _ := board["trees"].([]any)
	if len(trees) != 1 {
		t.Fatalf("the board shows %d delegated tasks, want 1", len(trees))
	}
	tree, _ := trees[0].(map[string]any)
	if tree["total"] != float64(1) || tree["done"] != float64(0) {
		t.Errorf("branch progress = %v/%v, want 0/1", tree["done"], tree["total"])
	}

	// The channel underneath. A queue that has stopped moving is usually a link
	// that has stopped talking, and the board is where that has to show.
	links, _ := board["links"].([]any)
	if len(links) != 1 {
		t.Fatalf("the board shows %d links, want 1", len(links))
	}
	if seen, _ := links[0].(map[string]any)["last_seen_at"].(string); seen == "" {
		t.Error("the link has spoken but the board does not say when")
	}

	// And once the branch finishes, the tree does.
	work := child.tasks("incoming")[0]
	child.call(http.MethodPost, "/api/v1/urtuu/tasks/"+work.ID+"/accept", nil, http.StatusOK)
	child.call(http.MethodPost, "/api/v1/urtuu/tasks/"+work.ID+"/complete", nil, http.StatusOK)
	carry(t, parent, child)

	after := parent.call(http.MethodGet, "/api/v1/urtuu/tasks/board", nil, http.StatusOK)
	if remaining, _ := after["trees"].([]any); len(remaining) != 0 {
		t.Errorf("a fully completed tree is still listed as delegated: %v", remaining)
	}
}

// A refusal has to travel as clearly as an acceptance, reason included.
func TestAReturnedTaskCarriesItsReasonBack(t *testing.T) {
	pool := openPool(t)
	parent, child, parentPeerID := linked(t, pool, "local.count")

	parent.call(http.MethodPost, "/api/v1/urtuu/tasks", map[string]any{
		"code": "local.count", "peer_ids": []string{parentPeerID},
	}, http.StatusCreated)
	carry(t, parent, child)

	work := child.tasks("incoming")[0]
	// A refusal with no reason turns a channel into a telephone call.
	child.call(http.MethodPost, "/api/v1/urtuu/tasks/"+work.ID+"/return",
		map[string]string{}, http.StatusBadRequest)
	child.call(http.MethodPost, "/api/v1/urtuu/tasks/"+work.ID+"/return",
		map[string]string{"note": "энэ ажил манай харьяалалд биш"}, http.StatusOK)
	carry(t, parent, child)

	mirror := parent.tasks("outgoing")[0]
	if mirror.Status != string(contract.StatusReturned) {
		t.Errorf("the parent's mirror is %q, want RETURNED", mirror.Status)
	}
	if !strings.Contains(mirror.Note, "харьяалалд") {
		t.Errorf("the reason did not travel: %q", mirror.Note)
	}
	// A returned branch must not complete the task above it. Somebody has to
	// read the reason and decide, which is the whole difference between a
	// refusal and a delay.
	root := parent.task(mirror.ParentTaskID)
	if root.Status == string(contract.StatusCompleted) {
		t.Error("a returned branch completed the task it came from")
	}
}

// The cycle guard (§9). A→Б→А is a legitimate shape for a peer graph — two
// ministries can each be above the other for different kinds of work — so it is
// the task that must not go round, not the link that must not exist.
func TestATaskThatComesBackToItselfIsRefused(t *testing.T) {
	pool := openPool(t)
	parent, child, parentPeerID := linked(t, pool, "local.count")

	// An assignment whose chain already names the child. That is exactly what a
	// task would look like arriving on the third hop of A→Б→А, and building it
	// here rather than standing up three installations keeps the test about the
	// guard instead of about the topology.
	sent, err := parent.link.Enqueue(context.Background(), parent.tenantID,
		contract.KindTaskAssigned, assignment{
			TaskID:      uuid.NewString(),
			Code:        "local.count",
			Title:       "Эргэлдсэн даалгавар",
			Payload:     json.RawMessage(`{}`),
			OriginChain: []string{"some-other-installation", child.link.InstallationID()},
		}, parentPeerID)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if sent == "" {
		t.Fatal("nothing was queued")
	}

	carry(t, parent, child)

	if got := len(child.tasks("incoming")); got != 0 {
		t.Fatalf("the child created %d tasks from work that had already been through it", got)
	}

	// Refused, not dropped. The parent has to learn why, or the work simply
	// stops with nobody able to say where — so the refusal is asserted where it
	// actually lands, in the parent's inbox.
	if !strings.Contains(refusalTo(t, parent), "origin chain") {
		t.Error("the refusal that reached the parent does not name the cycle")
	}
}

// A code the receiving installation has never been told about is refused for
// the same reason and by the same path — which is what makes announcing the
// vocabulary worth doing.
func TestATaskUnderAnUnknownCodeIsRefused(t *testing.T) {
	pool := openPool(t)
	parent, child, parentPeerID := linked(t, pool, "local.count")

	// Sent before the vocabulary reaches the child: the announcement is queued
	// but nothing has carried it yet.
	if _, err := parent.link.Enqueue(context.Background(), parent.tenantID,
		contract.KindTaskAssigned, assignment{
			TaskID: uuid.NewString(), Code: "local.unknown", Title: "Танихгүй код",
			Payload: json.RawMessage(`{}`), OriginChain: []string{parent.link.InstallationID()},
		}, parentPeerID); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	carry(t, parent, child)

	if got := len(child.tasks("incoming")); got != 0 {
		t.Fatalf("the child created %d tasks under a code it does not have", got)
	}
	if !strings.Contains(refusalTo(t, parent), "local.unknown") {
		t.Error("the refusal that reached the parent does not name the code")
	}
}

// Sending work under a code that was never opened on that link is refused here,
// before anything leaves — the child would otherwise be asked for something it
// has no definition of.
func TestWorkCannotBeSentUnderACodeTheLinkWasNeverGiven(t *testing.T) {
	pool := openPool(t)
	parent, _, parentPeerID := linked(t, pool, "local.count")

	// Known here, never announced on the link. The channel's own routes define
	// a code; this app only ever asks whether one is open.
	parent.link.defineLocally("local.private", string(contract.LineAssignment))

	parent.call(http.MethodPost, "/api/v1/urtuu/tasks", map[string]any{
		"code": "local.private", "peer_ids": []string{parentPeerID},
	}, http.StatusBadRequest)

	// And nothing half-happened: the failed fan-out rolled the whole thing
	// back, root included.
	if got := len(parent.tasks("local")); got != 0 {
		t.Errorf("a refused fan-out left %d tasks behind", got)
	}
}

// Work an organisation raises for itself is a real case — it is how a task gets
// counted and timed like any other — and it needs no channel at all.
func TestATaskCanBeRaisedAndKeptHere(t *testing.T) {
	pool := openPool(t)
	parent, _, _ := linked(t, pool, "local.count")

	created := parent.call(http.MethodPost, "/api/v1/urtuu/tasks", map[string]any{
		"code": "local.count",
	}, http.StatusCreated)
	id := created["id"].(string)

	if got := parent.task(id).Status; got != string(contract.StatusReceived) {
		t.Errorf("a locally raised task is %q, want RECEIVED", got)
	}
	// The state machine still applies, and skipping a step is refused with a
	// 409 rather than quietly allowed.
	parent.call(http.MethodPost, "/api/v1/urtuu/tasks/"+id+"/close", nil, http.StatusConflict)
	parent.call(http.MethodPost, "/api/v1/urtuu/tasks/"+id+"/accept", nil, http.StatusOK)
	parent.call(http.MethodPost, "/api/v1/urtuu/tasks/"+id+"/complete", nil, http.StatusOK)
	parent.call(http.MethodPost, "/api/v1/urtuu/tasks/"+id+"/close", nil, http.StatusOK)
}

// ---------------------------------------------------------------- reports

// poolQuerier is the read surface the engine hands a report, backed by the
// pool. The engine's own is a transaction with a statement timeout on it; what
// is under test here is the three queries, not the engine.
type poolQuerier struct{ pool *pgxpool.Pool }

func (q poolQuerier) Query(ctx context.Context, sql string, args ...any) (nexus.Rows, error) {
	return q.pool.Query(ctx, sql, args...)
}

// The three reports run against a real schema, over real tasks.
//
// A report is SQL somebody else's engine executes, so the failure it has is a
// column that does not exist or a scan that does not match — neither of which
// the compiler sees. This is the check that does.
func TestTheThreeReportsRun(t *testing.T) {
	pool := openPool(t)
	parent, child, parentPeerID := linked(t, pool, "local.count")

	parent.call(http.MethodPost, "/api/v1/urtuu/tasks", map[string]any{
		"code": "local.count", "peer_ids": []string{parentPeerID},
	}, http.StatusCreated)
	carry(t, parent, child)
	work := child.tasks("incoming")[0]
	child.call(http.MethodPost, "/api/v1/urtuu/tasks/"+work.ID+"/accept", nil, http.StatusOK)
	child.call(http.MethodPost, "/api/v1/urtuu/tasks/"+work.ID+"/complete", nil, http.StatusOK)
	carry(t, parent, child)

	ctx := nexus.WithTenantID(context.Background(), parent.tenantID)
	params := nexus.NewParams(map[string]any{
		"period_from": time.Now().Add(-24 * time.Hour),
		"period_to":   time.Now().Add(24 * time.Hour),
	}, "mn")

	// Two of the three have to have found something: a task was raised,
	// completed and carried over a link inside the window. Only the SLA report
	// is legitimately empty — nothing here was late.
	expectRows := map[string]bool{
		taskCompletion{}.Key(): true,
		channelLoad{}.Key():    true,
	}

	// The reports are built with the channel's directory, the same one the
	// module hands them: two of the three name peers with it and the third
	// reads the outbox through it, so a nil one would be testing the empty
	// answer rather than the report.
	peers := nexus.PeerDirectory(parent.link)
	for _, report := range []nexus.Report{taskCompletion{peers}, slaBreaches{peers}, channelLoad{peers}} {
		t.Run(report.Key(), func(t *testing.T) {
			result, err := report.Run(ctx, poolQuerier{pool}, params)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if expectRows[report.Key()] && len(result.Rows) == 0 {
				t.Fatal("the report found nothing, and there is work in the window")
			}
			// Every declared column has to appear on every row, or the export
			// writes a blank cell under a header somebody is reading.
			for _, row := range result.Rows {
				for _, column := range report.Columns() {
					if _, ok := row[column.Key]; !ok {
						t.Errorf("row is missing the column %q", column.Key)
					}
				}
			}
		})
	}

	// Sharing is opt-in per scope. A report that cannot filter by counterparty
	// must not offer to — a grant asking for it would otherwise quietly become
	// a view of every subordinate.
	// Asked of the report rather than of the platform's reporting package. The
	// opt-in is a method the report declares, so a module outside this
	// repository can make it — and this test can check it — without reaching
	// into internal/platform. See docs/adr/0004-a-pilot-that-did-not-ship.md.
	for _, report := range []nexus.Report{taskCompletion{}, slaBreaches{}, channelLoad{}} {
		scopes := report.(interface{ Scopes() []string }).Scopes()
		if !contains(scopes, nexus.ReportScopeFull) {
			t.Errorf("%s cannot be shared at all", report.Key())
		}
		if contains(scopes, nexus.ReportScopeCounterparty) {
			t.Errorf("%s offers counterparty scope, and nothing in a task carries a counterparty reference",
				report.Key())
		}
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- evidence

// oneDocument is a document store in the smallest form that answers the
// contract. The real one is the documents app; what is under test here is what
// Өртөө does with what it is handed, not how a signature ceremony works.
type oneDocument struct {
	id         string
	title      string
	signatures int
}

func (d *oneDocument) File(_ context.Context, _ string, draft nexus.DocumentDraft) (nexus.FiledDocument, error) {
	d.id, d.title = uuid.NewString(), draft.Title
	return d.filed(), nil
}

func (d *oneDocument) Document(_ context.Context, _, id string) (nexus.FiledDocument, error) {
	if id != d.id {
		return nexus.FiledDocument{}, errors.New("no such document")
	}
	return d.filed(), nil
}

func (d *oneDocument) filed() nexus.FiledDocument {
	return nexus.FiledDocument{
		ID: d.id, Title: d.title, Type: "APPROVAL",
		SignatureCount: d.signatures, RequiredSignatures: 2,
	}
}

// A task can be accompanied by an official document, and what crosses the link
// is a reference to it — never the document.
func TestATaskCarriesAReferenceToItsOrderAndNotTheOrder(t *testing.T) {
	pool := openPool(t)
	parent, child, parentPeerID := linked(t, pool, "local.count")

	store := &oneDocument{}
	nexus.Provide[nexus.DocumentFiler](store)
	t.Cleanup(func() { nexus.Provide[nexus.DocumentFiler](nil) })

	parent.call(http.MethodPost, "/api/v1/urtuu/tasks", map[string]any{
		"code":     "local.count",
		"peer_ids": []string{parentPeerID},
		"document": map[string]string{"title": "Тооллого явуулах тухай тушаал", "type": "APPROVAL"},
	}, http.StatusCreated)
	if store.id == "" {
		t.Fatal("no document was filed")
	}

	carry(t, parent, child)

	detail := child.call(http.MethodGet, "/api/v1/urtuu/tasks/"+child.tasks("incoming")[0].ID, nil, http.StatusOK)
	evidence, _ := detail["evidence"].([]any)
	if len(evidence) != 1 {
		t.Fatalf("the child sees %d attachments, want 1", len(evidence))
	}
	reference, _ := evidence[0].(map[string]any)
	if reference["ref"] != store.id {
		t.Errorf("ref = %v, want the sender's document id", reference["ref"])
	}
	// Whose id it is. Without this the child has an identifier it could look up
	// in its own database and find a different document under.
	if reference["installation"] != parent.link.InstallationID() {
		t.Errorf("installation = %v, want the sender's", reference["installation"])
	}
	if reference["title"] != "Тооллого явуулах тухай тушаал" {
		t.Errorf("title = %v", reference["title"])
	}
	if reference["signed"] != false {
		t.Error("an unsigned order arrived marked as signed")
	}

	// The signature count on the child's copy is a snapshot of what it was
	// told. Signing at the sender does not silently rewrite it — but the
	// sender's own screen reads the current state.
	store.signatures = 2
	senderDetail := parent.call(http.MethodGet,
		"/api/v1/urtuu/tasks/"+parent.tasks("outgoing")[0].ParentTaskID, nil, http.StatusOK)
	senderEvidence, _ := senderDetail["evidence"].([]any)
	if len(senderEvidence) != 1 {
		t.Fatalf("the sender sees %d attachments, want 1", len(senderEvidence))
	}
	if signed, _ := senderEvidence[0].(map[string]any)["signed"].(bool); !signed {
		t.Error("the sender's screen does not show the order as signed once it has been")
	}
}

// The completion report is the other direction: a child files what it did,
// signs it, and the reference travels up with the update.
func TestACompletionCanCarryItsOwnSignedReport(t *testing.T) {
	pool := openPool(t)
	parent, child, parentPeerID := linked(t, pool, "local.count")

	store := &oneDocument{signatures: 2}
	nexus.Provide[nexus.DocumentFiler](store)
	t.Cleanup(func() { nexus.Provide[nexus.DocumentFiler](nil) })

	parent.call(http.MethodPost, "/api/v1/urtuu/tasks", map[string]any{
		"code": "local.count", "peer_ids": []string{parentPeerID},
	}, http.StatusCreated)
	carry(t, parent, child)

	work := child.tasks("incoming")[0]
	child.call(http.MethodPost, "/api/v1/urtuu/tasks/"+work.ID+"/accept", nil, http.StatusOK)
	child.call(http.MethodPost, "/api/v1/urtuu/tasks/"+work.ID+"/complete", map[string]any{
		"note":     "дууслаа",
		"document": map[string]string{"title": "Тооллогын тайлан", "type": "APPROVAL"},
	}, http.StatusOK)
	carry(t, parent, child)

	mirror := parent.tasks("outgoing")[0]
	detail := parent.call(http.MethodGet, "/api/v1/urtuu/tasks/"+mirror.ID, nil, http.StatusOK)
	evidence, _ := detail["evidence"].([]any)
	if len(evidence) != 1 {
		t.Fatalf("the parent sees %d attachments on the completed branch, want 1", len(evidence))
	}
	reference, _ := evidence[0].(map[string]any)
	if reference["installation"] != child.link.InstallationID() {
		t.Errorf("installation = %v, want the child's — the report is filed there", reference["installation"])
	}
	if signed, _ := reference["signed"].(bool); !signed {
		t.Error("the signed report did not arrive as signed")
	}
}

// A deployment with no document store must refuse the attachment rather than
// dropping it: a task saying "see the attached order" with no order is worse
// than one that was refused at the point of raising it.
func TestAnAttachmentIsRefusedWithoutADocumentStore(t *testing.T) {
	pool := openPool(t)
	parent, _, parentPeerID := linked(t, pool, "local.count")
	nexus.Provide[nexus.DocumentFiler](nil)

	parent.call(http.MethodPost, "/api/v1/urtuu/tasks", map[string]any{
		"code": "local.count", "peer_ids": []string{parentPeerID},
		"document": map[string]string{"title": "Тушаал"},
	}, http.StatusBadRequest)

	if got := len(parent.tasks("local")); got != 0 {
		t.Errorf("a refused attachment left %d tasks behind", got)
	}
}

// ------------------------------------------------------------- two lines

// serviceLine sets up a pair whose vocabulary carries a state-service code.
func serviceLine(t *testing.T, pool *pgxpool.Pool) (*site, *site, string) {
	t.Helper()
	parent, child, parentPeerID := linked(t, pool, "local.count")

	// A second code, on the other line, open on the link at both ends. In
	// production this is two requests to the channel's routes and a code sync
	// over the wire; none of it is this app's.
	parent.link.openCode("local.certificate", string(contract.LineService), 7*24*3600)
	child.link.openCode("local.certificate", string(contract.LineService), 7*24*3600)

	return parent, child, parentPeerID
}

// The service line's whole promise: somebody outside the platform asked, and
// an answer has to come back to them.
func TestAServiceRequestCarriesItsApplicantAndDemandsAnAnswer(t *testing.T) {
	pool := openPool(t)
	parent, child, parentPeerID := serviceLine(t, pool)

	// A request from nobody is a request nobody can answer.
	parent.call(http.MethodPost, "/api/v1/urtuu/tasks", map[string]any{
		"code": "local.certificate", "peer_ids": []string{parentPeerID},
	}, http.StatusBadRequest)

	parent.call(http.MethodPost, "/api/v1/urtuu/tasks", map[string]any{
		"code":     "local.certificate",
		"peer_ids": []string{parentPeerID},
		"applicant": map[string]string{
			"kind": "citizen", "name": "Дорж", "registry_number": "УБ12345678", "contact": "99001122",
		},
	}, http.StatusCreated)
	carry(t, parent, child)

	work := child.tasks("incoming")[0]
	if work.Line != contract.LineService {
		t.Fatalf("the arrived task is on the %q line, want %q", work.Line, contract.LineService)
	}
	// The applicant travelled: the office that has to issue a certificate
	// cannot issue it to nobody.
	var applicant contract.Applicant
	if err := json.Unmarshal(work.Applicant, &applicant); err != nil {
		t.Fatalf("decode applicant: %v", err)
	}
	if applicant.Name != "Дорж" || applicant.RegistryNumber != "УБ12345678" {
		t.Errorf("applicant = %+v, want the person who asked", applicant)
	}

	child.call(http.MethodPost, "/api/v1/urtuu/tasks/"+work.ID+"/accept", nil, http.StatusOK)

	// The line's promise, refused at the door rather than at the constraint.
	child.call(http.MethodPost, "/api/v1/urtuu/tasks/"+work.ID+"/complete",
		map[string]string{"note": "болсон"}, http.StatusBadRequest)

	child.call(http.MethodPost, "/api/v1/urtuu/tasks/"+work.ID+"/complete", map[string]string{
		"answer": "Тодорхойлолт олгов, дугаар 2026/114",
	}, http.StatusOK)
	carry(t, parent, child)

	// And the answer reached the installation the person applied to. Without
	// this the request was fulfilled somewhere they will never see.
	mirror := parent.tasks("outgoing")[0]
	if mirror.Status != string(contract.StatusCompleted) {
		t.Fatalf("the parent's mirror is %q, want COMPLETED", mirror.Status)
	}
	if !strings.Contains(mirror.Answer, "2026/114") {
		t.Errorf("the answer did not come back: %q", mirror.Answer)
	}
	root := parent.task(mirror.ParentTaskID)
	if !strings.Contains(root.Answer, "2026/114") {
		t.Errorf("the request the citizen made shows no answer: %q", root.Answer)
	}
}

// The two lines are two promises, and neither may be worn by the other.
func TestTheLinesDoNotBorrowEachOthersPromises(t *testing.T) {
	pool := openPool(t)
	parent, _, parentPeerID := serviceLine(t, pool)

	// An assignment has no applicant: attaching one would invent a citizen
	// behind a ministry's internal instruction.
	parent.call(http.MethodPost, "/api/v1/urtuu/tasks", map[string]any{
		"code": "local.count", "peer_ids": []string{parentPeerID},
		"applicant": map[string]string{"kind": "citizen", "name": "Дорж"},
	}, http.StatusBadRequest)

	// And an assignment completes with nothing to tell anybody, because there
	// is nobody outside the platform waiting.
	created := parent.call(http.MethodPost, "/api/v1/urtuu/tasks",
		map[string]any{"code": "local.count"}, http.StatusCreated)
	id := created["id"].(string)
	if got := parent.task(id).Line; got != contract.LineAssignment {
		t.Errorf("a locally authored code produced the %q line, want %q", got, contract.LineAssignment)
	}
	parent.call(http.MethodPost, "/api/v1/urtuu/tasks/"+id+"/accept", nil, http.StatusOK)
	parent.call(http.MethodPost, "/api/v1/urtuu/tasks/"+id+"/complete", nil, http.StatusOK)

	// The two queues are separately readable, which is what "two lines" means
	// on every screen.
	answer := parent.call(http.MethodGet,
		"/api/v1/urtuu/tasks?line="+contract.LineService, nil, http.StatusOK)
	tasks, _ := answer["tasks"].([]any)
	for _, item := range tasks {
		if line, _ := item.(map[string]any)["line"].(string); line != contract.LineService {
			t.Errorf("the service queue returned a %q task", line)
		}
	}
	parent.call(http.MethodGet, "/api/v1/urtuu/tasks?line=invented", nil, http.StatusBadRequest)
}

// The schema is the last line of defence, and it has to hold when the handler
// is gone round entirely.
func TestTheDatabaseItselfRefusesAnUnansweredService(t *testing.T) {
	pool := openPool(t)
	parent, _, _ := serviceLine(t, pool)

	created := parent.call(http.MethodPost, "/api/v1/urtuu/tasks", map[string]any{
		"code":      "local.certificate",
		"applicant": map[string]string{"kind": "citizen", "name": "Дорж"},
	}, http.StatusCreated)
	id := created["id"].(string)

	_, err := pool.Exec(nexus.WithTenantID(context.Background(), parent.tenantID),
		`UPDATE urtuu_tasks SET status = 'COMPLETED' WHERE id = $1`, id)
	if err == nil {
		t.Fatal("a service request was marked complete with no answer, straight in SQL")
	}
	if !strings.Contains(err.Error(), "urtuu_tasks_service_has_answer") {
		t.Errorf("refused, but not by the constraint that exists for this: %v", err)
	}
}

// ------------------------------------------------------- register numbers

// The number is what a person quotes down a telephone, so it has to be
// allocated per installation, per line and per year — and each side has to
// register incoming work under its own number while citing the sender's.
func TestEverySideRegistersItsOwnNumberAndCitesTheSenders(t *testing.T) {
	pool := openPool(t)
	parent, child, parentPeerID := serviceLine(t, pool)

	created := parent.call(http.MethodPost, "/api/v1/urtuu/tasks", map[string]any{
		"code": "local.count", "peer_ids": []string{parentPeerID},
	}, http.StatusCreated)
	rootNumber, _ := created["number"].(string)

	year := time.Now().Year()
	// Д for an assignment, the year, and a sequence. The first task this
	// organisation raised this year is number one.
	if want := fmt.Sprintf("Д%d-00001", year); rootNumber != want {
		t.Fatalf("the raised task is %q, want %q", rootNumber, want)
	}
	// The dispatch is registered too, the way outgoing mail is.
	dispatch := parent.tasks("outgoing")[0]
	if dispatch.Number != fmt.Sprintf("Д%d-00002", year) {
		t.Errorf("the dispatch is %q; each one is registered in turn", dispatch.Number)
	}

	carry(t, parent, child)

	work := child.tasks("incoming")[0]
	// The child's own register starts at one: two registers, two numbers.
	if want := fmt.Sprintf("Д%d-00001", year); work.Number != want {
		t.Errorf("the child registered it as %q, want %q", work.Number, want)
	}
	// And it cites what arrived, the way an incoming letter cites the sender's
	// reference. The "whose number is this" half is the link's own name.
	if work.OriginNumber != dispatch.Number {
		t.Errorf("origin number = %q, want the sender's %q", work.OriginNumber, dispatch.Number)
	}
	if work.OriginPeerName == "" {
		t.Error("the cited number names no installation, so it cannot be read")
	}
}

// The two lines are numbered apart: the first letter is the promise, and a
// service request and an assignment raised on the same day are both number one.
func TestTheTwoLinesAreNumberedApart(t *testing.T) {
	pool := openPool(t)
	parent, _, _ := serviceLine(t, pool)
	year := time.Now().Year()

	assignment := parent.call(http.MethodPost, "/api/v1/urtuu/tasks",
		map[string]any{"code": "local.count"}, http.StatusCreated)
	service := parent.call(http.MethodPost, "/api/v1/urtuu/tasks", map[string]any{
		"code":      "local.certificate",
		"applicant": map[string]string{"kind": "citizen", "name": "Дорж"},
	}, http.StatusCreated)

	if got := assignment["number"]; got != fmt.Sprintf("Д%d-00001", year) {
		t.Errorf("assignment number = %v", got)
	}
	if got := service["number"]; got != fmt.Sprintf("Ү%d-00001", year) {
		t.Errorf("service number = %v", got)
	}
}
