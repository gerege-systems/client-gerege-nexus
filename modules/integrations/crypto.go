/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package integrations

import (
	"errors"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// The credentials this app stores — a webhook's signing secret, a provider's
// OAuth token bundle — are sealed by the deployment's cipher rather than by one
// of ours.
//
// The cipher was in this package first, moved into the platform's kernel when
// the operator console started holding credentials too, and stayed there when
// the connectors left for this repository. All three moves are the same
// argument: a deployment with two ciphers has half its secrets encrypted one
// way and half the other, and nothing saying which half. So the key is the
// deployment's (INTEGRATION_ENCRYPTION_KEY, unchanged) and this asks for it by
// contract — nexus.SecretSealer.
//
// Asked for per call rather than held in a field: a module is built before
// main() has finished publishing capabilities, and a nil sealer captured at
// construction is a panic at the first save rather than an error at the first
// boot.

// ErrNoEncryptionKey is a deployment with no key configured. Kept as this
// package's own sentinel so the handlers' messages did not have to change; it
// wraps the SDK's, so errors.Is answers for either.
var ErrNoEncryptionKey = errors.New(
	"INTEGRATION_ENCRYPTION_KEY is not set, so credentials cannot be stored safely")

const encryptionKeyEnv = "INTEGRATION_ENCRYPTION_KEY"

// EncryptionConfigured reports whether credentials can be stored. The
// connectors screen asks so it can say why a provider is unavailable instead of
// failing at the moment of saving.
func EncryptionConfigured() bool {
	sealer, err := nexus.Secrets()
	return err == nil && sealer.Configured()
}

func seal(plaintext []byte) ([]byte, error) {
	sealer, err := nexus.Secrets()
	if err != nil {
		return nil, ErrNoEncryptionKey
	}
	sealed, err := sealer.Seal(plaintext)
	if err != nil {
		// The platform's own sentinel says the same thing in its own words;
		// this package's callers compare against this one.
		return nil, ErrNoEncryptionKey
	}
	return sealed, nil
}

func open(ciphertext []byte) ([]byte, error) {
	sealer, err := nexus.Secrets()
	if err != nil {
		return nil, ErrNoEncryptionKey
	}
	return sealer.Open(ciphertext)
}
