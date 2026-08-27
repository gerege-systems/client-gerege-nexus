package channel

// The register client, against documents built the way the standard says they
// are built — docs/RING_STANDARD.md.
//
// No database and no network: what is under test is the contract with
// ring.dgov.mn, which is a document and a signature.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
)

// The register's key. A test key, seeded, signing nothing real.
func registerKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	seed := []byte("ring-register-seed-0123456789abc")
	if len(seed) != ed25519.SeedSize {
		t.Fatalf("seed is %d bytes, want %d", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed)
}

// publish builds a document the way the standard describes: the signature is
// taken over generated_at, a newline, and the processes array *verbatim*.
func publish(t *testing.T, key ed25519.PrivateKey, generatedAt, processes string) []byte {
	t.Helper()
	message := append(append([]byte(generatedAt), '\n'), processes...)
	document := map[string]any{
		"generated_at": generatedAt,
		"key_id":       "ring-test",
		"processes":    json.RawMessage(processes),
		"signature":    base64.StdEncoding.EncodeToString(ed25519.Sign(key, message)),
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return encoded
}

const oneProcess = `[{"code":"Д-101","line":"service",` +
	`"names":{"mn":"Тодорхойлолт олгох","en":"Issue a certificate"},` +
	`"schema":{"type":"object","properties":{"period":{"type":"string"}}},` +
	`"sla_hours":240,"version":3,"active":true,"process_ref":"ring/Д-101"}]`

// registerServing stands up a register that answers with the given body, and
// returns the client pointed at it plus a count of how many times it was asked.
func registerServing(t *testing.T, key ed25519.PrivateKey, body []byte, etag string) (*ringHTTP, *int) {
	t.Helper()
	asked := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// The conditional half of the standard: a register that has published
		// nothing new answers 304 and the whole vocabulary stays off the wire.
		if etag != "" && r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	return &ringHTTP{
		base:      server.URL,
		key:       "test-key",
		publicKey: key.Public().(ed25519.PublicKey),
		client:    server.Client(),
	}, &asked
}

func TestTheRegistersDocumentBecomesRequestCodes(t *testing.T) {
	key := registerKey(t)
	client, _ := registerServing(t, key, publish(t, key, "2026-08-16T09:00:00Z", oneProcess), "")

	codes, err := client.Processes(context.Background())
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(codes) != 1 {
		t.Fatalf("imported %d codes, want 1", len(codes))
	}

	code := codes[0]
	if code.Code != "Д-101" {
		t.Errorf("code = %q", code.Code)
	}
	if code.Line != contract.LineService {
		t.Errorf("line = %q, want the service line", code.Line)
	}
	if code.Source != contract.SourceRing {
		t.Errorf("source = %q", code.Source)
	}
	// Calendar hours, because a holiday calendar is not this platform's to own.
	if code.DefaultSLA != 240*time.Hour {
		t.Errorf("sla = %s, want 240h", code.DefaultSLA)
	}
	if code.Version != 3 {
		t.Errorf("version = %d, want the register's 3", code.Version)
	}
	// The schema is passed through untouched: what a valid JSON Schema is, is
	// not this platform's question.
	if !strings.Contains(string(code.Schema), `"period"`) {
		t.Errorf("schema did not survive: %s", code.Schema)
	}
}

// The one guarantee the whole format exists for.
func TestAnEditedRegisterDocumentIsRefused(t *testing.T) {
	key := registerKey(t)
	document := publish(t, key, "2026-08-16T09:00:00Z", oneProcess)

	// The transport was perfectly good and one norm was changed on the way:
	// ten days becomes one. Every office in the country would be measured
	// against it.
	tampered := strings.Replace(string(document), `"sla_hours":240`, `"sla_hours":24`, 1)
	if tampered == string(document) {
		t.Fatal("the test did not edit the document")
	}

	client, _ := registerServing(t, key, []byte(tampered), "")
	if _, err := client.Processes(context.Background()); err == nil {
		t.Fatal("an edited register document was accepted")
	}
}

func TestAnotherKeysDocumentIsRefused(t *testing.T) {
	key := registerKey(t)
	_, impostor, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	client, _ := registerServing(t, key, publish(t, impostor, "2026-08-16T09:00:00Z", oneProcess), "")

	if _, err := client.Processes(context.Background()); err == nil {
		t.Fatal("a document signed by somebody else was accepted")
	}
}

// A register that has published nothing new must cost a 304, and the tag must
// only be remembered for a document that was actually accepted.
func TestAnUnchangedRegisterCostsNothing(t *testing.T) {
	key := registerKey(t)
	client, asked := registerServing(t, key,
		publish(t, key, "2026-08-16T09:00:00Z", oneProcess), `"v1"`)

	if _, err := client.Processes(context.Background()); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if _, err := client.Processes(context.Background()); !errors.Is(err, ErrRingUnchanged) {
		t.Fatalf("second import = %v, want ErrRingUnchanged", err)
	}
	if *asked != 2 {
		t.Errorf("the register was asked %d times, want 2", *asked)
	}
}

// A tag kept for a document that failed verification would make the next
// import a 304 and leave the bad answer standing as the current one.
func TestARefusedDocumentIsNotRemembered(t *testing.T) {
	key := registerKey(t)
	_, impostor, _ := ed25519.GenerateKey(nil)
	client, _ := registerServing(t, key,
		publish(t, impostor, "2026-08-16T09:00:00Z", oneProcess), `"v1"`)

	if _, err := client.Processes(context.Background()); err == nil {
		t.Fatal("the forged document was accepted")
	}
	client.mu.Lock()
	remembered := client.etag
	client.mu.Unlock()
	if remembered != "" {
		t.Errorf("the client remembered %q from a document it refused", remembered)
	}
}

// One bad record costs that record, not the country's vocabulary — and the
// namespace rule holds against the register too.
func TestOneUnusableRecordDoesNotLoseTheRest(t *testing.T) {
	processes := `[` +
		`{"code":"","names":{"mn":"Нэргүй"},"version":1},` +
		`{"code":"local.mine","names":{"mn":"Хулгайлсан орон зай"},"version":1},` +
		`{"code":"Д-900","names":{"en":"No Mongolian name"},"version":1},` +
		`{"code":"Д-101","names":{"mn":"Зөв"},"version":1}` +
		`]`
	key := registerKey(t)
	client, _ := registerServing(t, key, publish(t, key, "2026-08-16T09:00:00Z", processes), "")

	codes, err := client.Processes(context.Background())
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(codes) != 1 || codes[0].Code != "Д-101" {
		t.Fatalf("imported %+v, want only Д-101", codes)
	}
}

// Without a key to verify with, the import does not exist. A register that has
// been impersonated could otherwise change what a code means, and how long it
// is allowed to take, on every government installation in the country.
func TestTheImportIsOffWithoutAKeyToVerifyWith(t *testing.T) {
	t.Setenv(ringBaseURLEnv, "https://ring.example.mn")
	t.Setenv(ringAPIKeyEnv, "a-key")
	t.Setenv(ringPublicKeyEnv, "")
	if newRingImporter(http.DefaultClient) != nil {
		t.Error("the import was built with no way to check a signature")
	}

	t.Setenv(ringPublicKeyEnv, "not-base64!!")
	if newRingImporter(http.DefaultClient) != nil {
		t.Error("the import was built with an unreadable key")
	}

	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	t.Setenv(ringPublicKeyEnv, base64.StdEncoding.EncodeToString(public))
	if newRingImporter(http.DefaultClient) == nil {
		t.Error("a fully configured register was refused")
	}
}
