package channel

// The request-code vocabulary, and how a parent's half of it reaches a child.
//
// The transport tests prove an envelope survives the journey. These prove what
// the first envelope kind actually means: that a parent decides per link which
// codes may be used, that a child stores exactly what it was last told, and
// that the namespace rule keeps a locally invented code from ever being
// mistaken for one of the state's.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/jackc/pgx/v5/pgxpool"
)

// linkedPair returns two installations with a confirmed link between them, plus
// the two ids for it.
func linkedPair(t *testing.T, pool *pgxpool.Pool, seed byte) (*installation, *installation, string, string) {
	t.Helper()
	parent := newInstallation(t, pool, "parent", seed)
	child := newInstallation(t, pool, "child", seed+100)
	parentPeerID, childPeerID := handshake(t, parent, child)
	if rec := parent.adminCall(t, parent.svc.handleConfirm, nil, parentPeerID); rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", rec.Code, rec.Body)
	}
	return parent, child, parentPeerID, childPeerID
}

// carry moves whatever is queued and lets the receiving side read it.
func carry(t *testing.T, parent, child *installation, childPeerID string) {
	t.Helper()
	link := child.childLink(t, childPeerID)
	if err := child.svc.exchangeOnce(context.Background(), link, 0); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	child.svc.ProcessInbox(context.Background())
	// A second round so the acknowledgement lands and the parent's own queue
	// stops offering what has already been read.
	if err := child.svc.exchangeOnce(context.Background(), link, 0); err != nil {
		t.Fatalf("second exchange: %v", err)
	}
	parent.svc.ProcessInbox(context.Background())
}

func (i *installation) codes(t *testing.T) map[string]Code {
	t.Helper()
	list, err := i.svc.listCodes(context.Background(), i.tenantID)
	if err != nil {
		t.Fatalf("list codes: %v", err)
	}
	byCode := make(map[string]Code, len(list))
	for _, code := range list {
		byCode[code.Code] = code
	}
	return byCode
}

func (i *installation) createLocalCode(t *testing.T, code, name string) string {
	t.Helper()
	rec := i.adminCall(t, i.svc.handleCreateCode, map[string]any{
		"code":                code,
		"names":               map[string]string{"mn": name, "en": name},
		"schema":              json.RawMessage(`{"type":"object"}`),
		"default_sla_seconds": 3 * 24 * 3600,
	}, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create code = %d %s", rec.Code, rec.Body)
	}
	return decodeJSON[struct {
		ID string `json:"id"`
	}](t, rec).ID
}

func TestOnlyALocalNamespacedCodeCanBeAuthoredHere(t *testing.T) {
	pool := openPool(t)
	t.Setenv(insecurePeersEnv, "1")
	here := newInstallation(t, pool, "solo", 10)

	// The unprefixed namespace belongs to ring.dgov.mn. A code invented today
	// without a prefix collides with one published tomorrow, and the collision
	// reads as a rename rather than as two different things.
	rec := here.adminCall(t, here.svc.handleCreateCode, map[string]any{
		"code":  "D-101",
		"names": map[string]string{"mn": "Өөрийн код"},
	}, "")
	if rec.Code == http.StatusCreated {
		t.Fatal("a code was authored in the state register's namespace")
	}

	// And a code with no Mongolian name is refused: Mongolian is what every
	// other locale falls back to, so without it the code renders as its own
	// identifier everywhere.
	rec = here.adminCall(t, here.svc.handleCreateCode, map[string]any{
		"code": "local.audit", "names": map[string]string{"en": "Audit"},
	}, "")
	if rec.Code == http.StatusCreated {
		t.Fatal("a code with no Mongolian name was accepted")
	}

	here.createLocalCode(t, "local.audit", "Дотоод хяналт")
	if _, ok := here.codes(t)["local.audit"]; !ok {
		t.Error("the local code was not registered")
	}
}

func TestAParentAnnouncesOnlyWhatItOpensOnThatLink(t *testing.T) {
	pool := openPool(t)
	t.Setenv(insecurePeersEnv, "1")
	parent, child, parentPeerID, childPeerID := linkedPair(t, pool, 11)

	parent.createLocalCode(t, "local.count", "Тооллого")
	parent.createLocalCode(t, "local.secret", "Зөвхөн дотоод")

	// One of the two. A vocabulary is opened per link deliberately: a code that
	// concerns one subordinate does not concern another.
	rec := parent.adminCall(t, parent.svc.handleSetPeerCodes,
		map[string]any{"codes": []string{"local.count"}}, parentPeerID)
	if rec.Code != http.StatusOK {
		t.Fatalf("open codes = %d %s", rec.Code, rec.Body)
	}

	carry(t, parent, child, childPeerID)

	got := child.codes(t)
	code, ok := got["local.count"]
	if !ok {
		t.Fatalf("the child did not receive the opened code; it has %d codes", len(got))
	}
	if code.Source != contract.SourceLink {
		t.Errorf("source = %q, want %q", code.Source, contract.SourceLink)
	}
	if code.SourcePeerID != childPeerID {
		t.Errorf("the code is not attributed to the link it came from")
	}
	if code.Names["mn"] != "Тооллого" {
		t.Errorf("names = %v — a code's translations travel with it", code.Names)
	}
	if code.DefaultSLASeconds == nil || *code.DefaultSLASeconds != 3*24*3600 {
		t.Errorf("default_sla = %v, want the parent's norm", code.DefaultSLASeconds)
	}
	if _, leaked := got["local.secret"]; leaked {
		t.Error("a code that was never opened on this link reached the child")
	}
}

// Closing a vocabulary has to reach the child too, or a code goes on being
// usable downstream after the parent has stopped offering it.
func TestClosingACodeWithdrawsItDownstream(t *testing.T) {
	pool := openPool(t)
	t.Setenv(insecurePeersEnv, "1")
	parent, child, parentPeerID, childPeerID := linkedPair(t, pool, 12)

	parent.createLocalCode(t, "local.count", "Тооллого")
	parent.adminCall(t, parent.svc.handleSetPeerCodes,
		map[string]any{"codes": []string{"local.count"}}, parentPeerID)
	carry(t, parent, child, childPeerID)
	if _, ok := child.codes(t)["local.count"]; !ok {
		t.Fatal("the code never arrived")
	}

	// An empty set is a real announcement, not a no-op.
	parent.adminCall(t, parent.svc.handleSetPeerCodes,
		map[string]any{"codes": []string{}}, parentPeerID)
	carry(t, parent, child, childPeerID)

	if _, ok := child.codes(t)["local.count"]; ok {
		t.Error("a withdrawn code is still usable on the child")
	}
}

// A code that came from somewhere else is not editable here. An edit would be
// overwritten by the next announcement, and one that silently reverts is worse
// than one that is refused.
func TestASyncedCodeCannotBeRedefinedDownstream(t *testing.T) {
	pool := openPool(t)
	t.Setenv(insecurePeersEnv, "1")
	parent, child, parentPeerID, childPeerID := linkedPair(t, pool, 13)

	parent.createLocalCode(t, "local.count", "Тооллого")
	parent.adminCall(t, parent.svc.handleSetPeerCodes,
		map[string]any{"codes": []string{"local.count"}}, parentPeerID)
	carry(t, parent, child, childPeerID)

	synced := child.codes(t)["local.count"]
	rec := child.adminCall(t, child.svc.handleUpdateCode,
		map[string]any{"names": map[string]string{"mn": "Өөрчилсөн"}}, synced.ID)
	if rec.Code != http.StatusForbidden {
		t.Errorf("redefining a synced code answered %d, want 403", rec.Code)
	}

	// But whether this organisation uses it is its own decision.
	off := false
	rec = child.adminCall(t, child.svc.handleUpdateCode, map[string]any{"active": &off}, synced.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("deactivating a synced code = %d %s", rec.Code, rec.Body)
	}
	if child.codes(t)["local.count"].Active {
		t.Error("the code is still active after being switched off")
	}
}

func TestRingIsOffUntilItIsConfigured(t *testing.T) {
	pool := openPool(t)
	here := newInstallation(t, pool, "solo", 14)

	if here.svc.ring != nil {
		t.Fatal("the register is configured on a deployment that named no base URL")
	}
	rec := here.adminCall(t, here.svc.handleRingSync, nil, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("ring sync = %d, want 503 — an unconfigured register is not a fault in this platform", rec.Code)
	}
}

// The development mock exists so the shape of the feature can be worked on
// without credentials, and its codes have to land like any other imported ones.
func TestTheRingMockImportsIntoTheVocabulary(t *testing.T) {
	pool := openPool(t)
	t.Setenv(ringBaseURLEnv, ringMockURL)
	here := newInstallation(t, pool, "solo", 15)

	rec := here.adminCall(t, here.svc.handleRingSync, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("ring sync = %d %s", rec.Code, rec.Body)
	}

	imported := here.codes(t)
	code, ok := imported["D-101"]
	if !ok {
		t.Fatalf("nothing was imported; the vocabulary holds %d codes", len(imported))
	}
	if code.Source != contract.SourceRing {
		t.Errorf("source = %q, want %q", code.Source, contract.SourceRing)
	}
	if code.RingProcessRef == "" {
		t.Error("an imported code does not say which process it came from")
	}

	// Whether this organisation uses a code is its own decision, and a
	// re-import must not undo it.
	off := false
	here.adminCall(t, here.svc.handleUpdateCode, map[string]any{"active": &off}, code.ID)
	if rec := here.adminCall(t, here.svc.handleRingSync, nil, ""); rec.Code != http.StatusOK {
		t.Fatalf("second ring sync = %d %s", rec.Code, rec.Body)
	}
	if here.codes(t)["D-101"].Active {
		t.Error("a re-import switched a code back on that an administrator had switched off")
	}
}

// Revoking a link takes its vocabulary with it: those codes were that link's
// and mean nothing without it.
func TestRevokingALinkRemovesTheCodesItAnnounced(t *testing.T) {
	pool := openPool(t)
	t.Setenv(insecurePeersEnv, "1")
	parent, child, parentPeerID, childPeerID := linkedPair(t, pool, 16)

	parent.createLocalCode(t, "local.count", "Тооллого")
	parent.adminCall(t, parent.svc.handleSetPeerCodes,
		map[string]any{"codes": []string{"local.count"}}, parentPeerID)
	carry(t, parent, child, childPeerID)
	if _, ok := child.codes(t)["local.count"]; !ok {
		t.Fatal("the code never arrived")
	}

	if _, err := pool.Exec(nexus.WithWorkspaceID(context.Background(), child.tenantID),
		`DELETE FROM urtuu_peers WHERE id = $1`, childPeerID); err != nil {
		t.Fatalf("remove link: %v", err)
	}
	if _, ok := child.codes(t)["local.count"]; ok {
		t.Error("a link's vocabulary outlived the link")
	}
}
