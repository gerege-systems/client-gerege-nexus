/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package urtuu

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// Work may only be given to somebody who belongs to this organisation.
//
// The check used to be `SELECT EXISTS (... FROM memberships ...)` in the
// handler; it is nexus.Directory now. The property it holds is the reason it
// exists at all, and the comment beside the old query said it: a user id from
// another organisation would otherwise be assignable, and no row-level policy
// covers `users` — people belong to several organisations by design.
//
// Untested until now, on either side of the change.
func TestWorkGoesOnlyToAMemberOfThisOrganisation(t *testing.T) {
	dsn := os.Getenv("URTUU_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("set URTUU_TEST_DATABASE_URL or DATABASE_URL to a migrated test database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	here, hereUser := organisationWithOneMember(t, pool, "here")
	there, thereUser := organisationWithOneMember(t, pool, "there")

	// The platform's directory, as this module sees it. It was
	// internal/platform/directory over the same Postgres while this app lived
	// in the core; that package is the platform's to test, and what is under
	// test here is what the app does with the answer — refuse work to somebody
	// who is not a member of the organisation giving it.
	people := &fakePeople{}
	people.add(nexus.DirectoryPerson{TenantID: here, UserID: hereUser, Active: true})
	people.add(nexus.DirectoryPerson{TenantID: there, UserID: thereUser, Active: true})
	nexus.Provide[nexus.Directory](people)
	t.Cleanup(func() { nexus.Provide[nexus.Directory](nil) })

	module := &Module{db: pool}

	if !module.isMember(ctx, here, hereUser) {
		t.Error("this organisation's own member was refused work")
	}
	if module.isMember(ctx, here, thereUser) {
		t.Error("a member of another organisation was accepted; that is the whole point of the check")
	}
	if module.isMember(ctx, there, hereUser) {
		t.Error("the check is not symmetric: it accepted the first organisation's member in the second")
	}
	if module.isMember(ctx, here, uuid.NewString()) {
		t.Error("a user id belonging to nobody was accepted")
	}
}

// A deactivated member is not somebody to give work to. Their account still
// signs in — they may belong to another organisation — but this one has said
// they have left.
func TestWorkDoesNotGoToSomebodyWhoHasLeft(t *testing.T) {
	dsn := os.Getenv("URTUU_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("set URTUU_TEST_DATABASE_URL or DATABASE_URL to a migrated test database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	tenantID, userID := organisationWithOneMember(t, pool, "left")

	people := &fakePeople{}
	person := nexus.DirectoryPerson{TenantID: tenantID, UserID: userID, Active: true}
	people.add(person)
	nexus.Provide[nexus.Directory](people)
	t.Cleanup(func() { nexus.Provide[nexus.Directory](nil) })
	module := &Module{db: pool}
	if !module.isMember(ctx, tenantID, userID) {
		t.Fatal("the member was refused before being deactivated")
	}

	// The organisation says they have left. The directory is what carries that
	// fact to this app, so that is where it is said — the membership row is the
	// platform's and this app never reads it.
	people.deactivate(userID)
	if module.isMember(ctx, tenantID, userID) {
		t.Error("work was offered to somebody this organisation has deactivated")
	}
}

// organisationWithOneMember creates a tenant with one active member and returns
// both ids, cleaning up after itself.
func organisationWithOneMember(t *testing.T, pool *pgxpool.Pool, name string) (string, string) {
	t.Helper()
	ctx := context.Background()
	tenantID, userID := uuid.NewString(), uuid.NewString()
	slug := "urtuu-people-" + name + "-" + tenantID[:8]

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants (id, name, slug) VALUES ($1, $2, $2)`, tenantID, slug); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID) })

	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, name) VALUES ($1, $2, '', $3)`,
		userID, slug+"@example.invalid", "Person "+name); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })

	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, active) VALUES ($1, $2, true)`,
		tenantID, userID); err != nil {
		t.Fatalf("create membership: %v", err)
	}
	return tenantID, userID
}
