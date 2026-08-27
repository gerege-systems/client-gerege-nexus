/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package integrations

import (
	"errors"
	"os"
	"sync"
	"time"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// testSealer is the deployment's cipher, for a test process that has no
// platform under it.
//
// The real one is the platform's (internal/kernel/security) and this package
// reaches it through nexus.SecretSealer. A test binary publishes nothing, so
// without this every save would fail on "no key" and the cases about what
// happens *after* a credential is stored could not run.
//
// It reads the same environment variable and does no encryption at all: what
// these tests assert is that a secret is never handed back out, not that AES
// works. Reversible on purpose, so a case can check what was stored.
type testSealer struct{}

func (testSealer) Configured() bool { return os.Getenv(encryptionKeyEnv) != "" }

func (s testSealer) Seal(plaintext []byte) ([]byte, error) {
	if !s.Configured() {
		return nil, ErrNoEncryptionKey
	}
	if len(plaintext) == 0 {
		return nil, nil
	}
	return append([]byte("sealed:"), plaintext...), nil
}

func (s testSealer) Open(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, nil
	}
	if !s.Configured() {
		return nil, ErrNoEncryptionKey
	}
	if len(ciphertext) < 7 || string(ciphertext[:7]) != "sealed:" {
		return nil, errors.New("integrations: ciphertext was not sealed by this test")
	}
	return ciphertext[7:], nil
}

// resetKeyForTest is what it was: a test changing the environment between
// cases. Nothing is memoised now — the sealer reads the variable on each call —
// so all this has to do is make sure one is published, which keeps every call
// site unchanged. Once, because Provide logs a warning when a capability is
// replaced, and a hundred of those in a test run is noise that hides the one
// that would matter.
var sealerOnce sync.Once

func resetKeyForTest() {
	sealerOnce.Do(func() { nexus.Provide[nexus.SecretSealer](testSealer{}) })
}

func nowPlus(d time.Duration) time.Time  { return time.Now().Add(d) }
func nowMinus(d time.Duration) time.Time { return time.Now().Add(-d) }
