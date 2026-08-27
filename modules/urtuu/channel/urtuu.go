/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Package urtuu carries signed work between two Gerege Nexus installations.
 *
 * It is the transport half of Өртөө: links, the outbound queue, delivery with
 * retry, and идемпотент receipt. What the messages *mean* — tasks, their
 * lifecycle, the screens — is the Өртөө app in internal/apps/urtuu. The split
 * is the one the proposal draws (§3): the channel is infrastructure any module
 * may reach for, the task board is a product a tenant chooses to install.
 *
 * Two rules shape everything here.
 *
 * The child connects, never the parent (§2.1). A subordinate installation may
 * be behind a firewall, on a private network, or switched off for a week; a
 * parent that had to reach it would have a link that works only from one side
 * of the network. So the child polls for what is waiting and pushes what has
 * happened, and the parent's half of the conversation is a queue it holds until
 * somebody comes for it. The catalogue sync chose pull for the same reason.
 *
 * A token says who is speaking; a signature says who wrote it (§2.2). They are
 * different questions and both are asked on every envelope. A stolen token gets
 * an attacker a conversation and no ability to author anything in it; a forged
 * envelope does not survive the public key exchanged when the link was made.
 */
package channel

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/go-chi/chi/v5"
)

const (
	// signingKeyEnv holds this installation's Ed25519 key, base64. Either the
	// 32-byte seed or the 64-byte private key is accepted: the first is what
	// `openssl rand -base64 32` produces and the second is what Go prints, and
	// an operator should not have to know which one this program wanted.
	signingKeyEnv = "URTUU_SIGNING_KEY"

	// insecurePeersEnv allows plain http peers. Off by default: the token that
	// authenticates a link travels in the Authorization header, and a link
	// established over http hands it to anybody on the path. A deployment whose
	// installations sit on one private government network turns it on
	// deliberately, which is also what the integration tests do.
	insecurePeersEnv = "URTUU_ALLOW_INSECURE_PEERS"
)

// Service is the transport. Nil-safe at the edges: a deployment with no signing
// key still constructs one, and it reports itself off rather than making the
// platform refuse to start — Өртөө is a channel some deployments use, not
// something signing in depends on.
type Service struct {
	db    nexus.DB
	perms nexus.PermissionStore

	// signing is this installation's own key. Nil means Өртөө is off.
	signing ed25519.PrivateKey
	public  ed25519.PublicKey
	// installationID is derived from the public key rather than configured.
	// It has to be stable, globally unique and unforgeable, because it is what
	// a task's origin_chain is checked against to stop a task travelling in a
	// circle (§9) — and a value an operator types is none of those three.
	installationID string

	client *http.Client

	// ring is the state register, when this deployment has been given
	// credentials for it. Nil is the ordinary case and means the import button
	// says so rather than failing under somebody's finger.
	ring RingImporter

	// readers is who reads which kind of envelope. A plain map behind a lock:
	// it is written a handful of times during construction and read once per
	// envelope that arrives.
	readersMu sync.RWMutex
	readers   map[string]contract.LinkReader
}

// New builds the transport from the environment.
//
// A missing or unusable key is a warning and a disabled service, never a
// startup failure. The alternative — refusing to boot because an optional
// channel is unconfigured — would make every deployment that does not use
// Өртөө carry a key it has no use for.
func New(db nexus.DB, perms nexus.PermissionStore) *Service {
	s := &Service{
		db:    db,
		perms: perms,
		// Long enough for a 25-second long poll and the round trip around it.
		// The exchange loop bounds each call with a context as well; this is
		// the backstop for a peer that accepts a connection and then says
		// nothing at all.
		client: &http.Client{Timeout: pullWindow + 20*time.Second},
	}

	raw := strings.TrimSpace(os.Getenv(signingKeyEnv))
	if raw == "" {
		slog.Info("urtuu: " + signingKeyEnv + " is not set, so this installation has no Өртөө identity and the channel is off")
		return s
	}
	key, err := parseSigningKey(raw)
	if err != nil {
		// Not generated on the spot. A key that changes at every boot would
		// make every existing link unverifiable and every peer's stored public
		// key wrong, which is a worse failure than being switched off — and a
		// silent one, because the links would look established.
		slog.Error("urtuu: "+signingKeyEnv+" could not be read, so the channel is off",
			"error", err)
		return s
	}
	s.signing = key
	s.public = key.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(s.public)
	s.installationID = hex.EncodeToString(sum[:16])
	slog.Info("urtuu: this installation can exchange work with its peers",
		"installation_id", s.installationID, "public_key", s.PublicKey())

	// The register, and the one kind of envelope this package reads for itself.
	// The task kinds belong to the Өртөө app and are registered by it, so an
	// installation without the app still receives, stores and acknowledges them
	// — and processes them the day it is installed.
	s.ring = newRingImporter(s.client)
	s.Deliver(contract.KindCodeSync, s.receiveCodeSync)
	return s
}

// parseSigningKey accepts a base64 seed or a base64 private key.
func parseSigningKey(raw string) (ed25519.PrivateKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("not base64")
	}
	switch len(decoded) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(decoded), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(decoded), nil
	default:
		return nil, errors.New("not a 32-byte Ed25519 seed or a 64-byte private key")
	}
}

// Enabled reports whether this installation has an Өртөө identity.
func (s *Service) Enabled() bool { return s.signing != nil }

// PublicKey is what peers verify this installation's envelopes with, base64.
func (s *Service) PublicKey() string {
	if !s.Enabled() {
		return ""
	}
	return base64.StdEncoding.EncodeToString(s.public)
}

// InstallationID identifies this installation on a task's origin chain.
func (s *Service) InstallationID() string { return s.installationID }

// peerToken is the bearer token this installation presents on one link.
//
// Derived rather than stored, which removes the one secret this package would
// otherwise have to keep in the clear: a child has to *present* its token, so
// hashing it — the way sessions and devices do — is not open to it, and storing
// it as plaintext or inventing a second encryption key both cost more than this
// does. HMAC over the signing key with a domain separator gives a value that is
// stable for the life of the link, unguessable without the key, and different
// for every link and every installation.
//
// The parent, which only ever verifies, keeps the SHA-256 the way it keeps a
// session's — see redeem.
func (s *Service) peerToken(peerID string) string {
	mac := hmac.New(sha256.New, s.signing.Seed())
	mac.Write([]byte("urtuu-peer-token:" + peerID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// tokenHash is what the verifying side stores. #nosec G101 -- this hashes a
// credential, it does not contain one.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// inviteHash normalises an invite code before hashing it: the code is read off
// a screen and typed into another one, so case and the grouping dashes are
// presentation rather than content — the same rule device enrolment codes
// follow.
func inviteHash(code string) string {
	return tokenHash(strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", "")))
}

// allowInsecurePeers reports whether http peers are permitted.
func allowInsecurePeers() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(insecurePeersEnv))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// normalizeBaseURL validates the address an administrator typed for a parent.
//
// This is a trust boundary: whatever comes back from here is an address this
// server will connect to, carrying this link's token. Scheme and host are
// checked, the path is dropped — a base URL is an origin, and everything under
// it is this package's to decide — and http is refused unless the deployment
// has said its peers are on a private network.
func normalizeBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("the parent address is not a URL")
	}
	if parsed.Host == "" {
		return "", errors.New("the parent address has no host")
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !allowInsecurePeers() {
			return "", errors.New("the parent address must be https, or set " + insecurePeersEnv +
				" if these installations share a private network")
		}
	default:
		return "", errors.New("the parent address must be http or https")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

// wellKnown is what an installation publishes about itself.
//
// It is the one thing a peer reads before there is any relationship: a child
// being pointed at a parent fetches this to learn the key it will verify that
// parent's envelopes with. Nothing here is a secret and nothing here is
// tenant-scoped — this is the installation, not an organisation on it.
type wellKnown struct {
	InstallationID string `json:"installation_id"`
	PublicKey      string `json:"public_key"`
	// Protocol is the envelope version this installation speaks. One value so
	// far; it is here because the first incompatible change is the one that
	// needs somewhere to have been announced.
	Protocol string `json:"protocol"`
}

// Protocol is the envelope contract version.
const Protocol = "urtuu/1"

// Routes mounts everything the channel serves: four endpoints another
// installation reaches, and the two an administrator here does.
//
// It takes the root router and the platform's workspace authentication, which
// is what a module is handed — the channel is part of an app now, and this is
// the app's RegisterRoutes reaching into it. It used to be two methods called
// from the platform's own setupRoutes, where the policy for every route in the
// deployment was decided in one file; that file no longer knows Өртөө exists,
// so the four session-less routes are declared session-less here, in the
// package that knows why each one is.
//
// The permission split is the other half. `urtuu.read` and `urtuu.manage` are
// declared by the app this channel is part of, so a member who cannot read the
// board cannot read the links either.
func (s *Service) Routes(r chi.Router, workspaceAuth func(http.Handler) http.Handler) {
	// This installation's identity: the public key a subordinate verifies its
	// parent's envelopes with. At the root and public by necessity — it is read
	// before any relationship exists, by a server that has nothing to
	// authenticate with yet. It carries no secret and answers 404 where the
	// channel is not configured, which is a real answer rather than an absence.
	r.Get("/.well-known/urtuu.json", s.HandleWellKnown)

	// Redeeming an invitation. Session-less because the caller is another
	// installation rather than a person; the single-use code (24 hours, hashed,
	// cleared on use) is the whole of the authority.
	r.Post("/api/v1/urtuu/peers/redeem", s.HandleRedeem)

	// The exchange. Authenticated by the link's bearer token, and every
	// envelope inside is checked again against the peer's public key — a token
	// says who is speaking and only a signature says who wrote what was said.
	//
	// Deliberately unthrottled: the caller is a server holding a credential
	// this deployment issued, and one child catching up after a week of
	// downtime must not throttle another out of the channel. What bounds it is
	// batchLimit and the long-poll window on the other side of the call.
	r.Get("/api/v1/urtuu/exchange/pull", s.HandlePull)
	r.Post("/api/v1/urtuu/exchange/push", s.HandlePush)

	r.Route("/api/v1/urtuu/peers", func(cr chi.Router) {
		cr.Use(workspaceAuth)
		read := nexus.RequirePermission(s.perms, "urtuu.read")
		manage := nexus.RequirePermission(s.perms, "urtuu.manage")
		cr.With(read).Get("/", s.handleListPeers)
		cr.With(manage).Post("/invite", s.handleInvite)
		cr.With(manage).Post("/", s.handleJoin)
		cr.With(manage).Post("/{id}/confirm", s.handleConfirm)
		cr.With(manage).Post("/{id}/revoke", s.handleRevoke)
		// Which of this organisation's codes a link may carry. A PUT because it
		// replaces the whole set — see handleSetPeerCodes for why adding is not
		// the operation.
		cr.With(manage).Put("/{id}/codes", s.handleSetPeerCodes)
	})
	r.Route("/api/v1/urtuu/codes", func(cr chi.Router) {
		cr.Use(workspaceAuth)
		read := nexus.RequirePermission(s.perms, "urtuu.read")
		manage := nexus.RequirePermission(s.perms, "urtuu.manage")
		cr.With(read).Get("/", s.handleListCodes)
		cr.With(manage).Post("/", s.handleCreateCode)
		cr.With(manage).Put("/{id}", s.handleUpdateCode)
		// Reaching out to the state register changes the vocabulary every child
		// of this organisation will be offered, so it is a manage act even
		// though nothing local is authored by it.
		cr.With(manage).Post("/ring-sync", s.handleRingSync)
	})
}

// The three endpoints another installation reaches. All are session-less by
// necessity — the caller is a server, not a person — and each carries its own
// authority: HandleRedeem the invitation code, HandlePull and HandlePush the
// link's bearer token. Routes above is where that is declared.

// HandleWellKnown serves /.well-known/urtuu.json.
func (s *Service) HandleWellKnown(w http.ResponseWriter, _ *http.Request) {
	if !s.Enabled() {
		// 404 rather than an empty document: an installation with no identity
		// has no Өртөө presence at all, and answering with a blank key would
		// let a child establish a link that can never verify anything.
		nexus.Error(w, http.StatusNotFound, "this installation does not run Өртөө")
		return
	}
	nexus.JSON(w, http.StatusOK, wellKnown{
		InstallationID: s.installationID,
		PublicKey:      s.PublicKey(),
		Protocol:       Protocol,
	})
}
