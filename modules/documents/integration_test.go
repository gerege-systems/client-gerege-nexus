package documents

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/documents"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests exercise signing against a real PostgreSQL schema, because what
// they protect lives partly in SQL: the status guard that keeps a decided
// document from being signed twice, the unique constraint that keeps one citizen
// from counting as two approvals, and the two counts the list derives from the
// ledger and the approval chain.
//
// They are skipped unless a migrated throwaway database is provided:
//
//	DOCUMENTS_TEST_DATABASE_URL=postgres://... go test ./internal/apps/documents/...
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DOCUMENTS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set DOCUMENTS_TEST_DATABASE_URL to a migrated test database to run documents integration tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type fixture struct {
	m        *DocumentsModule
	tenantID string
	// signer is the rail a document carrying a file is signed through. It is
	// reachable from the tests so that one can make it answer badly — see
	// fakeSigner.
	signer *fakeSigner
}

// newFixture builds a module wired to its own tenant, so one test's documents
// are never another's. The identity clients are pinned to mock mode: these tests
// are about what the module records, not about reaching sso.gov.mn.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	// The rails are fakes now, not the platform's mock clients — see
	// rails_fake_test.go. The environment variables they read are gone with
	// them.

	pool := testPool(t)
	signer := newFakeSigner()
	m := &DocumentsModule{db: pool, eidSvc: newFakeEID(), danSvc: fakeDAN{reachable: true}, signer: signer}

	var tenantID string
	slug := fmt.Sprintf("docs-test-%s", randomSuffix(t))
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO tenants (slug, name) VALUES ($1, $2) RETURNING id`,
		slug, "Documents integration test").Scan(&tenantID); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		// The schema cascades from tenants, so one delete clears the documents,
		// signatures, chains, policies, templates and rules this test made.
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	return &fixture{m: m, tenantID: tenantID, signer: signer}
}

// signWithEID runs the whole approval ceremony: push the request to the citizen,
// then poll until they approve. In mock mode the citizen approves after a moment,
// which is exactly the shape a live approval has.
func signWithEID(t *testing.T, f *fixture, docID, regNumber string) (*Document, error) {
	t.Helper()
	ctx := context.Background()

	session, err := f.m.StartEIDSignature(ctx, f.tenantID, docID, regNumber)
	if err != nil {
		return nil, err
	}
	if session.VerificationCode == "" {
		t.Error("the citizen has no code to check the request against")
	}
	if !strings.Contains(session.DisplayText, "Гарын үсэг") {
		t.Errorf("display text %q does not say what is being approved", session.DisplayText)
	}
	return pollUntilApproved(t, f, docID, session.SessionID)
}

// pollUntilApproved waits on one already-started session, the way a screen does.
func pollUntilApproved(t *testing.T, f *fixture, docID, sessionID string) (*Document, error) {
	t.Helper()
	ctx := context.Background()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		progress, err := f.m.PollEIDSignature(ctx, f.tenantID, docID, sessionID)
		if err != nil {
			return nil, err
		}
		if progress.State == ApprovalComplete {
			return progress.Document, nil
		}
		if progress.State != ApprovalRunning {
			return nil, fmt.Errorf("approval ended as %s", progress.State)
		}
		time.Sleep(150 * time.Millisecond)
	}
	return nil, errors.New("the approval never completed")
}

// randomSuffix borrows the database's uuid generator rather than math/rand, so a
// test never has to seed anything to get an unused tenant slug.
func randomSuffix(t *testing.T) string {
	t.Helper()
	var suffix string
	if err := testPool(t).QueryRow(context.Background(),
		`SELECT left(gen_random_uuid()::text, 8)`).Scan(&suffix); err != nil {
		t.Fatalf("generate suffix: %v", err)
	}
	return suffix
}

func TestSigningApprovesWhenNoChainIsConfigured(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Хамтран ажиллах гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if doc.Status != StatusPending {
		t.Fatalf("new document status = %q, want %q", doc.Status, StatusPending)
	}
	if doc.RequiredSignatures != 1 || doc.SignatureCount != 0 {
		t.Errorf("new document progress = %d/%d, want 0/1", doc.SignatureCount, doc.RequiredSignatures)
	}

	signed, err := signWithEID(t, f, doc.ID, "AA90010111")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if signed.Status != StatusApproved {
		t.Errorf("status = %q, want %q", signed.Status, StatusApproved)
	}
	if signed.SignatureCount != 1 || signed.RequiredSignatures != 1 {
		t.Errorf("progress = %d/%d, want 1/1", signed.SignatureCount, signed.RequiredSignatures)
	}
	if signed.SignerRegNumber != "AA90010111" {
		t.Errorf("signer = %q, want AA90010111", signed.SignerRegNumber)
	}
	if signed.SignedAt == nil {
		t.Error("signed_at was not recorded")
	}

	ledger, err := f.m.ListSignatures(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("signatures: %v", err)
	}
	if len(ledger) != 1 {
		t.Fatalf("ledger has %d rows, want 1", len(ledger))
	}
	if ledger[0].SignerMethod != SignerEID || ledger[0].SignatureHash == "" {
		t.Errorf("ledger row = %+v, want an E-ID row referencing the approval", ledger[0])
	}
	// And it says what it is worth. Nothing here signs a document's content —
	// this app holds none — so the record must publish an approval and not let
	// a reader take it for a signature over the bytes. See ADR 0002.
	if ledger[0].Proof != domain.ProofApproval {
		t.Errorf("ledger proof = %q, want %q", ledger[0].Proof, domain.ProofApproval)
	}
	if signed.Proof != domain.ProofApproval {
		t.Errorf("document proof = %q, want %q", signed.Proof, domain.ProofApproval)
	}

	// A decided document is not signable again — the refusal comes before anybody
	// is asked to approve anything.
	if _, err := f.m.StartEIDSignature(ctx, f.tenantID, doc.ID, "BB90010111"); !errors.Is(err, ErrNotSignable) {
		t.Errorf("second sign: got %v, want ErrNotSignable", err)
	}
}

// An approval is given for one document, against a display text naming it. It
// must not be redeemable against another, or the citizen's consent has been moved
// to something they never read.
func TestAnApprovalCannotBeMovedToAnotherDocument(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	signMe, err := f.m.CreateDocument(ctx, f.tenantID, "Иргэн харсан гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	other, err := f.m.CreateDocument(ctx, f.tenantID, "Иргэн хараагүй гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create other: %v", err)
	}

	session, err := f.m.StartEIDSignature(ctx, f.tenantID, signMe.ID, "AA90010111")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	if _, err := f.m.PollEIDSignature(ctx, f.tenantID, other.ID, session.SessionID); !errors.Is(err, ErrSignSessionUnknown) {
		t.Errorf("polling another document with this session: got %v, want ErrSignSessionUnknown", err)
	}
	// And the document the citizen never saw stays unsigned.
	untouched, err := f.m.getDocument(ctx, f.tenantID, other.ID)
	if err != nil {
		t.Fatalf("read other: %v", err)
	}
	if untouched.SignatureCount != 0 || untouched.Status != StatusPending {
		t.Errorf("other document = %d signature(s), status %q; want it untouched", untouched.SignatureCount, untouched.Status)
	}

	// This same session, polled against the document it was started for, signs it.
	signed, err := pollUntilApproved(t, f, signMe.ID, session.SessionID)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// And it is spent: polling again reports the document it already signed rather
	// than signing a second time.
	again, err := f.m.PollEIDSignature(ctx, f.tenantID, signMe.ID, session.SessionID)
	if err != nil {
		t.Fatalf("re-poll: %v", err)
	}
	if again.State != ApprovalComplete || again.Document == nil {
		t.Fatalf("re-poll = %+v, want the signature it already produced", again)
	}
	if again.Document.SignatureCount != signed.SignatureCount {
		t.Errorf("re-poll changed the count to %d, want %d", again.Document.SignatureCount, signed.SignatureCount)
	}
}

// Another tenant cannot poll a session even holding its id.
func TestASignSessionIsScopedToItsTenant(t *testing.T) {
	owner := newFixture(t)
	intruder := newFixture(t)
	ctx := context.Background()

	doc, err := owner.m.CreateDocument(ctx, owner.tenantID, "Бусдын гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	session, err := owner.m.StartEIDSignature(ctx, owner.tenantID, doc.ID, "AA90010111")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	if _, err := intruder.m.PollEIDSignature(ctx, intruder.tenantID, doc.ID, session.SessionID); !errors.Is(err, ErrSignSessionUnknown) {
		t.Errorf("cross-tenant poll: got %v, want ErrSignSessionUnknown", err)
	}
}

func TestAChainOfTwoNeedsTwoDifferentSigners(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "REQUEST", []WorkflowStep{
		{Name: "Хэлтсийн дарга"},
		{Name: "Захирал"},
	}); err != nil {
		t.Fatalf("configure chain: %v", err)
	}

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Албан хүсэлт", "REQUEST")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if doc.RequiredSignatures != 2 {
		t.Fatalf("required = %d, want 2", doc.RequiredSignatures)
	}

	first, err := signWithEID(t, f, doc.ID, "AA90010111")
	if err != nil {
		t.Fatalf("first sign: %v", err)
	}
	if first.Status != StatusPending {
		t.Errorf("after one of two signatures status = %q, want it to stay %q", first.Status, StatusPending)
	}
	if first.SignatureCount != 1 {
		t.Errorf("progress = %d/2, want 1/2", first.SignatureCount)
	}

	// The same citizen approving again is the same approval, not the second one.
	if _, err := signWithEID(t, f, doc.ID, "AA90010111"); !errors.Is(err, ErrAlreadySigned) {
		t.Errorf("same signer twice: got %v, want ErrAlreadySigned", err)
	}

	second, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "BB90010111", "123456")
	if err != nil {
		t.Fatalf("second sign: %v", err)
	}
	if second.Status != StatusApproved {
		t.Errorf("status = %q, want %q once the chain is complete", second.Status, StatusApproved)
	}
	if second.SignatureCount != 2 {
		t.Errorf("progress = %d/2, want 2/2", second.SignatureCount)
	}
	// The record mirrors the newest signature, which is the DAN one.
	if second.SignerMethod != SignerDAN {
		t.Errorf("mirrored method = %q, want %q", second.SignerMethod, SignerDAN)
	}
}

func TestPolicyRefusesAChannelAndAnUnnamedSigner(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.m.SaveSignaturePolicy(ctx, f.tenantID, SignaturePolicy{
		DocType: "APPROVAL", AllowEID: true, AllowDAN: false,
	}); err != nil {
		t.Fatalf("save policy: %v", err)
	}

	// Requiring a named signer cannot be saved while the chain names nobody: it
	// would leave the type unsignable by anyone.
	if _, err := f.m.SaveSignaturePolicy(ctx, f.tenantID, SignaturePolicy{
		DocType: "APPROVAL", AllowEID: true, RequireNamedSigner: true,
	}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Errorf("policy with no named step: got %v, want ErrInvalidConfiguration", err)
	}

	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "APPROVAL", []WorkflowStep{
		{Name: "Захирал", SignerRegNumber: "CC90010111"},
	}); err != nil {
		t.Fatalf("configure chain: %v", err)
	}
	if _, err := f.m.SaveSignaturePolicy(ctx, f.tenantID, SignaturePolicy{
		DocType: "APPROVAL", AllowEID: true, RequireNamedSigner: true,
	}); err != nil {
		t.Fatalf("save policy once a signer is named: %v", err)
	}

	// The document takes the chain as it stands now, so it is created after it.
	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Дотоод батламж", "APPROVAL")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "CC90010111", "123456"); !errors.Is(err, ErrSignatureRejected) {
		t.Errorf("DAN against an E-ID-only policy: got %v, want ErrSignatureRejected", err)
	}

	// The refusal lands at start: there is no reason to push a request to somebody
	// whose approval could never be recorded.
	if _, err := f.m.StartEIDSignature(ctx, f.tenantID, doc.ID, "AA90010111"); !errors.Is(err, ErrSignatureRejected) {
		t.Errorf("signer the step does not name: got %v, want ErrSignatureRejected", err)
	}
	if _, err := signWithEID(t, f, doc.ID, "CC90010111"); err != nil {
		t.Errorf("the named signer must be accepted: %v", err)
	}

	// And the chain cannot be emptied of named signers while the policy demands one.
	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "APPROVAL", []WorkflowStep{{Name: "Хэн ч"}}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Errorf("chain dropping its named signer: got %v, want ErrInvalidConfiguration", err)
	}
}

// A named step is binding on its own — the signature policy decides whether OPEN
// steps are allowed at all, not whether a named one means anything. A tenant that
// names its approvers and never opens the policy screen still gets them enforced.
func TestANamedStepIsBindingWithoutThePolicyFlag(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "REQUEST", []WorkflowStep{
		{Name: "Хэлтсийн дарга", SignerRegNumber: "AA90010111"},
		{Name: "Захирал"}, // open on purpose: anyone may take the second approval
	}); err != nil {
		t.Fatalf("configure chain: %v", err)
	}

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Албан хүсэлт", "REQUEST")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Step one names the department head, and the policy was never touched.
	if _, err := f.m.StartEIDSignature(ctx, f.tenantID, doc.ID, "ZZ99999999"); !errors.Is(err, ErrSignatureRejected) {
		t.Errorf("a stranger taking a named step: got %v, want ErrSignatureRejected", err)
	}
	if _, err := signWithEID(t, f, doc.ID, "AA90010111"); err != nil {
		t.Fatalf("the named signer must be accepted: %v", err)
	}

	// Step two names nobody, so anyone allowed to sign may finish it.
	done, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "ZZ99999999", "123456")
	if err != nil {
		t.Fatalf("open step: %v", err)
	}
	if done.Status != StatusApproved {
		t.Errorf("status = %q, want %q", done.Status, StatusApproved)
	}

	ledger, err := f.m.ListSignatures(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("signatures: %v", err)
	}
	if len(ledger) != 2 || ledger[0].StepOrder != 1 || ledger[1].StepOrder != 2 {
		t.Errorf("ledger = %+v, want the two signatures recorded against steps 1 and 2", ledger)
	}
	if ledger[0].SignerRegNumber != "AA90010111" {
		t.Errorf("step 1 was filled by %q, want the citizen it names", ledger[0].SignerRegNumber)
	}
}

// What a document needed is its own property. Editing the type's chain afterwards
// must not change what an in-flight document is waiting for, nor rewrite what a
// finished one required.
func TestAChainEditDoesNotReachDocumentsAlreadyWaiting(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{
		{Name: "Нэг"}, {Name: "Хоёр"}, {Name: "Гурав"},
	}); err != nil {
		t.Fatalf("configure chain: %v", err)
	}

	inFlight, err := f.m.CreateDocument(ctx, f.tenantID, "Гурван гарын үсэгтэй гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if inFlight.RequiredSignatures != 3 {
		t.Fatalf("required = %d, want 3", inFlight.RequiredSignatures)
	}
	if _, err := signWithEID(t, f, inFlight.ID, "AA90010111"); err != nil {
		t.Fatalf("first sign: %v", err)
	}

	// Shrinking the chain used to strand this document: its progress would read
	// 1/1 "complete" while its status stayed PENDING, and no further signature
	// could finish it.
	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{{Name: "Зөвхөн нэг"}}); err != nil {
		t.Fatalf("shrink chain: %v", err)
	}

	still, err := f.m.getDocument(ctx, f.tenantID, inFlight.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if still.RequiredSignatures != 3 {
		t.Errorf("required = %d, want it to stay 3 — the document was routed under a three-step chain", still.RequiredSignatures)
	}
	if still.Status != StatusPending {
		t.Errorf("status = %q, want %q", still.Status, StatusPending)
	}

	// And it can still be finished, on the terms it started with.
	if _, err := f.m.SignWithDAN(ctx, f.tenantID, inFlight.ID, "BB90010111", "123456"); err != nil {
		t.Fatalf("second sign: %v", err)
	}
	finished, err := signWithEID(t, f, inFlight.ID, "CC90010111")
	if err != nil {
		t.Fatalf("third sign: %v", err)
	}
	if finished.Status != StatusApproved {
		t.Errorf("status = %q, want %q on the third of three", finished.Status, StatusApproved)
	}

	// A document created after the edit takes the new chain.
	fresh, err := f.m.CreateDocument(ctx, f.tenantID, "Нэг гарын үсэгтэй гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create fresh: %v", err)
	}
	if fresh.RequiredSignatures != 1 {
		t.Errorf("required = %d, want 1 from the chain as it now stands", fresh.RequiredSignatures)
	}

	// And the finished document's history stays what it was, whatever happens next.
	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{
		{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "D"}, {Name: "E"},
	}); err != nil {
		t.Fatalf("grow chain: %v", err)
	}
	decided, err := f.m.getDocument(ctx, f.tenantID, inFlight.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if decided.RequiredSignatures != 3 || decided.SignatureCount != 3 {
		t.Errorf("decided document reads %d/%d, want 3/3 for ever",
			decided.SignatureCount, decided.RequiredSignatures)
	}
	if decided.Status != StatusApproved {
		t.Errorf("status = %q, want it to stay %q", decided.Status, StatusApproved)
	}
}

// The policy screen and the chain screen edit one setting between them: a policy
// that only accepts named signers needs a chain that names one per step. Saved
// concurrently against each other's stale state, both guards used to pass and the
// pair committed exactly what they exist to prevent — a type nobody could sign.
// One of the two must lose.
func TestThePolicyAndTheChainCannotLockATypeOutTogether(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// A chain that names its only signer, and a policy that does not yet insist.
	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "REQUEST", []WorkflowStep{
		{Name: "Захирал", SignerRegNumber: "CC90010111"},
	}); err != nil {
		t.Fatalf("configure chain: %v", err)
	}

	// One request opens the step; the other demands that no step be open.
	start := make(chan struct{})
	var chainErr, policyErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, chainErr = f.m.ReplaceWorkflow(ctx, f.tenantID, "REQUEST", []WorkflowStep{{Name: "Хэн ч"}})
	}()
	go func() {
		defer wg.Done()
		<-start
		_, policyErr = f.m.SaveSignaturePolicy(ctx, f.tenantID, SignaturePolicy{
			DocType: "REQUEST", AllowEID: true, AllowDAN: true, RequireNamedSigner: true,
		})
	}()
	close(start)
	wg.Wait()

	if chainErr == nil && policyErr == nil {
		t.Error("both saves succeeded, so the type is now unsignable by anybody")
	}

	// Whatever won, the stored pair has to be one a citizen could satisfy.
	policy, err := f.m.SignaturePolicyFor(ctx, f.tenantID, "REQUEST")
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	chains, err := f.m.ListWorkflows(ctx, f.tenantID)
	if err != nil {
		t.Fatalf("read chains: %v", err)
	}
	var steps []WorkflowStep
	for _, chain := range chains {
		if chain.DocType == "REQUEST" {
			steps = chain.Steps
		}
	}
	if policy.RequireNamedSigner {
		if err := domain.StepsCanRequireNamedSigners("REQUEST", steps); err != nil {
			t.Errorf("stored state cannot be satisfied by anyone: %v (chain %+v)", err, steps)
		}
	}

	// And the type is still signable in practice.
	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Албан хүсэлт", "REQUEST")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	signer := "CC90010111"
	if !policy.RequireNamedSigner && len(steps) > 0 && steps[0].SignerRegNumber == "" {
		signer = "AA90010111" // the open step lets anyone in
	}
	if _, err := signWithEID(t, f, doc.ID, signer); err != nil {
		t.Errorf("nobody can sign a %s any more: %v", "REQUEST", err)
	}
}

// A chain naming one citizen at two steps could never be completed — they sign a
// document once — so it has to be refused when it is saved, whatever the signature
// policy says. The old guard only ran when the policy demanded named signers.
func TestAChainCannotNameOneCitizenTwice(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	_, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{
		{Name: "Хэлтсийн дарга", SignerRegNumber: "AA90010111"},
		{Name: "Захирал", SignerRegNumber: "AA90010111"},
	})
	if !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("got %v, want ErrInvalidConfiguration", err)
	}
	if !strings.Contains(err.Error(), "AA90010111") {
		t.Errorf("got %q, want it to name the repeated citizen", err)
	}

	// Case matters as little here as anywhere else: the check runs on the
	// normalised value.
	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{
		{Name: "Нэг", SignerRegNumber: "aa90010111"},
		{Name: "Хоёр", SignerRegNumber: "AA90010111 "},
	}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Errorf("same citizen written two ways: got %v, want ErrInvalidConfiguration", err)
	}
}

// An open step is open to anyone — except somebody the chain still needs further
// along. Letting them spend their one signature early would leave their own step
// unfillable and the document unapprovable, with a legal chain and no policy flag
// involved.
func TestAnOpenStepHoldsBackALaterStepsSigner(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// The natural ordering: a reviewer anyone may be, then the director.
	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{
		{Name: "Хянагч"}, // open
		{Name: "Захирал", SignerRegNumber: "AA90010111"},
	}); err != nil {
		t.Fatalf("configure chain: %v", err)
	}

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Хоёр шаттай гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The director cannot take the reviewer's step, even though it names nobody.
	if _, err := f.m.StartEIDSignature(ctx, f.tenantID, doc.ID, "AA90010111"); !errors.Is(err, ErrSignatureRejected) {
		t.Errorf("the director taking the open step: got %v, want ErrSignatureRejected", err)
	}

	// Anyone else may.
	if _, err := signWithEID(t, f, doc.ID, "ZZ99999999"); err != nil {
		t.Fatalf("open step: %v", err)
	}
	// And then the director finishes their own.
	done, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "AA90010111", "123456")
	if err != nil {
		t.Fatalf("director: %v", err)
	}
	if done.Status != StatusApproved {
		t.Errorf("status = %q, want %q", done.Status, StatusApproved)
	}
}

// Asking a citizen to approve on their own device and then throwing the approval
// away because they had already signed is worse than not asking.
func TestARepeatSignerIsRefusedBeforeTheCitizenIsAsked(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "REQUEST", []WorkflowStep{
		{Name: "Нэг"}, {Name: "Хоёр"}, // both open
	}); err != nil {
		t.Fatalf("configure chain: %v", err)
	}

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Хоёр нээлттэй шат", "REQUEST")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := signWithEID(t, f, doc.ID, "AA90010111"); err != nil {
		t.Fatalf("first signature: %v", err)
	}

	// The same citizen for the second, still-open step: refused at start, before a
	// request reaches their phone.
	if _, err := f.m.StartEIDSignature(ctx, f.tenantID, doc.ID, "AA90010111"); !errors.Is(err, ErrAlreadySigned) {
		t.Errorf("eid start: got %v, want ErrAlreadySigned", err)
	}
	if _, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "AA90010111", "123456"); !errors.Is(err, ErrAlreadySigned) {
		t.Errorf("dan: got %v, want ErrAlreadySigned", err)
	}

	// Somebody else still finishes it.
	done, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "BB90010111", "123456")
	if err != nil {
		t.Fatalf("second signer: %v", err)
	}
	if done.Status != StatusApproved {
		t.Errorf("status = %q, want %q", done.Status, StatusApproved)
	}
}

// The citizen has already approved by the time the signature is written, so an
// operator closing the dialog — which aborts the poll and cancels its context —
// must not destroy it.
func TestASignatureSurvivesTheCallerHangingUp(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Цуцлагдсан хүсэлт", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	session, err := f.m.StartEIDSignature(ctx, f.tenantID, doc.ID, "AA90010111")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for the mock citizen to approve, then poll on a context that is
	// cancelled the instant the write begins — the shape of a dialog being closed.
	time.Sleep(1800 * time.Millisecond)
	cancelled, cancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	_, _ = f.m.PollEIDSignature(cancelled, f.tenantID, doc.ID, session.SessionID)

	// Whatever the caller was told, the citizen's approval is either fully applied
	// or not applied at all — never half.
	after, err := f.m.getDocument(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	ledger, err := f.m.ListSignatures(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("signatures: %v", err)
	}
	if len(ledger) != after.SignatureCount {
		t.Errorf("ledger has %d rows but the document reports %d", len(ledger), after.SignatureCount)
	}
	if after.SignatureCount == 1 && after.Status != StatusApproved {
		t.Errorf("a single-signature document with its signature must be %q, got %q", StatusApproved, after.Status)
	}
	// And the session cannot be spent twice: a later poll reports the same outcome.
	if after.SignatureCount == 1 {
		again, err := f.m.PollEIDSignature(ctx, f.tenantID, doc.ID, session.SessionID)
		if err != nil {
			t.Fatalf("re-poll: %v", err)
		}
		if again.State != ApprovalComplete || again.Document == nil || again.Document.SignatureCount != 1 {
			t.Errorf("re-poll = %+v, want the one signature it already produced", again)
		}
	}
}

// A document may hold a signature on a LATER step while an earlier one is still
// open: migration 00017 credits a pre-existing signature to the step that names its
// signer, not to its place in time, because order did not matter before. The next
// approval is therefore the lowest unfilled step — not "one more than the count",
// which would ask a citizen who has already signed and stick for ever.
func TestASignatureOnALaterStepLeavesTheEarlierOneOpen(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{
		{Name: "Захирал", SignerRegNumber: "AA90010111"},
		{Name: "Ня-бо", SignerRegNumber: "BB90010111"},
	}); err != nil {
		t.Fatalf("configure chain: %v", err)
	}

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Дараалал зөрсөн гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Stand in for what the migration leaves behind: the accountant signed first,
	// credited to the step that names them.
	if _, err := f.m.db.Exec(ctx,
		`INSERT INTO document_signatures
		        (tenant_id, document_id, signer_name, signer_reg_number, signer_method,
		         signature_hash, signed_at, step_order)
		 VALUES ($1, $2, 'Ня-бо', 'BB90010111', 'EID', 'legacy', NOW() - INTERVAL '2 days', 2)`,
		f.tenantID, doc.ID); err != nil {
		t.Fatalf("insert legacy signature: %v", err)
	}

	// One signature, but step 1 is what is missing — and it belongs to the director.
	// Anyone else is turned away by whose step it is, which is the more useful thing
	// to be told: the accountant is not asked to sign twice, they are told it is the
	// director's turn.
	for _, who := range []string{"BB90010111", "ZZ99999999"} {
		err := func() error {
			_, err := f.m.StartEIDSignature(ctx, f.tenantID, doc.ID, who)
			return err
		}()
		if !errors.Is(err, ErrSignatureRejected) {
			t.Errorf("%s taking the director's step: got %v, want ErrSignatureRejected", who, err)
		}
		if err != nil && !strings.Contains(err.Error(), "AA90010111") {
			t.Errorf("%s: got %q, want it to say whose step it is", who, err)
		}
	}

	done, err := signWithEID(t, f, doc.ID, "AA90010111")
	if err != nil {
		t.Fatalf("the director must be able to finish it: %v", err)
	}
	if done.Status != StatusApproved {
		t.Errorf("status = %q, want %q once both steps are filled", done.Status, StatusApproved)
	}
	if done.SignatureCount != 2 || done.RequiredSignatures != 2 {
		t.Errorf("progress = %d/%d, want 2/2", done.SignatureCount, done.RequiredSignatures)
	}

	ledger, err := f.m.ListSignatures(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("signatures: %v", err)
	}
	if len(ledger) != 2 || ledger[0].StepOrder != 1 || ledger[1].StepOrder != 2 {
		t.Errorf("ledger = %+v, want step 1 then step 2", ledger)
	}
	if ledger[0].SignerRegNumber != "AA90010111" || ledger[1].SignerRegNumber != "BB90010111" {
		t.Errorf("each step must be filled by the citizen it names, got %q then %q",
			ledger[0].SignerRegNumber, ledger[1].SignerRegNumber)
	}
}

// A chain naming one citizen twice cannot be saved any more, but chains stored
// before that rule existed still can be. The path that decides who may sign must
// never copy one verbatim: the second step would be owed to somebody who has
// already signed, and no document of that type could ever be approved.
func TestASnapshotOpensARepeatedSigner(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Straight into the table, the way a chain stored under the old rules looks.
	for order, name := range map[int]string{1: "Ня-бо", 2: "Захирал"} {
		if _, err := f.m.db.Exec(ctx,
			`INSERT INTO document_workflow_steps (tenant_id, doc_type, step_order, name, signer_reg_number)
			      VALUES ($1, 'CONTRACT', $2, $3, 'AA90010111')`,
			f.tenantID, order, name); err != nil {
			t.Fatalf("insert legacy step %d: %v", order, err)
		}
	}

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Хуучин давхар хэлхээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if doc.RequiredSignatures != 2 {
		t.Fatalf("required = %d, want 2 — the tenant asked for two approvals", doc.RequiredSignatures)
	}

	steps, err := f.m.DocumentSteps(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("steps: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	if steps[0].SignerRegNumber != "AA90010111" {
		t.Errorf("step 1 = %q, want the citizen the chain names", steps[0].SignerRegNumber)
	}
	if steps[1].SignerRegNumber != "" {
		t.Errorf("step 2 = %q, want it copied open — the same citizen cannot fill both",
			steps[1].SignerRegNumber)
	}

	// And it can actually be finished: the named citizen, then anyone else.
	if _, err := signWithEID(t, f, doc.ID, "AA90010111"); err != nil {
		t.Fatalf("the named signer: %v", err)
	}
	done, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "ZZ99999999", "123456")
	if err != nil {
		t.Fatalf("the open step: %v", err)
	}
	if done.Status != StatusApproved {
		t.Errorf("status = %q, want %q — a legacy chain must not strand its documents",
			done.Status, StatusApproved)
	}
}

// The other way a stored chain carries a step nobody can fill: a name too short to be
// a registration number. Nothing checked the length before this release, and the number
// was ignored unless the policy asked for it, so a typo sat there harmlessly meaning
// "one more signature". Copied verbatim it now names a citizen who cannot present that
// number to any provider, and rejection becomes the document's only exit.
func TestASnapshotOpensAStepNamingSomethingUnusable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Straight into the table: a real signer, then a typo, the way it was stored.
	for order, reg := range map[int]string{1: "AA90010111", 2: "AA9"} {
		if _, err := f.m.db.Exec(ctx,
			`INSERT INTO document_workflow_steps (tenant_id, doc_type, step_order, name, signer_reg_number)
			      VALUES ($1, 'CONTRACT', $2, 'Шат', $3)`,
			f.tenantID, order, reg); err != nil {
			t.Fatalf("insert legacy step %d: %v", order, err)
		}
	}

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Хуучин алдаатай хэлхээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	steps, err := f.m.DocumentSteps(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("steps: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2 — the tenant asked for two approvals", len(steps))
	}
	if steps[0].SignerRegNumber != "AA90010111" {
		t.Errorf("step 1 = %q, want the citizen the chain names", steps[0].SignerRegNumber)
	}
	if steps[1].SignerRegNumber != "" {
		t.Errorf("step 2 = %q, want it copied open — no provider would accept that number",
			steps[1].SignerRegNumber)
	}

	// And the document can be finished, which is the whole point.
	if _, err := signWithEID(t, f, doc.ID, "AA90010111"); err != nil {
		t.Fatalf("the named signer: %v", err)
	}
	done, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "ZZ99999999", "123456")
	if err != nil {
		t.Fatalf("the opened step: %v", err)
	}
	if done.Status != StatusApproved {
		t.Errorf("status = %q, want %q — an unusable name must not strand the type",
			done.Status, StatusApproved)
	}
}

// Three decisions compare a registration number to another registration number: whether
// a named step is this citizen's, whether they have signed already, and the ledger's
// one-per-signer constraint. All three are string comparisons, so the shape the number
// arrives in must not matter — a citizen presenting their own number in lower case is
// the named signer, and is not a second person who may sign again.
func TestARegistrationNumbersShapeDoesNotDecideWhoSigned(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{
		{Order: 1, Name: "Ня-бо", SignerRegNumber: "AA90010111"},
		{Order: 2, Name: "Захирал", SignerRegNumber: ""},
	}); err != nil {
		t.Fatalf("chain: %v", err)
	}
	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Хэлбэрийн шалгалт", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The named citizen, presenting their number the other way round.
	signed, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "  aa90010111  ", "123456")
	if err != nil {
		t.Fatalf("the named signer in lower case: %v — the step is theirs", err)
	}
	if signed.SignatureCount != 1 {
		t.Fatalf("count = %d, want 1", signed.SignatureCount)
	}

	// Stored normalised, so the ledger and the trail name one citizen, not two shapes.
	sigs, err := f.m.ListSignatures(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("signatures: %v", err)
	}
	if len(sigs) != 1 || sigs[0].SignerRegNumber != "AA90010111" {
		t.Fatalf("ledger = %+v, want one row holding AA90010111", sigs)
	}

	// And the other shape is the same person, so it cannot fill the open step too.
	if _, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "AA90010111", "123456"); err == nil {
		t.Error("the same citizen signed twice by changing the case of their number")
	}
	if _, err := signWithEID(t, f, doc.ID, "aa90010111"); err == nil {
		t.Error("the same citizen signed twice through the other provider")
	}

	// Somebody else still can.
	done, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "ZZ99999999", "123456")
	if err != nil {
		t.Fatalf("the open step: %v", err)
	}
	if done.Status != StatusApproved {
		t.Errorf("status = %q, want %q", done.Status, StatusApproved)
	}
}

// The end of the same story, through the real tables: a Mongolian chain, in the
// Cyrillic the numbers are actually issued in, must reach a document's own chain
// NAMED and be fillable by the citizen it names. Measuring the bound in bytes let
// such a chain be saved and then quietly opened, so the workflows screen promised an
// approver the document did not have.
func TestACyrillicChainReachesTheDocumentNamed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{
		{Order: 1, Name: "Ня-бо", SignerRegNumber: "уб99010111"},
		{Order: 2, Name: "Захирал", SignerRegNumber: "УХ88070202"},
	}); err != nil {
		t.Fatalf("a Cyrillic chain must be storable: %v", err)
	}

	// Six characters is not a registration number, whatever its byte count.
	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "REQUEST", []WorkflowStep{
		{Order: 1, Name: "Хагас", SignerRegNumber: "УБ9901"},
	}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("got %v, want a refusal — УБ9901 is 6 characters", err)
	}

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Кирилл хэлхээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	steps, err := f.m.DocumentSteps(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("steps: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	if steps[0].SignerRegNumber != "УБ99010111" || steps[1].SignerRegNumber != "УХ88070202" {
		t.Fatalf("the document's chain = %q/%q, want both citizens named in upper case",
			steps[0].SignerRegNumber, steps[1].SignerRegNumber)
	}

	// And it can be completed by the citizens it names, and nobody else.
	if _, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "ЗЗ99999999", "123456"); err == nil {
		t.Error("a stranger filled a named step")
	}
	if _, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "уб99010111", "123456"); err != nil {
		t.Fatalf("the citizen the step names: %v", err)
	}
	done, err := signWithEID(t, f, doc.ID, "ух88070202")
	if err != nil {
		t.Fatalf("the second citizen: %v", err)
	}
	if done.Status != StatusApproved {
		t.Errorf("status = %q, want %q", done.Status, StatusApproved)
	}

	sigs, err := f.m.ListSignatures(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("signatures: %v", err)
	}
	for _, sig := range sigs {
		if sig.SignerRegNumber != strings.ToUpper(sig.SignerRegNumber) {
			t.Errorf("the ledger holds %q, want it normalised", sig.SignerRegNumber)
		}
	}
}

// A chain whose step numbers are not 1..n. Nothing this module writes produces one —
// ReplaceWorkflow renumbers from 1 — but the table only requires them positive and
// distinct, so a chain edited by hand can carry gaps. The copy must preserve the
// numbers rather than renumber them: the requirement is counted from the rows
// inserted, and the next approval is the lowest UNFILLED number, so a renumbering
// that disagreed with the signatures already placed would strand the document.
func TestAChainWithGapsInItsNumbersStillWorks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	for order, reg := range map[int]string{3: "AA90010111", 9: ""} {
		if _, err := f.m.db.Exec(ctx,
			`INSERT INTO document_workflow_steps (tenant_id, doc_type, step_order, name, signer_reg_number)
			      VALUES ($1, 'CONTRACT', $2, 'Шат', $3)`,
			f.tenantID, order, reg); err != nil {
			t.Fatalf("insert step %d: %v", order, err)
		}
	}

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Цоорхойтой хэлхээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if doc.RequiredSignatures != 2 {
		t.Fatalf("required = %d, want 2 — two steps were copied", doc.RequiredSignatures)
	}
	steps, err := f.m.DocumentSteps(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("steps: %v", err)
	}
	if len(steps) != 2 || steps[0].Order != 3 || steps[1].Order != 9 {
		t.Fatalf("the document's chain = %+v, want the numbers it was given", steps)
	}

	// The named citizen owns step 3, so nobody else may go first.
	if _, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "ZZ99999999", "123456"); err == nil {
		t.Error("a stranger filled the named step")
	}
	if _, err := signWithEID(t, f, doc.ID, "AA90010111"); err != nil {
		t.Fatalf("the named signer: %v", err)
	}
	done, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "ZZ99999999", "123456")
	if err != nil {
		t.Fatalf("the open step: %v", err)
	}
	if done.Status != StatusApproved || done.OutstandingSteps != 0 {
		t.Errorf("status = %q with %d outstanding, want APPROVED with none",
			done.Status, done.OutstandingSteps)
	}
}

// A retired template is not a missing one. The row is on the operator's screen,
// greyed out; answering "template not found" sends them looking for something that
// is not wrong. The Use button is hidden on a retired row, so reaching this means the
// page is older than the retirement — which is exactly when the answer matters.
func TestARetiredTemplateSaysSoRatherThanVanishing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	tpl, err := f.m.CreateTemplate(ctx, f.tenantID, "Хүчингүй загвар", "CONTRACT", "Гэрээ {year}")
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	if _, err := f.m.UpdateTemplate(ctx, f.tenantID, tpl.ID, "Хүчингүй загвар", "CONTRACT", "Гэрээ {year}", false); err != nil {
		t.Fatalf("retire: %v", err)
	}

	_, err = f.m.CreateDocumentFromTemplate(ctx, f.tenantID, tpl.ID)
	if !errors.Is(err, ErrTemplateRetired) {
		t.Fatalf("got %v, want ErrTemplateRetired", err)
	}
	if errors.Is(err, ErrTemplateNotFound) {
		t.Error("a retired template must not be reported as missing")
	}

	// And a template this tenant really does not hold is still missing.
	if _, err := f.m.CreateDocumentFromTemplate(ctx, f.tenantID,
		"3f1b9c62-2f1a-4a1c-9d3e-8b7a5c4e1d20"); !errors.Is(err, ErrTemplateNotFound) {
		t.Errorf("got %v, want ErrTemplateNotFound", err)
	}
}

// A signature that fills no step is parked past the END of the chain, not at its
// size. On a hand-numbered chain the two are different numbers, and using the size
// writes the parked signature on top of a real step — the trail would then show
// somebody the chain never named as having given that step's approval.
func TestASignatureThatFillsNoStepIsParkedPastTheChain(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	for order, reg := range map[int]string{2: "AA90010111", 3: "BB90010111"} {
		if _, err := f.m.db.Exec(ctx,
			`INSERT INTO document_workflow_steps (tenant_id, doc_type, step_order, name, signer_reg_number)
			      VALUES ($1, 'CONTRACT', $2, 'Шат', $3)`,
			f.tenantID, order, reg); err != nil {
			t.Fatalf("insert step %d: %v", order, err)
		}
	}
	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Цоорхойтой, нэмэлттэй", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Stand in for what migration 00017 leaves on a document that held a parked
	// signature: it owes one approval more than its chain has steps.
	if _, err := f.m.db.Exec(ctx,
		`UPDATE document_records SET required_signatures = 3 WHERE id = $1`, doc.ID); err != nil {
		t.Fatalf("pin the requirement: %v", err)
	}

	if _, err := signWithEID(t, f, doc.ID, "AA90010111"); err != nil {
		t.Fatalf("step 2's citizen: %v", err)
	}
	if _, err := signWithEID(t, f, doc.ID, "BB90010111"); err != nil {
		t.Fatalf("step 3's citizen: %v", err)
	}
	// Every step is filled, and the document still owes one. Whoever gives it fills
	// no step, so their signature must not be written onto one.
	done, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "ZZ99999999", "123456")
	if err != nil {
		t.Fatalf("the extra approval: %v", err)
	}
	if done.Status != StatusApproved {
		t.Errorf("status = %q, want %q", done.Status, StatusApproved)
	}

	sigs, err := f.m.ListSignatures(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("signatures: %v", err)
	}
	held := map[int]string{}
	for _, sig := range sigs {
		if first, clash := held[sig.StepOrder]; clash {
			t.Errorf("step %d holds both %s and %s — one approval, two signers",
				sig.StepOrder, first, sig.SignerRegNumber)
		}
		held[sig.StepOrder] = sig.SignerRegNumber
	}
	if held[2] != "AA90010111" || held[3] != "BB90010111" {
		t.Errorf("the chain's steps = %v, want each filled by the citizen it names", held)
	}
	if held[4] != "ZZ99999999" {
		t.Errorf("the parked signature sits at %v, want step 4 — past the chain's end", held)
	}
}

// The table itself refuses two signatures on one approval now. The numbering that
// could produce them is fixed in both places it lived, but a constraint is worth more
// than either fix: it turns a numbering mistake into a failed write, before anything
// is attributed to the wrong person, rather than a ledger that reads plausibly and
// credits a named step to a stranger.
func TestTheLedgerRefusesTwoSignaturesOnOneApproval(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// A chain of two, so the document is still waiting after the first signature —
	// otherwise it is APPROVED and every later attempt is refused for that reason
	// instead, before the ledger is reached at all.
	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{
		{Order: 1, Name: "Ня-бо", SignerRegNumber: ""},
		{Order: 2, Name: "Захирал", SignerRegNumber: ""},
	}); err != nil {
		t.Fatalf("chain: %v", err)
	}
	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Хоёр гарын үсэг, нэг батламж", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "AA90010111", "123456"); err != nil {
		t.Fatalf("first signature: %v", err)
	}

	var step int
	if err := f.m.db.QueryRow(ctx,
		`SELECT step_order FROM document_signatures WHERE document_id = $1
		  ORDER BY step_order LIMIT 1`, doc.ID).Scan(&step); err != nil {
		t.Fatalf("read the step it filled: %v", err)
	}

	// Straight at the table, the way a numbering mistake would arrive.
	_, err = f.m.db.Exec(ctx,
		`INSERT INTO document_signatures
		        (tenant_id, document_id, signer_name, signer_reg_number, signer_method,
		         signature_hash, signed_at, step_order)
		 VALUES ($1, $2, 'Хэн нэгэн', 'ZZ99999999', 'DAN', 'h', NOW(), $3)`,
		f.tenantID, doc.ID, step)
	if err == nil {
		t.Fatal("the ledger accepted a second signature on one approval")
	}
	if !isConstraintViolation(err, "document_signatures_one_per_approval") {
		t.Errorf("got %v, want the one-per-approval constraint", err)
	}

	// And the message the module gives its caller depends on WHICH constraint was
	// hit: only the one-per-signer constraint means "you have already signed".
	if _, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "AA90010111", "123456"); !errors.Is(err, ErrAlreadySigned) {
		t.Errorf("the same citizen twice: got %v, want ErrAlreadySigned", err)
	}
}

// eID states no deadline for a push session — it decides when one dies and says so
// with EXPIRED. Inventing one here would be a cutoff of our own making: a citizen who
// takes longer than it to unlock a phone, find the notification and enter a PIN would
// be told the request expired while eID was still waiting for them. Which is exactly
// what the sign-in flow was doing until it stopped inventing one.
func TestASessionWithNoStatedDeadlineIsNotExpiredEarly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Хугацаа хэлээгүй", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	session, err := f.m.StartEIDSignature(ctx, f.tenantID, doc.ID, "AA90010111")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// What a live push session looks like once stored: no deadline, and the citizen
	// has been looking for their phone for six minutes — well past the two the old
	// code invented, and well inside anything eID would call expired.
	if _, err := f.m.db.Exec(ctx,
		`UPDATE document_eid_sign_sessions
		    SET expires_at = NULL, created_at = NOW() - INTERVAL '6 minutes'
		  WHERE session_id = $1`, session.SessionID); err != nil {
		t.Fatalf("age the session: %v", err)
	}

	progress, err := f.m.PollEIDSignature(ctx, f.tenantID, doc.ID, session.SessionID)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if progress.State == ApprovalExpired {
		t.Error("a session eID never put a deadline on was called expired")
	}

	// The backstop still holds: an approval id from an hour ago is not redeemable.
	if _, err := f.m.db.Exec(ctx,
		`UPDATE document_eid_sign_sessions
		    SET created_at = NOW() - INTERVAL '1 hour'
		  WHERE session_id = $1`, session.SessionID); err != nil {
		t.Fatalf("age it further: %v", err)
	}
	progress, err = f.m.PollEIDSignature(ctx, f.tenantID, doc.ID, session.SessionID)
	if err != nil {
		t.Fatalf("poll again: %v", err)
	}
	if progress.State != ApprovalExpired {
		t.Errorf("state = %q, want %q past the backstop", progress.State, ApprovalExpired)
	}

	// And a deadline eID DID state is still honoured, with the grace on top.
	second, err := f.m.StartEIDSignature(ctx, f.tenantID, doc.ID, "AA90010111")
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if _, err := f.m.db.Exec(ctx,
		`UPDATE document_eid_sign_sessions
		    SET expires_at = NOW() - INTERVAL '10 minutes'
		  WHERE session_id = $1`, second.SessionID); err != nil {
		t.Fatalf("expire the second: %v", err)
	}
	progress, err = f.m.PollEIDSignature(ctx, f.tenantID, doc.ID, second.SessionID)
	if err != nil {
		t.Fatalf("poll the second: %v", err)
	}
	if progress.State != ApprovalExpired {
		t.Errorf("state = %q, want %q — eID's own deadline is past", progress.State, ApprovalExpired)
	}
}

// A poll that finds the citizen has already signed asks WHICH session produced that
// signature. If it was this one — two tabs, or a retry that overlapped its predecessor,
// both queued on the document's row lock — the ceremony succeeded and the answer is the
// document. If the citizen signed by another route, this session produced nothing and
// the refusal is the truth. This test pins the second half: the first is only reachable
// in a real race, and was verified with four concurrent polls answering COMPLETE over
// one signature.
func TestAPollIsNotToldTheCeremonySucceededWhenItDidNot(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{
		{Order: 1, Name: "Нэг", SignerRegNumber: ""},
		{Order: 2, Name: "Хоёр", SignerRegNumber: ""},
	}); err != nil {
		t.Fatalf("chain: %v", err)
	}
	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Хоёр суваг", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The citizen is asked through eID...
	session, err := f.m.StartEIDSignature(ctx, f.tenantID, doc.ID, "AA90010111")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// ...and signs through DAN instead, so this session produces nothing.
	if _, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "AA90010111", "123456"); err != nil {
		t.Fatalf("DAN: %v", err)
	}

	// Polled until the citizen has actually approved on their device, or the poll only
	// ever reports RUNNING and decides nothing.
	_, err = pollUntilApproved(t, f, doc.ID, session.SessionID)
	if !errors.Is(err, ErrAlreadySigned) {
		t.Fatalf("got %v, want ErrAlreadySigned — this session signed nothing", err)
	}

	// And it stays unspent, so nothing can later claim it produced a signature.
	var consumed bool
	if err := f.m.db.QueryRow(ctx,
		`SELECT consumed_at IS NOT NULL FROM document_eid_sign_sessions WHERE session_id = $1`,
		session.SessionID).Scan(&consumed); err != nil {
		t.Fatalf("read the session: %v", err)
	}
	if consumed {
		t.Error("a session that produced no signature was marked spent")
	}
}

// The list is one page of a tenant's documents, and it says how many there are. Each
// row counts its own signatures and outstanding steps, so an unbounded list was
// expensive as well as unreadable — twenty thousand documents came to 5.9 MB on every
// page load — and a screen that shows the newest two hundred of four thousand without
// saying so is one an operator searches in vain.
func TestTheListIsOnePageAndSaysHowManyThereAre(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Straight into the table: the fixture's own signing paths are not what is under
	// test here, and 250 ceremonies would take minutes.
	for i := 0; i < 250; i++ {
		status := StatusPending
		if i%2 == 0 {
			status = StatusApproved
		}
		if _, err := f.m.db.Exec(ctx,
			`INSERT INTO document_records (tenant_id, title, doc_type, status, created_at)
			      VALUES ($1, $2, 'CONTRACT', $3, NOW() - make_interval(mins => $4))`,
			f.tenantID, fmt.Sprintf("Багц %03d", i), status, i); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	page, err := f.m.ListDocuments(ctx, f.tenantID, DocumentFilter{}, 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Documents) != ListLimitDefault {
		t.Errorf("got %d rows, want the default limit of %d", len(page.Documents), ListLimitDefault)
	}
	if page.Total != 250 {
		t.Errorf("total = %d, want 250 — the screen has to be able to say what it is not showing", page.Total)
	}
	if page.Documents[0].Title != "Багц 000" {
		t.Errorf("first row = %q, want the newest", page.Documents[0].Title)
	}

	// A caller cannot ask for the whole table by hand.
	page, err = f.m.ListDocuments(ctx, f.tenantID, DocumentFilter{}, 10_000, 0)
	if err != nil {
		t.Fatalf("list with a large limit: %v", err)
	}
	if page.Limit != ListLimitMax {
		t.Errorf("limit = %d, want it clamped to %d", page.Limit, ListLimitMax)
	}
	if len(page.Documents) != 250 {
		t.Errorf("got %d rows, want all 250 — the clamp is above what this tenant holds", len(page.Documents))
	}

	// The next page continues where the first left off.
	page, err = f.m.ListDocuments(ctx, f.tenantID, DocumentFilter{}, 100, 100)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(page.Documents) != 100 || page.Documents[0].Title != "Багц 100" {
		t.Errorf("second page starts at %q with %d rows", page.Documents[0].Title, len(page.Documents))
	}

	// The queue asks the server for what is waiting, so a pending document cannot fall
	// off the end of a page full of approved ones.
	page, err = f.m.ListDocuments(ctx, f.tenantID, DocumentFilter{Status: StatusPending}, 0, 0)
	if err != nil {
		t.Fatalf("pending page: %v", err)
	}
	if page.Total != 125 {
		t.Errorf("pending total = %d, want 125", page.Total)
	}
	for _, doc := range page.Documents {
		if doc.Status != StatusPending {
			t.Fatalf("the pending page carries a %s document", doc.Status)
		}
	}

	// Oldest first, which is how a queue is worked — and the reason it matters: with
	// the newest first the document that has waited longest is on the LAST page, so a
	// screen showing one page would report the longest wait as whatever it happened to
	// be holding.
	page, err = f.m.ListDocuments(ctx, f.tenantID, DocumentFilter{Status: StatusPending, Order: ListOrderOldest}, 10, 0)
	if err != nil {
		t.Fatalf("oldest first: %v", err)
	}
	if len(page.Documents) != 10 || page.Documents[0].Title != "Багц 249" {
		t.Errorf("oldest page starts at %q, want the oldest document", page.Documents[0].Title)
	}

	// A status or type nothing can hold is refused rather than answered with an empty
	// page: a screen cannot tell "nothing matches" from "you asked for something that
	// does not exist".
	if _, err := f.m.ListDocuments(ctx, f.tenantID, DocumentFilter{Status: "WHATEVER"}, 0, 0); !errors.Is(err, ErrInvalidDocument) {
		t.Errorf("unknown status: got %v, want ErrInvalidDocument", err)
	}
	if _, err := f.m.ListDocuments(ctx, f.tenantID, DocumentFilter{DocType: "WHATEVER"}, 0, 0); !errors.Is(err, ErrInvalidDocument) {
		t.Errorf("unknown type: got %v, want ErrInvalidDocument", err)
	}

	// Paging is not a way to FIND a document, so the list can be searched. The search is
	// a substring of the title, case-insensitive.
	page, err = f.m.ListDocuments(ctx, f.tenantID, DocumentFilter{Search: "багц 04"}, 0, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if page.Total != 10 {
		t.Errorf("search total = %d, want the ten of Багц 040..049", page.Total)
	}

	// A title holding a LIKE wildcard is searched for literally, not as a pattern.
	if _, err := f.m.db.Exec(ctx,
		`INSERT INTO document_records (tenant_id, title, doc_type, status)
		      VALUES ($1, '100% гэрээ', 'CONTRACT', $2)`, f.tenantID, StatusPending); err != nil {
		t.Fatalf("insert the awkward title: %v", err)
	}
	page, err = f.m.ListDocuments(ctx, f.tenantID, DocumentFilter{Search: "100%"}, 0, 0)
	if err != nil {
		t.Fatalf("search for a percent sign: %v", err)
	}
	if page.Total != 1 {
		t.Errorf("searching for \"100%%\" matched %d, want the one document called that", page.Total)
	}
	page, err = f.m.ListDocuments(ctx, f.tenantID, DocumentFilter{Search: "%"}, 0, 0)
	if err != nil {
		t.Fatalf("search for a bare percent: %v", err)
	}
	if page.Total != 1 {
		t.Errorf("a bare %% matched %d documents, want the one whose title holds it", page.Total)
	}

	// The filters compose, and a search that matches nothing is an empty page rather
	// than an error.
	page, err = f.m.ListDocuments(ctx, f.tenantID,
		DocumentFilter{Status: StatusApproved, DocType: "CONTRACT", Search: "багц 04"}, 0, 0)
	if err != nil {
		t.Fatalf("composed filter: %v", err)
	}
	if page.Total != 5 {
		t.Errorf("approved of Багц 040..049 = %d, want 5", page.Total)
	}
	page, err = f.m.ListDocuments(ctx, f.tenantID, DocumentFilter{Search: "юу ч биш"}, 0, 0)
	if err != nil {
		t.Fatalf("a search that matches nothing: %v", err)
	}
	if page.Total != 0 || len(page.Documents) != 0 {
		t.Errorf("got %d of %d, want an empty page", len(page.Documents), page.Total)
	}
}

// A session that produced a signature is never deleted and never reported expired,
// even when the poll that looks at it arrives past the deadline. The two used to be
// possible together: one poll reading the row as expired while another, a second ahead
// of it, was committing the signature and the spending of that same row.
func TestAnExpiredPollDoesNotBuryACompletedCeremony(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Хугацаа ба гарын үсэг", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	session, err := f.m.StartEIDSignature(ctx, f.tenantID, doc.ID, "AA90010111")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	signed, err := pollUntilApproved(t, f, doc.ID, session.SessionID)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if signed.Status != StatusApproved {
		t.Fatalf("status = %q, want %q", signed.Status, StatusApproved)
	}

	// Now age the spent session past every deadline and poll it again, which is what a
	// straggler does.
	if _, err := f.m.db.Exec(ctx,
		`UPDATE document_eid_sign_sessions
		    SET created_at = NOW() - INTERVAL '2 hours', expires_at = NOW() - INTERVAL '2 hours'
		  WHERE session_id = $1`, session.SessionID); err != nil {
		t.Fatalf("age the session: %v", err)
	}

	progress, err := f.m.PollEIDSignature(ctx, f.tenantID, doc.ID, session.SessionID)
	if err != nil {
		t.Fatalf("poll the aged session: %v", err)
	}
	if progress.State != ApprovalComplete {
		t.Errorf("state = %q, want %q — this session produced the signature", progress.State, ApprovalComplete)
	}

	// And the row survives, because it is the record that this ceremony happened.
	var alive bool
	if err := f.m.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM document_eid_sign_sessions WHERE session_id = $1)`,
		session.SessionID).Scan(&alive); err != nil {
		t.Fatalf("check the row: %v", err)
	}
	if !alive {
		t.Error("the session that produced the signature was deleted as expired")
	}

	// A session that produced nothing is still cleared away and reported expired.
	other, err := f.m.CreateDocument(ctx, f.tenantID, "Хэн ч зураагүй", "CONTRACT")
	if err != nil {
		t.Fatalf("create the second: %v", err)
	}
	unused, err := f.m.StartEIDSignature(ctx, f.tenantID, other.ID, "BB90010111")
	if err != nil {
		t.Fatalf("start the second: %v", err)
	}
	if _, err := f.m.db.Exec(ctx,
		`UPDATE document_eid_sign_sessions SET expires_at = NOW() - INTERVAL '2 hours'
		  WHERE session_id = $1`, unused.SessionID); err != nil {
		t.Fatalf("expire the second: %v", err)
	}
	progress, err = f.m.PollEIDSignature(ctx, f.tenantID, other.ID, unused.SessionID)
	if err != nil {
		t.Fatalf("poll the unused session: %v", err)
	}
	if progress.State != ApprovalExpired {
		t.Errorf("state = %q, want %q", progress.State, ApprovalExpired)
	}
	if err := f.m.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM document_eid_sign_sessions WHERE session_id = $1)`,
		unused.SessionID).Scan(&alive); err != nil {
		t.Fatalf("check the second row: %v", err)
	}
	if alive {
		t.Error("an expired session that produced nothing was left behind")
	}
}

// A search is for a substring of the title, so the characters Postgres reads as
// wildcards must not act as wildcards: somebody typing "100%" is looking for the
// document called that, not for every document. The escape character is declared in the
// query rather than assumed, so this cannot be broken by a setting somewhere else — and
// it is itself searchable.
func TestASearchIsForWhatWasTypedAndNothingElse(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	for _, title := range []string{"100% гэрээ", "plain гэрээ", `back\slash гэрээ`, "a_b гэрээ", "яах вэ! гэрээ"} {
		if _, err := f.m.db.Exec(ctx,
			`INSERT INTO document_records (tenant_id, title, doc_type, status)
			      VALUES ($1, $2, 'CONTRACT', $3)`, f.tenantID, title, StatusPending); err != nil {
			t.Fatalf("insert %q: %v", title, err)
		}
	}

	for _, tc := range []struct {
		search string
		want   string
	}{
		{"100%", "100% гэрээ"},
		{"%", "100% гэрээ"},
		{"_", "a_b гэрээ"},
		{"a_b", "a_b гэрээ"},
		{`back\slash`, `back\slash гэрээ`},
		{"!", "яах вэ! гэрээ"},
		{"яах вэ!", "яах вэ! гэрээ"},
		// Case-insensitive — and this row is the one that matters for Mongolian. It
		// passes on any cluster, but it is only DECISIVE on one initialised with
		// LC_CTYPE=C (what postgres:16-alpine produces), where plain ILIKE folds Latin
		// and leaves Cyrillic alone: without the ICU collation titleMatch asks for, this
		// search matches nothing at all there.
		{"ГЭРЭЭ", ""},
	} {
		page, err := f.m.ListDocuments(ctx, f.tenantID, DocumentFilter{Search: tc.search}, 0, 0)
		if err != nil {
			t.Fatalf("search %q: %v", tc.search, err)
		}
		if tc.want == "" {
			if page.Total != 5 {
				t.Errorf("search %q matched %d, want all five", tc.search, page.Total)
			}
			continue
		}
		if page.Total != 1 {
			t.Errorf("search %q matched %d documents, want only %q", tc.search, page.Total, tc.want)
			continue
		}
		if page.Documents[0].Title != tc.want {
			t.Errorf("search %q matched %q, want %q", tc.search, page.Documents[0].Title, tc.want)
		}
	}

	// And a search longer than a title could ever be is refused rather than run.
	if _, err := f.m.ListDocuments(ctx, f.tenantID,
		DocumentFilter{Search: strings.Repeat("х", TitleLimit+1)}, 0, 0); !errors.Is(err, ErrInvalidDocument) {
		t.Errorf("an over-long search: got %v, want ErrInvalidDocument", err)
	}
}

// The search's case-insensitivity for Cyrillic rests on the database being able to fold
// it, one way or the other. CI runs against the Debian postgres image, whose UTF-8 ctype
// folds it directly, so a regression that only shows on a C-ctype cluster — which is what
// the postgres:16-alpine image this stack deploys produces — would pass there unnoticed.
//
// So this asserts the assumption rather than the behaviour: whichever database the suite
// is pointed at, EITHER its ctype folds Cyrillic or an ICU collation is available for
// titleMatch to lean on. A cluster where neither is true is one where a Mongolian
// operator cannot search, and it should say so here rather than in production.
func TestThisDatabaseCanFoldCyrillicOneWayOrTheOther(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	var ctypeFolds, icu bool
	if err := f.m.db.QueryRow(ctx,
		`SELECT 'ГЭРЭЭ 2026' ILIKE '%гэрээ%',
		        EXISTS (SELECT 1 FROM pg_collation WHERE collname = 'und-x-icu')`).Scan(&ctypeFolds, &icu); err != nil {
		t.Fatalf("ask the database: %v", err)
	}
	if !ctypeFolds && !icu {
		var ctype string
		_ = f.m.db.QueryRow(ctx,
			`SELECT datctype FROM pg_database WHERE datname = current_database()`).Scan(&ctype)
		t.Fatalf("this database (ctype %q) folds neither: ILIKE leaves Cyrillic alone and "+
			"there is no ICU collation to use instead, so a Mongolian title search cannot be "+
			"case-insensitive here", ctype)
	}
	t.Logf("ctype folds Cyrillic: %v; ICU collation available: %v", ctypeFolds, icu)
}

// Offset counts from the start of a set other people are changing. Approve one document
// between two requests and the rows shift up, so the next request skips one — and a
// skipped document is on no screen at all, which on the approval queue is a signature
// nobody can give. A cursor names a place instead of a distance.
func TestACursorDoesNotSkipWhatMovesUnderIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Six documents, oldest first, one minute apart so the order is unambiguous.
	for i := 0; i < 6; i++ {
		if _, err := f.m.db.Exec(ctx,
			`INSERT INTO document_records (tenant_id, title, doc_type, status, created_at)
			      VALUES ($1, $2, 'CONTRACT', $3, NOW() - make_interval(mins => $4))`,
			f.tenantID, fmt.Sprintf("Мөр %d", i), StatusPending, 10-i); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	oldest := DocumentFilter{Status: StatusPending, Order: ListOrderOldest}
	first, err := f.m.ListDocuments(ctx, f.tenantID, oldest, 3, 0)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Documents) != 3 || first.Documents[0].Title != "Мөр 0" {
		t.Fatalf("first page = %+v", first.Documents)
	}
	last := first.Documents[2]

	// Somebody approves a document from the FIRST page, so everything after it shifts
	// up by one. This is what breaks offset.
	if _, err := f.m.db.Exec(ctx,
		`UPDATE document_records SET status = $1 WHERE tenant_id = $2 AND title = 'Мөр 1'`,
		StatusApproved, f.tenantID); err != nil {
		t.Fatalf("approve one: %v", err)
	}

	// Offset would now skip: rows 4..6 of a set that lost one are 5 and 6.
	byOffset, err := f.m.ListDocuments(ctx, f.tenantID, oldest, 3, 3)
	if err != nil {
		t.Fatalf("offset page: %v", err)
	}
	skipped := true
	for _, doc := range byOffset.Documents {
		if doc.Title == "Мөр 3" {
			skipped = false
		}
	}
	if !skipped {
		t.Log("offset happened not to skip here; the cursor assertion below is what matters")
	}

	// The cursor continues from the row actually seen, so nothing between here and
	// there can move it.
	after := oldest
	after.AfterAt, after.AfterID = last.CreatedAt, last.ID
	byCursor, err := f.m.ListDocuments(ctx, f.tenantID, after, 3, 0)
	if err != nil {
		t.Fatalf("cursor page: %v", err)
	}
	got := []string{}
	for _, doc := range byCursor.Documents {
		got = append(got, doc.Title)
	}
	want := []string{"Мөр 3", "Мөр 4", "Мөр 5"}
	if len(got) != len(want) {
		t.Fatalf("cursor page = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cursor page = %v, want %v", got, want)
			break
		}
	}

	// Newest-first reads the other way from the same place.
	newest, err := f.m.ListDocuments(ctx, f.tenantID, DocumentFilter{Status: StatusPending}, 2, 0)
	if err != nil {
		t.Fatalf("newest page: %v", err)
	}
	if newest.Documents[0].Title != "Мөр 5" {
		t.Fatalf("newest page starts at %q", newest.Documents[0].Title)
	}
	onwards := DocumentFilter{Status: StatusPending}
	onwards.AfterAt, onwards.AfterID = newest.Documents[1].CreatedAt, newest.Documents[1].ID
	tail, err := f.m.ListDocuments(ctx, f.tenantID, onwards, 5, 0)
	if err != nil {
		t.Fatalf("newest tail: %v", err)
	}
	if len(tail.Documents) == 0 || tail.Documents[0].Title != "Мөр 3" {
		t.Errorf("newest tail = %+v, want it to continue at Мөр 3", tail.Documents)
	}

	// An id that is not an id is refused rather than paged from nowhere.
	bad := oldest
	bad.AfterID = "not-a-uuid"
	if _, err := f.m.ListDocuments(ctx, f.tenantID, bad, 3, 0); !errors.Is(err, ErrInvalidDocument) {
		t.Errorf("got %v, want ErrInvalidDocument", err)
	}
}

// A channel that is not there and a citizen who was refused are opposite answers: one
// is nobody's fault at this end and may be retried, the other is the caller's and must
// not be. DAN has no live client in this platform, so on any deployment with
// DAN_MOCK_MODE off — which is every production one — signing through it told the
// operator their signature had been REJECTED. It had not: the gateway was never reached.
func TestAnUnreachableDANIsProviderTroubleNotARejection(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "ДАН байхгүй", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The same module, against a DAN service in the shape production runs it: no mock,
	// so no gateway.
	// A ДАН in the shape production runs it: no gateway to reach.
	live := &DocumentsModule{db: f.m.db, eidSvc: f.m.eidSvc, danSvc: fakeDAN{reachable: false}}

	_, err = live.SignWithDAN(ctx, f.tenantID, doc.ID, "AA90010111", "123456")
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("got %v, want ErrProviderUnavailable — the gateway was never reached", err)
	}
	if errors.Is(err, ErrSignatureRejected) {
		t.Error("an unreachable gateway must not be reported as a refused signature")
	}

	// And nothing was recorded on the strength of a call that never happened.
	sigs, err := f.m.ListSignatures(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("signatures: %v", err)
	}
	if len(sigs) != 0 {
		t.Errorf("the ledger holds %d signatures after an unreachable gateway", len(sigs))
	}
}

// Asking the citizen again ends the previous asking. Every start pushes a prompt to
// their phone and leaves a row that can be turned into a signature; without this, an
// operator retrying five times left five live approval ids for one document, any of
// which could still be redeemed later.
func TestStartingAgainRetiresTheLastAsking(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Дахин дахин асуув", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	var ids []string
	for i := 0; i < 5; i++ {
		session, err := f.m.StartEIDSignature(ctx, f.tenantID, doc.ID, "AA90010111")
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		ids = append(ids, session.SessionID)
	}

	var live int
	if err := f.m.db.QueryRow(ctx,
		`SELECT count(*) FROM document_eid_sign_sessions
		  WHERE document_id = $1 AND consumed_at IS NULL`, doc.ID).Scan(&live); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if live != 1 {
		t.Errorf("%d live sessions after five askings, want 1", live)
	}

	// An earlier asking is no longer redeemable...
	if _, err := f.m.PollEIDSignature(ctx, f.tenantID, doc.ID, ids[0]); !errors.Is(err, ErrSignSessionUnknown) {
		t.Errorf("polling a retired session: got %v, want ErrSignSessionUnknown", err)
	}
	// ...and the newest one still signs.
	signed, err := pollUntilApproved(t, f, doc.ID, ids[len(ids)-1])
	if err != nil {
		t.Fatalf("the newest session: %v", err)
	}
	if signed.SignatureCount != 1 {
		t.Errorf("count = %d, want 1", signed.SignatureCount)
	}

	// A session for a DIFFERENT citizen on the same document is untouched: they were
	// each asked, and each of them may answer.
	other, err := f.m.CreateDocument(ctx, f.tenantID, "Хоёр хүн", "CONTRACT")
	if err != nil {
		t.Fatalf("create the second: %v", err)
	}
	first, err := f.m.StartEIDSignature(ctx, f.tenantID, other.ID, "AA90010111")
	if err != nil {
		t.Fatalf("ask the first citizen: %v", err)
	}
	if _, err := f.m.StartEIDSignature(ctx, f.tenantID, other.ID, "BB90010111"); err != nil {
		t.Fatalf("ask the second citizen: %v", err)
	}
	if err := f.m.db.QueryRow(ctx,
		`SELECT count(*) FROM document_eid_sign_sessions
		  WHERE document_id = $1 AND consumed_at IS NULL`, other.ID).Scan(&live); err != nil {
		t.Fatalf("count: %v", err)
	}
	if live != 2 {
		t.Errorf("%d live sessions for two citizens, want 2", live)
	}
	if _, err := f.m.PollEIDSignature(ctx, f.tenantID, other.ID, first.SessionID); errors.Is(err, ErrSignSessionUnknown) {
		t.Error("asking a second citizen retired the first citizen's request")
	}
}

// Postgres text holds neither a NUL character nor a byte sequence that is not UTF-8, so
// the driver refuses a value carrying either. Every field in this module used to answer
// that with 500 "we could not save it", which is untrue twice: nothing broke, and the
// input is the caller's. Each entry point now says which field and why.
func TestTextThatCannotBeStoredIsRefusedNotBlamedOnUs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Хадгалагдах текст", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	const nul = "a\x00b"
	badUTF8 := string([]byte{0x41, 0xff, 0xfe})

	for _, tc := range []struct {
		what string
		run  func(string) error
		want error
	}{
		{"document title", func(v string) error {
			_, err := f.m.CreateDocument(ctx, f.tenantID, v, "CONTRACT")
			return err
		}, ErrInvalidDocument},
		{"search", func(v string) error {
			_, err := f.m.ListDocuments(ctx, f.tenantID, DocumentFilter{Search: v}, 0, 0)
			return err
		}, ErrInvalidDocument},
		{"template name", func(v string) error {
			_, err := f.m.CreateTemplate(ctx, f.tenantID, v, "CONTRACT", "x")
			return err
		}, ErrInvalidConfiguration},
		{"title pattern", func(v string) error {
			_, err := f.m.CreateTemplate(ctx, f.tenantID, "нэр", "CONTRACT", v)
			return err
		}, ErrInvalidConfiguration},
		{"step name", func(v string) error {
			_, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "APPROVAL", []WorkflowStep{{Order: 1, Name: v}})
			return err
		}, ErrInvalidConfiguration},
		{"step signer", func(v string) error {
			_, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "APPROVAL",
				[]WorkflowStep{{Order: 1, Name: "ok", SignerRegNumber: "AA9001" + v}})
			return err
		}, ErrInvalidConfiguration},
		{"retention note", func(v string) error {
			_, err := f.m.SaveRetentionRule(ctx, f.tenantID, "APPROVAL", 3, v)
			return err
		}, ErrInvalidConfiguration},
		{"eID reg number", func(v string) error {
			_, err := f.m.StartEIDSignature(ctx, f.tenantID, doc.ID, "AA9001"+v)
			return err
		}, ErrSignatureRejected},
		{"DAN reg number", func(v string) error {
			_, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "AA9001"+v, "123456")
			return err
		}, ErrSignatureRejected},
	} {
		for _, value := range []string{nul, badUTF8} {
			err := tc.run(value)
			if !errors.Is(err, tc.want) {
				t.Errorf("%s with %q: got %v, want %v", tc.what, value, err, tc.want)
			}
		}
	}
}

// An absent field and an empty one mean opposite things here, and one of them is
// destructive: an empty array is a tenant saying "this type needs no chain", while a
// body that does not carry the field is a client that has gone wrong. Both used to
// clear the chain — and clearing it LOOSENS the type, since a type with no chain is
// approved by one open signature, so a malformed request could quietly turn a
// three-approver contract into a one-signature one.
func TestAnAbsentChainIsNotAnEmptyChain(t *testing.T) {
	f := newFixture(t)
	ctx := nexus.WithTenantID(context.Background(), f.tenantID)

	if _, err := f.m.ReplaceWorkflow(context.Background(), f.tenantID, "CONTRACT", []WorkflowStep{
		{Order: 1, Name: "Ня-бо", SignerRegNumber: "УБ99010111"},
		{Order: 2, Name: "Захирал", SignerRegNumber: ""},
	}); err != nil {
		t.Fatalf("chain: %v", err)
	}

	route := chi.NewRouter()
	route.Put("/{docType}", f.m.saveWorkflowHandler)
	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/CONTRACT", strings.NewReader(body)).WithContext(ctx)
		rec := httptest.NewRecorder()
		route.ServeHTTP(rec, req)
		return rec
	}
	steps := func() int {
		chain, err := f.m.workflowStepsTx(context.Background(), f.m.db, f.tenantID, "CONTRACT")
		if err != nil {
			t.Fatalf("read the chain: %v", err)
		}
		return len(chain)
	}

	for _, body := range []string{`{}`, `{"steps":null}`, `{"nope":[]}`} {
		if rec := put(body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400 (%s)", body, rec.Code, rec.Body.String())
		}
	}
	if n := steps(); n != 2 {
		t.Errorf("the chain has %d steps after three refused requests, want the 2 it had", n)
	}

	// An explicit empty array is a real instruction and is obeyed.
	if rec := put(`{"steps":[]}`); rec.Code != http.StatusOK {
		t.Fatalf("an explicit empty chain: got %d (%s)", rec.Code, rec.Body.String())
	}
	if n := steps(); n != 0 {
		t.Errorf("the chain has %d steps after being cleared on purpose", n)
	}
}

// A document goes straight into the approval queue when it is created, which is one
// step for an operator rather than two and leaves nothing forgotten in a drawer. The
// price of that is a mistyped title reaching the approvers, so a title can be corrected
// — until the first signature. After that it is what the citizen READ on their own
// device before approving, and changing it would make their consent a consent to
// something else.
func TestATitleCanBeCorrectedUntilSomebodyHasSignedIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{
		{Order: 1, Name: "Ня-бо", SignerRegNumber: ""},
		{Order: 2, Name: "Захирал", SignerRegNumber: ""},
	}); err != nil {
		t.Fatalf("chain: %v", err)
	}
	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Хамтарн ажиллах гэрэ 2026", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Nobody has signed, so the typo can go.
	fixed, err := f.m.RenameDocument(ctx, f.tenantID, doc.ID, "  Хамтран ажиллах гэрээ 2026  ")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if fixed.Title != "Хамтран ажиллах гэрээ 2026" {
		t.Errorf("title = %q, want it corrected and trimmed", fixed.Title)
	}

	// A title nothing could store, or that no column could hold, is refused as the
	// caller's mistake rather than saved or blamed on us.
	for _, bad := range []string{"", "   ", strings.Repeat("х", TitleLimit+1), "a\x00b"} {
		if _, err := f.m.RenameDocument(ctx, f.tenantID, doc.ID, bad); !errors.Is(err, ErrInvalidDocument) {
			t.Errorf("rename to %q: got %v, want ErrInvalidDocument", bad, err)
		}
	}

	// One signature, and the title is what that citizen approved.
	if _, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "AA90010111", "123456"); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := f.m.RenameDocument(ctx, f.tenantID, doc.ID, "Өөр гарчиг"); !errors.Is(err, ErrTitleFrozen) {
		t.Fatalf("rename after a signature: got %v, want ErrTitleFrozen", err)
	}
	after, err := f.m.getDocument(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after.Title != "Хамтран ажиллах гэрээ 2026" {
		t.Errorf("the title moved to %q after a refused rename", after.Title)
	}

	// A decided document is history: even with no signature on it, its title stands.
	other, err := f.m.CreateDocument(ctx, f.tenantID, "Татгалзах гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create the second: %v", err)
	}
	if _, err := f.m.RejectDocument(ctx, f.tenantID, other.ID); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if _, err := f.m.RenameDocument(ctx, f.tenantID, other.ID, "Дахиад өөр"); !errors.Is(err, ErrTitleFrozen) {
		t.Errorf("renaming a rejected document: got %v, want ErrTitleFrozen", err)
	}

	// And another tenant cannot rename it at all — the same answer as "signed", so the
	// refusal does not say whether the document exists.
	intruder := newFixture(t)
	if _, err := intruder.m.RenameDocument(ctx, intruder.tenantID, doc.ID, "Хулгайн гарчиг"); !errors.Is(err, ErrTitleFrozen) {
		t.Errorf("cross-tenant rename: got %v, want ErrTitleFrozen", err)
	}
}

// Meeting the signature count is not the same as finishing the chain. A signature
// from somebody no step names counts toward what a document holds and satisfies none
// of what it owes, so the payload has to say how many steps are still outstanding —
// a screen comparing only the two numbers painted "complete" over a document whose
// named approval was still missing.
func TestOutstandingStepsIsWhatCompletionMeans(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{
		{Name: "Хянагч"},                                 // open
		{Name: "Захирал", SignerRegNumber: "AA90010111"}, // named
	}); err != nil {
		t.Fatalf("configure chain: %v", err)
	}

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Хоёр шат", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if doc.OutstandingSteps != 2 {
		t.Fatalf("outstanding = %d, want 2 before anybody signs", doc.OutstandingSteps)
	}

	half, err := f.m.SignWithDAN(ctx, f.tenantID, doc.ID, "ZZ99999999", "123456")
	if err != nil {
		t.Fatalf("open step: %v", err)
	}
	if half.OutstandingSteps != 1 {
		t.Errorf("outstanding = %d, want 1 — the director has not signed", half.OutstandingSteps)
	}
	if half.Status != StatusPending {
		t.Errorf("status = %q, want %q", half.Status, StatusPending)
	}

	done, err := signWithEID(t, f, doc.ID, "AA90010111")
	if err != nil {
		t.Fatalf("the director: %v", err)
	}
	if done.OutstandingSteps != 0 {
		t.Errorf("outstanding = %d, want 0 once every step is filled", done.OutstandingSteps)
	}
	if done.Status != StatusApproved {
		t.Errorf("status = %q, want %q", done.Status, StatusApproved)
	}

	// A document with no chain has no steps to owe, so the count alone decides — the
	// way a type without a chain has always behaved.
	plain, err := f.m.CreateDocument(ctx, f.tenantID, "Хэлхээгүй", "APPROVAL")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if plain.OutstandingSteps != 0 || plain.RequiredSignatures != 1 {
		t.Errorf("no chain: outstanding %d, required %d; want 0 and 1",
			plain.OutstandingSteps, plain.RequiredSignatures)
	}
}

// A chain may not name something no citizen could present, and a template may not
// carry more than its column holds — both are refusals the operator can read rather
// than a Postgres error they cannot.
func TestConfigurationIsBoundedByWhatCanBeStoredAndSigned(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{
		{Name: "Захирал", SignerRegNumber: "AA1"},
	}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Errorf("a step naming AA1: got %v, want ErrInvalidConfiguration", err)
	}

	long := strings.Repeat("Урт ", 100) // 400 runes
	if _, err := f.m.CreateTemplate(ctx, f.tenantID, long, "CONTRACT", "Гэрээ {year}"); !errors.Is(err, ErrInvalidConfiguration) {
		t.Errorf("an over-long template name: got %v, want ErrInvalidConfiguration", err)
	}
	if _, err := f.m.CreateTemplate(ctx, f.tenantID, "Зөв нэр", "CONTRACT", long); !errors.Is(err, ErrInvalidConfiguration) {
		t.Errorf("an over-long title pattern: got %v, want ErrInvalidConfiguration", err)
	}

	tpl, err := f.m.CreateTemplate(ctx, f.tenantID, "Зөв нэр", "CONTRACT", "Гэрээ {year}")
	if err != nil {
		t.Fatalf("a template inside its limits must be accepted: %v", err)
	}
	if _, err := f.m.UpdateTemplate(ctx, f.tenantID, tpl.ID, long, "CONTRACT", "Гэрээ {year}", true); !errors.Is(err, ErrInvalidConfiguration) {
		t.Errorf("an over-long name on update: got %v, want ErrInvalidConfiguration", err)
	}
}

func TestRejectIsFinal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Татгалзах гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	rejected, err := f.m.RejectDocument(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.Status != StatusRejected {
		t.Errorf("status = %q, want %q", rejected.Status, StatusRejected)
	}

	if _, err := f.m.RejectDocument(ctx, f.tenantID, doc.ID); !errors.Is(err, ErrNotSignable) {
		t.Errorf("second reject: got %v, want ErrNotSignable", err)
	}
	if _, err := f.m.StartEIDSignature(ctx, f.tenantID, doc.ID, "AA90010111"); !errors.Is(err, ErrNotSignable) {
		t.Errorf("signing a rejected document: got %v, want ErrNotSignable", err)
	}
}

func TestAnotherTenantsDocumentIsNotSignable(t *testing.T) {
	owner := newFixture(t)
	intruder := newFixture(t)
	ctx := context.Background()

	doc, err := owner.m.CreateDocument(ctx, owner.tenantID, "Бусдын гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := intruder.m.StartEIDSignature(ctx, intruder.tenantID, doc.ID, "AA90010111"); !errors.Is(err, ErrNotSignable) {
		t.Errorf("cross-tenant sign: got %v, want ErrNotSignable", err)
	}
	if _, err := intruder.m.RejectDocument(ctx, intruder.tenantID, doc.ID); !errors.Is(err, ErrNotSignable) {
		t.Errorf("cross-tenant reject: got %v, want ErrNotSignable", err)
	}

	page, err := intruder.m.ListDocuments(ctx, intruder.tenantID, DocumentFilter{}, 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Documents) != 0 || page.Total != 0 {
		t.Errorf("intruder sees %d of %d documents, want none of none", len(page.Documents), page.Total)
	}
}

func TestTemplatesCreateDocumentsAndKeepNamesUnique(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	tpl, err := f.m.CreateTemplate(ctx, f.tenantID, "Гэрээний загвар", "CONTRACT", "Хамтран ажиллах гэрээ {year}")
	if err != nil {
		t.Fatalf("create template: %v", err)
	}

	if _, err := f.m.CreateTemplate(ctx, f.tenantID, "Гэрээний загвар", "REQUEST", "Дахин"); !errors.Is(err, ErrTemplateNameTaken) {
		t.Errorf("duplicate name: got %v, want ErrTemplateNameTaken", err)
	}

	doc, err := f.m.CreateDocumentFromTemplate(ctx, f.tenantID, tpl.ID)
	if err != nil {
		t.Fatalf("use template: %v", err)
	}
	if doc.DocType != "CONTRACT" {
		t.Errorf("doc_type = %q, want CONTRACT", doc.DocType)
	}
	if doc.Title == tpl.TitlePattern {
		t.Errorf("title %q still holds the pattern; {year} was not resolved", doc.Title)
	}
	if doc.Status != StatusPending {
		t.Errorf("a templated document must be routed like any other, got %q", doc.Status)
	}

	// Editing one changes what it will produce next, and retiring it stops it
	// producing anything without destroying the record of what it was.
	renamed, err := f.m.UpdateTemplate(ctx, f.tenantID, tpl.ID, "  Гэрээ 2027  ", "request", "Албан хүсэлт {year}", true)
	if err != nil {
		t.Fatalf("update template: %v", err)
	}
	if renamed.Name != "Гэрээ 2027" || renamed.DocType != "REQUEST" {
		t.Errorf("update = %+v, want the trimmed name and the normalised type", renamed)
	}
	if _, err := f.m.UpdateTemplate(ctx, f.tenantID, tpl.ID, "", "REQUEST", "x", true); !errors.Is(err, ErrInvalidConfiguration) {
		t.Errorf("empty name: got %v, want ErrInvalidConfiguration", err)
	}

	retired, err := f.m.UpdateTemplate(ctx, f.tenantID, tpl.ID, "Гэрээ 2027", "REQUEST", "Албан хүсэлт {year}", false)
	if err != nil {
		t.Fatalf("retire template: %v", err)
	}
	if retired.Active {
		t.Error("the template was not retired")
	}
	// A retired template produces nothing — and says which of the two it is, rather
	// than reporting a row the operator can see as missing.
	if _, err := f.m.CreateDocumentFromTemplate(ctx, f.tenantID, tpl.ID); !errors.Is(err, ErrTemplateRetired) {
		t.Errorf("using a retired template: got %v, want ErrTemplateRetired", err)
	}

	// A name another template already holds is a conflict, not a validation error.
	other, err := f.m.CreateTemplate(ctx, f.tenantID, "Өөр загвар", "CONTRACT", "Өөр {year}")
	if err != nil {
		t.Fatalf("create second template: %v", err)
	}
	if _, err := f.m.UpdateTemplate(ctx, f.tenantID, other.ID, "Гэрээ 2027", "CONTRACT", "Өөр {year}", true); !errors.Is(err, ErrTemplateNameTaken) {
		t.Errorf("duplicate name on update: got %v, want ErrTemplateNameTaken", err)
	}

	if err := f.m.DeleteTemplate(ctx, f.tenantID, tpl.ID); err != nil {
		t.Fatalf("delete template: %v", err)
	}
	if err := f.m.DeleteTemplate(ctx, f.tenantID, tpl.ID); !errors.Is(err, ErrTemplateNotFound) {
		t.Errorf("second delete: got %v, want ErrTemplateNotFound", err)
	}
	// The document the template produced outlives the template.
	if _, err := f.m.getDocument(ctx, f.tenantID, doc.ID); err != nil {
		t.Errorf("document should survive its template: %v", err)
	}
}

func TestRouteMovesADraftAndOnlyADraft(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Nothing the app creates is a draft, so the row is made the way one would
	// actually arrive: straight into the table at the column default.
	var draftID string
	if err := f.m.db.QueryRow(ctx,
		`INSERT INTO document_records (tenant_id, title, doc_type) VALUES ($1, $2, $3) RETURNING id`,
		f.tenantID, "Ноорог гэрээ", "CONTRACT").Scan(&draftID); err != nil {
		t.Fatalf("insert draft: %v", err)
	}

	draft, err := f.m.getDocument(ctx, f.tenantID, draftID)
	if err != nil {
		t.Fatalf("read draft: %v", err)
	}
	if draft.Status != StatusDraft {
		t.Fatalf("status = %q, want the column default %q", draft.Status, StatusDraft)
	}

	// A draft is not signable until it has been routed.
	if _, err := f.m.StartEIDSignature(ctx, f.tenantID, draftID, "AA90010111"); !errors.Is(err, ErrNotSignable) {
		t.Errorf("signing a draft: got %v, want ErrNotSignable", err)
	}

	routed, err := f.m.RouteDocument(ctx, f.tenantID, draftID)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if routed.Status != StatusPending {
		t.Errorf("status = %q, want %q", routed.Status, StatusPending)
	}
	if _, err := f.m.RouteDocument(ctx, f.tenantID, draftID); !errors.Is(err, ErrNotRoutable) {
		t.Errorf("routing twice: got %v, want ErrNotRoutable", err)
	}
	if _, err := signWithEID(t, f, draftID, "AA90010111"); err != nil {
		t.Errorf("a routed draft must be signable: %v", err)
	}
}

func TestRetentionReportsWhatIsPastItsTerm(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.m.CreateDocument(ctx, f.tenantID, "Шинэ гэрээ", "CONTRACT"); err != nil {
		t.Fatalf("create: %v", err)
	}

	var oldID string
	if err := f.m.db.QueryRow(ctx,
		`INSERT INTO document_records (tenant_id, title, doc_type, status, created_at)
		      VALUES ($1, $2, 'CONTRACT', $3, NOW() - INTERVAL '6 years') RETURNING id`,
		f.tenantID, "Хуучин гэрээ", StatusApproved).Scan(&oldID); err != nil {
		t.Fatalf("insert aged document: %v", err)
	}

	if _, err := f.m.SaveRetentionRule(ctx, f.tenantID, "CONTRACT", 5, "Гэрээг 5 жил хадгална"); err != nil {
		t.Fatalf("save rule: %v", err)
	}
	if _, err := f.m.SaveRetentionRule(ctx, f.tenantID, "CONTRACT", 0, ""); err == nil {
		t.Error("expected a zero-year term to be refused")
	}

	rules, err := f.m.ListRetentionRules(ctx, f.tenantID)
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules) != len(DocTypes) {
		t.Fatalf("got %d rows, want one per document type (%d)", len(rules), len(DocTypes))
	}

	for _, rule := range rules {
		// The list always knows its counts; only the save path may leave them absent.
		if rule.Expired == nil || rule.Total == nil {
			t.Errorf("%s: the list must carry both counts, got %+v", rule.DocType, rule)
			continue
		}
		switch rule.DocType {
		case "CONTRACT":
			if !rule.Configured || rule.RetainYears != 5 {
				t.Errorf("CONTRACT rule = %+v, want a stored 5-year term", rule)
			}
			if *rule.Total != 2 {
				t.Errorf("CONTRACT total = %d, want 2", *rule.Total)
			}
			if *rule.Expired != 1 {
				t.Errorf("CONTRACT expired = %d, want 1 (the six-year-old document)", *rule.Expired)
			}
		default:
			if rule.Configured {
				t.Errorf("%s must stay unconfigured", rule.DocType)
			}
			if *rule.Expired != 0 {
				t.Errorf("%s expired = %d, want 0 without a term", rule.DocType, *rule.Expired)
			}
		}
	}

	// A saved rule answers with the counts too, so the screen need not refetch.
	saved, err := f.m.SaveRetentionRule(ctx, f.tenantID, "CONTRACT", 5, "Гэрээг 5 жил хадгална")
	if err != nil {
		t.Fatalf("save again: %v", err)
	}
	if saved.Expired == nil || saved.Total == nil {
		t.Fatalf("the saved rule must carry its counts, got %+v", saved)
	}
	if *saved.Total != 2 || *saved.Expired != 1 {
		t.Errorf("saved counts = %d/%d, want 1 expired of 2", *saved.Expired, *saved.Total)
	}
}

func TestPoliciesAndChainsCoverEveryDocumentType(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	policies, err := f.m.ListSignaturePolicies(ctx, f.tenantID)
	if err != nil {
		t.Fatalf("list policies: %v", err)
	}
	if len(policies) != len(DocTypes) {
		t.Fatalf("got %d policies, want one per document type (%d)", len(policies), len(DocTypes))
	}
	for _, policy := range policies {
		if policy.Configured {
			t.Errorf("%s: a fresh tenant has configured nothing", policy.DocType)
		}
		if !policy.AllowEID || !policy.AllowDAN {
			t.Errorf("%s: an unconfigured type must accept both channels", policy.DocType)
		}
	}

	chains, err := f.m.ListWorkflows(ctx, f.tenantID)
	if err != nil {
		t.Fatalf("list chains: %v", err)
	}
	if len(chains) != len(DocTypes) {
		t.Fatalf("got %d chains, want one per document type (%d)", len(chains), len(DocTypes))
	}
	for _, chain := range chains {
		if len(chain.Steps) != 0 {
			t.Errorf("%s: a fresh tenant has no steps, got %d", chain.DocType, len(chain.Steps))
		}
	}

	// Replacing a chain is a swap, not an append.
	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{
		{Name: "Нэг"}, {Name: "Хоёр"}, {Name: "Гурав"},
	}); err != nil {
		t.Fatalf("configure chain: %v", err)
	}
	replaced, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{{Name: "Зөвхөн нэг"}})
	if err != nil {
		t.Fatalf("replace chain: %v", err)
	}
	if len(replaced.Steps) != 1 || replaced.Steps[0].Order != 1 {
		t.Errorf("chain = %+v, want a single step numbered 1", replaced.Steps)
	}

	// A step with no name is a step nobody can read.
	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{{Name: "  "}}); err == nil {
		t.Error("expected a nameless step to be refused")
	}
}

// A title freezes at the first signature, because the title is what the citizen read on
// their own device before approving. That guard used to live inside the UPDATE itself —
// `AND NOT EXISTS (SELECT 1 FROM document_signatures …)` — which looked airtight and was
// not: under READ COMMITTED an UPDATE that waits on a row lock re-checks its
// qualification against the new version of the row it is updating, but a subquery over
// another table is still evaluated on the statement's original snapshot. The signature
// committing while the rename waited was therefore invisible to it.
//
// A chain of two is what makes it reachable. One signature on a chain of one moves the
// document to APPROVED, and the status half of the guard caught that; on a chain of two
// the document stays PENDING_APPROVAL and nothing caught it at all.
func TestATitleCannotBeChangedByASignatureLandingUnderIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "CONTRACT", []WorkflowStep{
		{Name: "Нэгдүгээр", SignerRegNumber: "AA90010111"},
		{Name: "Хоёрдугаар", SignerRegNumber: "CC90010111"},
	}); err != nil {
		t.Fatalf("configure chain: %v", err)
	}

	const approved = "Иргэний уншсан гарчиг"
	doc, err := f.m.CreateDocument(ctx, f.tenantID, approved, "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// A signing transaction stopped exactly where recordSignature holds one: the
	// document's row locked, the signature written, nothing committed yet.
	tx, err := f.m.db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the signing transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`SELECT id FROM document_records WHERE id = $1 AND tenant_id = $2 FOR UPDATE`,
		doc.ID, f.tenantID); err != nil {
		t.Fatalf("lock the document: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO document_signatures
		        (tenant_id, document_id, signer_name, signer_reg_number,
		         signer_method, signature_hash, step_order)
		 VALUES ($1, $2, 'Иргэн А', 'AA90010111', $3, 'eid_session_rename_race', 1)`,
		f.tenantID, doc.ID, SignerEID); err != nil {
		t.Fatalf("write the signature: %v", err)
	}

	renamed := make(chan error, 1)
	go func() {
		_, err := f.m.RenameDocument(ctx, f.tenantID, doc.ID, "Дараа нь солигдсон гарчиг")
		renamed <- err
	}()

	waitUntilBlocked(t, f)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the signature: %v", err)
	}

	select {
	case err := <-renamed:
		if !errors.Is(err, ErrTitleFrozen) {
			t.Fatalf("renaming a document a signature had just landed on returned %v, want ErrTitleFrozen", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the rename never returned")
	}

	after, err := f.m.getDocument(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if after.Title != approved {
		t.Errorf("title = %q, want %q — the citizen approved the words, not the row", after.Title, approved)
	}
	if after.Status != StatusPending {
		t.Errorf("status = %q, want %q: a chain of two is not complete on one signature",
			after.Status, StatusPending)
	}
}

// waitUntilBlocked waits until another backend in this database is queued on a lock,
// which is how the test above knows the rename has reached the row it must wait for.
// A fixed sleep would be either too short on a loaded CI runner or wasted on a fast one.
func waitUntilBlocked(t *testing.T, f *fixture) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var blocked int
		if err := f.m.db.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity
			  WHERE datname = current_database()
			    AND cardinality(pg_blocking_pids(pid)) > 0`).Scan(&blocked); err != nil {
			t.Fatalf("look for a blocked backend: %v", err)
		}
		if blocked > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("nothing ever queued on the document's lock")
}

// A policy that accepts only a signer the chain names has to decide who may sign, not
// only what may be configured. It used to be read and thrown away: preflightSignature
// loaded the policy and consulted allows() alone, so on a document whose own chain names
// nobody — the ordinary shape for one created before the tenant configured a chain —
// anybody holding documents.sign could still sign it, under a screen saying otherwise.
func TestARequiredNamedSignerDecidesWhoMaySign(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Хуучин гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	steps, err := f.m.DocumentSteps(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("read the document's chain: %v", err)
	}
	if len(steps) != 0 {
		t.Fatalf("the document was given %d steps, want none — this test is about one that names nobody", len(steps))
	}

	// Written straight into the table, the way it would already be stored on a tenant
	// that turned the requirement on before SaveSignaturePolicy started guarding it.
	if _, err := f.m.db.Exec(ctx,
		`INSERT INTO document_signature_policies
		        (tenant_id, doc_type, allow_eid, allow_dan, require_named_signer)
		 VALUES ($1, 'CONTRACT', TRUE, TRUE, TRUE)`, f.tenantID); err != nil {
		t.Fatalf("store the policy: %v", err)
	}

	_, err = signWithEID(t, f, doc.ID, "AA90010111")
	if !errors.Is(err, ErrSignatureRejected) {
		t.Fatalf("a document naming nobody was signed under a named-signer policy: got %v", err)
	}

	// Refused before the citizen was troubled, so nothing was recorded either.
	applied, err := f.m.ListSignatures(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	if len(applied) != 0 {
		t.Errorf("the ledger holds %d signature(s) after a refusal, want none", len(applied))
	}
	after, err := f.m.getDocument(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if after.Status != StatusPending {
		t.Errorf("status = %q, want %q", after.Status, StatusPending)
	}
}

// The other half of that rule. Because a document is held to its own chain and a later
// configuration change never reaches it, turning the requirement on can leave documents
// already in the queue signable by nobody — so it is refused at the moment of the
// decision, the way an unfillable chain is, rather than discovered by a stuck document.
func TestTurningOnANamedSignerRequirementCannotStrandTheQueue(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Waiting under no chain at all: it names nobody.
	stranded, err := f.m.CreateDocument(ctx, f.tenantID, "Хүлээгдэж буй хүсэлт", "REQUEST")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// A chain that would satisfy the requirement for everything created from now on.
	if _, err := f.m.ReplaceWorkflow(ctx, f.tenantID, "REQUEST", []WorkflowStep{
		{Name: "Захирал", SignerRegNumber: "CC90010111"},
	}); err != nil {
		t.Fatalf("configure chain: %v", err)
	}

	on := SignaturePolicy{DocType: "REQUEST", AllowEID: true, AllowDAN: true, RequireNamedSigner: true}
	_, err = f.m.SaveSignaturePolicy(ctx, f.tenantID, on)
	if !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("turning the requirement on over a document nobody could then sign returned %v, want ErrInvalidConfiguration", err)
	}
	if !strings.Contains(err.Error(), "1 REQUEST") {
		t.Errorf("got %q, want it to say how many documents it is about", err)
	}

	policy, err := f.m.SignaturePolicyFor(ctx, f.tenantID, "REQUEST")
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	if policy.RequireNamedSigner {
		t.Fatal("the refused save was stored anyway")
	}

	// Decided, so it is no longer waiting on anybody — and now the requirement may go on.
	if _, err := f.m.RejectDocument(ctx, f.tenantID, stranded.ID); err != nil {
		t.Fatalf("reject: %v", err)
	}
	saved, err := f.m.SaveSignaturePolicy(ctx, f.tenantID, on)
	if err != nil {
		t.Fatalf("with nothing left to strand the requirement must save: %v", err)
	}
	if !saved.RequireNamedSigner {
		t.Fatal("the saved policy does not carry the requirement")
	}

	// And it means something: a document created now takes the named chain.
	fresh, err := f.m.CreateDocument(ctx, f.tenantID, "Шинэ хүсэлт", "REQUEST")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := signWithEID(t, f, fresh.ID, "AA90010111"); !errors.Is(err, ErrSignatureRejected) {
		t.Errorf("somebody the chain does not name was allowed to sign: got %v", err)
	}
	signed, err := signWithEID(t, f, fresh.ID, "CC90010111")
	if err != nil {
		t.Fatalf("the named signer could not sign: %v", err)
	}
	if signed.Status != StatusApproved {
		t.Errorf("status = %q, want %q", signed.Status, StatusApproved)
	}
}

// A button whose only outcome is an error is worse than no button.
//
// The dialog offered E-ID and ДАН always: neither this organisation's policy
// for the type nor whether the installation has the channel at all reached the
// screen. On ДАН that is every deployment — there is no live client for it — so
// picking it could only ever answer 503.
func TestTheScreenIsToldWhichChannelsCanActuallySign(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	policy, err := f.m.SignaturePolicyFor(ctx, f.tenantID, doc.DocType)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	// An unconfigured type allows both channels, which is what makes the
	// installation's own answer the one that decides here.
	if !policy.AllowEID || !policy.AllowDAN {
		t.Fatalf("the default policy changed: %+v", policy)
	}

	rails := domain.RailsFor(domain.SignaturePolicy{
		DocType:  policy.DocType,
		AllowEID: policy.AllowEID, AllowDAN: policy.AllowDAN,
		RequireNamedSigner: policy.RequireNamedSigner, Configured: policy.Configured,
	}, f.m.eidSvc.Mode() != "unconfigured", f.m.danSvc.Mode() != "unconfigured")

	if len(rails) != 2 {
		t.Fatalf("both channels have to be described, offered or not: %+v", rails)
	}
	for _, rail := range rails {
		if rail.Available && rail.Reason != "" {
			t.Errorf("%+v is available and carries a reason", rail)
		}
		if !rail.Available && rail.Reason == "" {
			t.Errorf("%+v is refused without saying whose decision it was", rail)
		}
	}
}
