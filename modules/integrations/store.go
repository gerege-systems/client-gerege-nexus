package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is returned when a connector does not exist *for the asking
// tenant*. It is deliberately not distinguishable from "exists but belongs to
// someone else": every lookup is scoped by tenant_id, so a neighbouring
// tenant's connector id reads as absent rather than as forbidden.
var ErrNotFound = errors.New("integration not found")

// ErrDuplicateName is the unique-name constraint, stated for the person who
// picked the name. One tenant's connectors are told apart by name — it is what
// an operator selects a destination by — so two called "Archive" is a way to
// file a signed document into the wrong account.
var ErrDuplicateName = errors.New("a connector with this name already exists")

// ConnectorStatus mirrors the CHECK constraint on the table.
//
// It is the administrator's intent and nothing else. It used to double as the
// last delivery outcome — a failure wrote ERROR into it — which switched the
// connector off, because every selection predicate here requires ACTIVE. A
// connector that is switched off is never contacted again, so the success that
// would have cleared the flag could not happen. Health lives in LastError.
type ConnectorStatus string

const (
	StatusActive   ConnectorStatus = "ACTIVE"
	StatusInactive ConnectorStatus = "INACTIVE"
)

// Connector is one tenant's configured integration.
//
// Neither secret carries out of this package in a form that can be serialised:
// SecretKey and the OAuth bundle are write-only fields, populated when saving
// and left empty when reading. What the API returns is this struct, so the
// redaction is structural rather than a step someone has to remember.
type Connector struct {
	ID           string            `json:"id"`
	TenantID     string            `json:"tenant_id"`
	Provider     Provider          `json:"provider"`
	Name         string            `json:"name"`
	TargetURL    string            `json:"target_url"`
	Status       ConnectorStatus   `json:"status"`
	Config       map[string]string `json:"config"`
	AccountLabel string            `json:"account_label"`
	Connected    bool              `json:"connected"`
	ConnectedAt  *time.Time        `json:"connected_at,omitempty"`
	LastPingAt   *time.Time        `json:"last_ping_at,omitempty"`
	LastError    string            `json:"last_error,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`

	// Capabilities is derived from the provider, not stored, so a connector
	// created before a capability existed still reports it.
	Capabilities []Capability `json:"capabilities"`
}

const connectorColumns = `
	id::text, tenant_id::text, provider, name, target_url, status, config,
	account_label, (oauth_ciphertext IS NOT NULL) AS connected,
	connected_at, last_ping_at, last_error, created_at, updated_at`

func scanConnector(row pgx.Row) (*Connector, error) {
	var c Connector
	var configRaw []byte
	if err := row.Scan(&c.ID, &c.TenantID, &c.Provider, &c.Name, &c.TargetURL, &c.Status,
		&configRaw, &c.AccountLabel, &c.Connected, &c.ConnectedAt, &c.LastPingAt,
		&c.LastError, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Config = map[string]string{}
	if len(configRaw) > 0 {
		_ = json.Unmarshal(configRaw, &c.Config)
	}
	if spec, err := SpecFor(c.Provider); err == nil {
		c.Capabilities = spec.Capabilities
	}
	if c.Capabilities == nil {
		c.Capabilities = []Capability{}
	}
	return &c, nil
}

type store struct{ db nexus.DB }

// list returns one tenant's connectors, newest first.
func (s *store) list(ctx context.Context, tenantID string) ([]*Connector, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+connectorColumns+` FROM workspace.integrations
		  WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*Connector, 0)
	for rows.Next() {
		c, err := scanConnector(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *store) get(ctx context.Context, tenantID, id string) (*Connector, error) {
	c, err := scanConnector(s.db.QueryRow(ctx,
		`SELECT `+connectorColumns+` FROM workspace.integrations WHERE id = $1 AND tenant_id = $2`,
		id, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// SaveRequest is what an administrator submits. Secret is write-only: an empty
// value on an update leaves whatever is stored alone rather than clearing it,
// because the form that submitted it never received the old value to resend.
type SaveRequest struct {
	Provider  Provider
	Name      string
	TargetURL string
	Secret    string
	Config    map[string]string
	Status    ConnectorStatus
}

func (s *store) create(ctx context.Context, tenantID string, req SaveRequest) (*Connector, error) {
	sealed, err := seal([]byte(req.Secret))
	if err != nil {
		return nil, err
	}
	configJSON, err := json.Marshal(req.Config)
	if err != nil {
		return nil, err
	}
	status := req.Status
	if status == "" {
		status = StatusActive
	}
	c, err := scanConnector(s.db.QueryRow(ctx,
		`INSERT INTO workspace.integrations (tenant_id, provider, name, target_url, status, config, secret_ciphertext)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING `+connectorColumns,
		tenantID, req.Provider, req.Name, req.TargetURL, status, configJSON, sealed))
	return c, namedError(err)
}

func (s *store) update(ctx context.Context, tenantID, id string, req SaveRequest) (*Connector, error) {
	configJSON, err := json.Marshal(req.Config)
	if err != nil {
		return nil, err
	}
	// A blank secret means "unchanged", so COALESCE keeps the stored one. The
	// alternative — treating blank as "clear it" — silently unsigns a tenant's
	// webhooks the first time somebody edits the connector's name.
	var sealed []byte
	if req.Secret != "" {
		if sealed, err = seal([]byte(req.Secret)); err != nil {
			return nil, err
		}
	}
	c, err := scanConnector(s.db.QueryRow(ctx,
		`UPDATE workspace.integrations
		    SET name = $3, target_url = $4, status = $5, config = $6,
		        secret_ciphertext = COALESCE($7, secret_ciphertext),
		        updated_at = NOW()
		  WHERE id = $1 AND tenant_id = $2
		 RETURNING `+connectorColumns,
		id, tenantID, req.Name, req.TargetURL, req.Status, configJSON, sealed))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, namedError(err)
}

// namedError turns the unique-name constraint into something a handler can
// answer with. Left as a raw driver error it reached the browser as a 400
// carrying the constraint's own name — which tells an administrator nothing and
// tells everyone else how the table is built.
func namedError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrDuplicateName
	}
	return err
}

func (s *store) delete(ctx context.Context, tenantID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM workspace.integrations WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// secretFor reads and decrypts a webhook signing key.
func (s *store) secretFor(ctx context.Context, tenantID, id string) (string, error) {
	var sealed []byte
	err := s.db.QueryRow(ctx,
		`SELECT secret_ciphertext FROM workspace.integrations WHERE id = $1 AND tenant_id = $2`,
		id, tenantID).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	plain, err := open(sealed)
	return string(plain), err
}

// dispatchTarget is the minimum a webhook delivery needs, read in one pass so
// dispatch does not issue a query per subscriber.
type dispatchTarget struct {
	id     string
	url    string
	secret string
}

func (s *store) dispatchTargets(ctx context.Context, tenantID string) ([]dispatchTarget, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id::text, target_url, secret_ciphertext
		   FROM workspace.integrations
		  WHERE tenant_id = $1 AND provider = 'webhook' AND status = 'ACTIVE' AND target_url <> ''`,
		tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]dispatchTarget, 0)
	for rows.Next() {
		var t dispatchTarget
		var sealed []byte
		if err := rows.Scan(&t.id, &t.url, &sealed); err != nil {
			return nil, err
		}
		plain, err := open(sealed)
		if err != nil {
			// One unreadable secret must not stop the other subscribers from
			// being delivered to; it is recorded against its own connector.
			s.noteError(ctx, tenantID, t.id, err.Error())
			continue
		}
		t.secret = string(plain)
		out = append(out, t)
	}
	return out, rows.Err()
}

// noteError records why a connector last failed. Best effort: it runs on paths
// that are already handling a failure, and a second failure there should not
// replace the first.
//
// It deliberately leaves status alone. Writing 'ERROR' here, as this used to,
// removed the connector from every dispatch and export query — so a subscriber
// that answered 503 once received nothing ever again, and the retry that would
// have proved it healthy was never attempted. A failure is reported, not acted
// on; only an administrator switches a connector off.
func (s *store) noteError(ctx context.Context, tenantID, id, detail string) {
	_, _ = s.db.Exec(ctx,
		`UPDATE workspace.integrations SET last_error = $3, updated_at = NOW()
		  WHERE id = $1 AND tenant_id = $2`, id, tenantID, detail)
}

func (s *store) noteSuccess(ctx context.Context, tenantID, id string) {
	_, _ = s.db.Exec(ctx,
		`UPDATE workspace.integrations SET last_ping_at = NOW(), last_error = '', updated_at = NOW()
		  WHERE id = $1 AND tenant_id = $2`, id, tenantID)
}

// ─── OAuth token storage ─────────────────────────────────────────────────────

// tokenBundle is what a completed OAuth grant leaves behind. It is sealed as
// one JSON blob rather than as separate columns so that adding a field later
// does not need a migration and a re-encryption pass.
type tokenBundle struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Scope        string    `json:"scope,omitempty"`
}

func (t tokenBundle) expired() bool {
	// Refresh a minute early: a token that expires between the check and the
	// call is the same failure as an expired one, only harder to reproduce.
	return !t.ExpiresAt.IsZero() && time.Now().Add(time.Minute).After(t.ExpiresAt)
}

// saveGrant records a newly authorised account.
//
// This is the connect: an administrator has just come back from a consent
// screen, so the account is named, the connector is switched on and the moment
// it happened is stamped.
func (s *store) saveGrant(ctx context.Context, tenantID, id string, tok tokenBundle, accountLabel string) error {
	sealed, err := sealTokens(tok)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx,
		`UPDATE workspace.integrations
		    SET oauth_ciphertext = $3,
		        account_label = COALESCE(NULLIF($4, ''), account_label),
		        connected_at = NOW(), status = 'ACTIVE', last_error = '', updated_at = NOW()
		  WHERE id = $1 AND tenant_id = $2`,
		id, tenantID, sealed, accountLabel)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// refreshTokens replaces the stored bundle with a renewed one and touches
// nothing else.
//
// Refreshing used to go through the same statement as connecting, which reset
// connected_at every time an access token was renewed — so "connected since"
// drifted forward by the hour and stopped meaning when the account was
// authorised, which is the one question that column answers.
func (s *store) refreshTokens(ctx context.Context, tenantID, id string, tok tokenBundle) error {
	sealed, err := sealTokens(tok)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx,
		`UPDATE workspace.integrations SET oauth_ciphertext = $3, updated_at = NOW()
		  WHERE id = $1 AND tenant_id = $2`,
		id, tenantID, sealed)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func sealTokens(tok tokenBundle) ([]byte, error) {
	raw, err := json.Marshal(tok)
	if err != nil {
		return nil, err
	}
	return seal(raw)
}

func (s *store) tokens(ctx context.Context, tenantID, id string) (tokenBundle, error) {
	var sealed []byte
	err := s.db.QueryRow(ctx,
		`SELECT oauth_ciphertext FROM workspace.integrations WHERE id = $1 AND tenant_id = $2`,
		id, tenantID).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return tokenBundle{}, ErrNotFound
	}
	if err != nil {
		return tokenBundle{}, err
	}
	if len(sealed) == 0 {
		return tokenBundle{}, fmt.Errorf("connector is not connected to an account yet")
	}
	plain, err := open(sealed)
	if err != nil {
		return tokenBundle{}, err
	}
	var tok tokenBundle
	if err := json.Unmarshal(plain, &tok); err != nil {
		return tokenBundle{}, fmt.Errorf("integration: stored token is unreadable: %w", err)
	}
	return tok, nil
}

func (s *store) disconnect(ctx context.Context, tenantID, id string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE workspace.integrations
		    SET oauth_ciphertext = NULL, account_label = '', connected_at = NULL,
		        status = 'INACTIVE', updated_at = NOW()
		  WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ─── Delivery log ────────────────────────────────────────────────────────────

// Delivery is one recorded attempt to send something out of the platform.
type Delivery struct {
	ID          string    `json:"id"`
	Integration string    `json:"integration_id"`
	Kind        string    `json:"kind"`
	Reference   string    `json:"reference"`
	Outcome     string    `json:"outcome"`
	Detail      string    `json:"detail,omitempty"`
	ExternalID  string    `json:"external_id,omitempty"`
	ExternalURL string    `json:"external_url,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s *store) recordDelivery(ctx context.Context, tenantID, integrationID, kind, reference,
	outcome, detail, externalID, externalURL string) {
	_, _ = s.db.Exec(ctx,
		`INSERT INTO workspace.integration_deliveries
		     (tenant_id, integration_id, kind, reference, outcome, detail, external_id, external_url)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		tenantID, integrationID, kind, reference, outcome, detail, externalID, externalURL)
}

// deleteDeliveriesBefore prunes the delivery log.
//
// It is append-only and written to on every event, every export and every
// meeting, so without this it is the table that grows without limit. The
// horizon is long enough to answer for a disclosure after the fact, which is
// what the log is for.
func (s *store) deleteDeliveriesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM workspace.integration_deliveries WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *store) deliveries(ctx context.Context, tenantID string, limit int) ([]Delivery, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx,
		`SELECT id::text, integration_id::text, kind, reference, outcome, detail,
		        external_id, external_url, created_at
		   FROM workspace.integration_deliveries
		  WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Delivery, 0, limit)
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.Integration, &d.Kind, &d.Reference, &d.Outcome,
			&d.Detail, &d.ExternalID, &d.ExternalURL, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
