package wire_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
)

// seed is a fixed Ed25519 seed so the signature below is reproducible. It is a
// test key and nothing else has ever signed with it.
var seed = []byte("urtuu-golden-seed-0123456789abcd")

func goldenKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	if len(seed) != ed25519.SeedSize {
		t.Fatalf("seed is %d bytes, want %d", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func goldenEnvelope() wire.Envelope {
	return wire.Envelope{
		MessageID: "b0a1f5d2-0000-4000-8000-000000000001",
		Kind:      wire.KindTaskAssigned,
		CreatedAt: "2026-08-15T09:00:00Z",
		Payload:   json.RawMessage(`{"code":"D-101","title":"Хагас жилийн тооллого"}`),
	}
}

// The signature this exact envelope produces under this exact key. If the
// signing input ever changes shape — a field added, the separator moved, the
// timestamp reformatted — this is what says so, and it says so on both sides of
// a link at once: an installation that has not been upgraded is still producing
// the old bytes, and every envelope between the two would stop verifying in the
// field rather than here.
const goldenSignature = "f5AebJ9Ae1UnxyFU/Gw87KTpF/NnTY2Z3Fjrlu0O5Z5i8k89V79WfnhTMLpn3c6dDZ7aP6k4o1rRtXLSwaqpAA=="

func TestSignatureIsStableAcrossReleases(t *testing.T) {
	signed, err := wire.Sign(goldenKey(t), goldenEnvelope())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if signed.Signature != goldenSignature {
		t.Errorf("signature = %q, want %q\nthe signing input changed; every peer on an older build is now unreachable",
			signed.Signature, goldenSignature)
	}
}

// The contract, stated the way it is actually used: one installation signs, the
// other verifies, and neither of them re-encodes anything on the way.
func TestOneSideSignsAndTheOtherVerifies(t *testing.T) {
	key := goldenKey(t)
	signed, err := wire.Sign(key, goldenEnvelope())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Over the wire and back, which is where a re-encode would happen if one
	// were going to.
	onTheWire, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var received wire.Envelope
	if err := json.Unmarshal(onTheWire, &received); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if err := received.Valid(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if err := wire.Verify(key.Public().(ed25519.PublicKey), received); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if string(received.Payload) != string(signed.Payload) {
		t.Errorf("payload bytes changed in transit: %q -> %q", signed.Payload, received.Payload)
	}
}

func TestAnEditedPayloadStopsVerifying(t *testing.T) {
	key := goldenKey(t)
	signed, err := wire.Sign(key, goldenEnvelope())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// The exact attack the signature exists for: the transport token is
	// correct, the message is well formed, and one field of the task has been
	// changed on the way.
	signed.Payload = json.RawMessage(`{"code":"D-999","title":"Хагас жилийн тооллого"}`)
	if err := wire.Verify(key.Public().(ed25519.PublicKey), signed); !errors.Is(err, wire.ErrSignature) {
		t.Fatalf("verify = %v, want ErrSignature", err)
	}
}

func TestAnotherInstallationsKeyDoesNotVerify(t *testing.T) {
	signed, err := wire.Sign(goldenKey(t), goldenEnvelope())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	other, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := wire.Verify(other, signed); !errors.Is(err, wire.ErrSignature) {
		t.Fatalf("verify = %v, want ErrSignature", err)
	}
}

// created_at is inside the signature so a captured envelope cannot be re-dated
// and replayed as current work.
func TestARedatedEnvelopeStopsVerifying(t *testing.T) {
	key := goldenKey(t)
	signed, err := wire.Sign(key, goldenEnvelope())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	signed.CreatedAt = "2026-09-01T09:00:00Z"
	if err := wire.Verify(key.Public().(ed25519.PublicKey), signed); !errors.Is(err, wire.ErrSignature) {
		t.Fatalf("verify = %v, want ErrSignature", err)
	}
}

func TestFreshnessBoundsTheReplayWindow(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	envelope := goldenEnvelope()

	if err := envelope.Fresh(now); err != nil {
		t.Fatalf("an envelope dated now is not fresh: %v", err)
	}
	if err := envelope.Fresh(now.Add(wire.MaxAge + time.Minute)); err == nil {
		t.Error("an envelope older than MaxAge was accepted; a captured one becomes replayable once the inbox is pruned")
	}
	if err := envelope.Fresh(now.Add(-wire.MaxSkew - time.Minute)); err == nil {
		t.Error("an envelope dated in the future was accepted")
	}
	// A peer whose clock is a couple of minutes ahead is ordinary and must not
	// have its work refused for it.
	if err := envelope.Fresh(now.Add(-time.Minute)); err != nil {
		t.Errorf("a small clock difference was refused: %v", err)
	}
}

func TestValidRefusesMalformedEnvelopesBeforeAnythingExpensive(t *testing.T) {
	base := goldenEnvelope()
	base.Signature = "not-checked-here"

	cases := map[string]func(*wire.Envelope){
		"no message id": func(e *wire.Envelope) { e.MessageID = "" },
		"no kind":       func(e *wire.Envelope) { e.Kind = "" },
		"no payload":    func(e *wire.Envelope) { e.Payload = nil },
		"unsigned":      func(e *wire.Envelope) { e.Signature = "" },
		"payload over the limit": func(e *wire.Envelope) {
			e.Payload = json.RawMessage(`"` + strings.Repeat("x", wire.MaxPayloadBytes) + `"`)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			envelope := base
			mutate(&envelope)
			if err := envelope.Valid(); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestNewMarshalsThePayloadExactlyOnce(t *testing.T) {
	created := time.Date(2026, 8, 15, 9, 0, 0, 0, time.FixedZone("ulaanbaatar", 8*3600))
	envelope, err := wire.New("id-1", wire.KindTaskUpdate, created, map[string]string{"status": "ACCEPTED"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// UTC, always: the format is inside the signature, so two installations in
	// different zones must render the same instant identically.
	if envelope.CreatedAt != "2026-08-15T01:00:00Z" {
		t.Errorf("created_at = %q, want the instant in UTC", envelope.CreatedAt)
	}
	if string(envelope.Payload) != `{"status":"ACCEPTED"}` {
		t.Errorf("payload = %s", envelope.Payload)
	}
}

func TestSignRefusesAKeyThatIsNotOne(t *testing.T) {
	if _, err := wire.Sign(ed25519.PrivateKey("short"), goldenEnvelope()); err == nil {
		t.Error("signed with a key that is not an Ed25519 private key")
	}
	if err := wire.Verify(ed25519.PublicKey("short"), goldenEnvelope()); err == nil {
		t.Error("verified against a key that is not an Ed25519 public key")
	}
	if err := wire.Verify(make(ed25519.PublicKey, ed25519.PublicKeySize), wire.Envelope{Signature: "!!not base64!!"}); err == nil {
		t.Error("accepted a signature that is not base64")
	}
}

// A sanity check on the golden constant itself: it has to be a real Ed25519
// signature, not merely a string that happens to match.
func TestGoldenSignatureIsWellFormed(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(goldenSignature)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) != ed25519.SignatureSize {
		t.Errorf("signature is %d bytes, want %d", len(raw), ed25519.SignatureSize)
	}
}
