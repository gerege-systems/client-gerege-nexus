/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * The organisation in PostgreSQL. Every query here was lifted out of an HTTP
 * handler unchanged — same columns, same joins, same ordering — because a
 * refactor that also rewrites the SQL cannot say which of the two broke
 * anything.
 *
 * What this layer decides is one thing: what a database error means. A unique
 * violation is a code somebody has already used, a foreign key violation is a
 * unit from another organisation, and no rows is a membership that is not this
 * tenant's. Above here those are domain refusals, and nothing has to know that
 * PostgreSQL numbers them 23505 and 23503.
 */
package postgres

import (
	"context"
	"errors"

	"github.com/gerege-systems/client-gerege-nexus/domain/organisation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is the handle this store needs. It is pgx's own shape and deliberately so:
// the platform hands modules a *pgxpool.Pool behind nexus.DB, and that value
// satisfies this as it is. Naming nexus.DB here would be the one import that
// puts the SDK back into the domain.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type Store struct{ db DB }

func New(db DB) *Store { return &Store{db: db} }

func (s *Store) UpdatePerson(ctx context.Context, tenantID, membershipID string, edit organisation.PersonEdit) (bool, error) {
	set := edit.DepartmentID != nil
	var department any
	if set && *edit.DepartmentID != "" {
		department = *edit.DepartmentID
	}
	// The app's own row now, not two columns on the platform's membership
	// table. Upserted rather than updated: a person with no job title and no
	// department has no row here, which is what keeps this table the size of
	// the answers somebody actually gave.
	tag, err := s.db.Exec(ctx,
		`INSERT INTO organisation_people (membership_id, tenant_id, job_title, department_id)
		 VALUES ($1::uuid, $2::uuid, COALESCE($3, ''), CASE WHEN $4 THEN $5::uuid ELSE NULL END)
		 ON CONFLICT (membership_id) DO UPDATE SET
		     job_title     = COALESCE($3, organisation_people.job_title),
		     department_id = CASE WHEN $4 THEN $5::uuid ELSE organisation_people.department_id END,
		     updated_at    = NOW()`,
		membershipID, tenantID, edit.JobTitle, set, department)
	if err != nil {
		return false, organisation.Failed("could not save the person", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Store) ListDepartments(ctx context.Context, tenantIDs []string) ([]organisation.Department, error) {
	rows, err := s.db.Query(ctx,
		// The headcount reads organisation_people, not memberships: which
		// department a person is in is this app's fact. The manager's *name*
		// is the platform's and is filled in by the module from the
		// directory — see internal/apps/organisation/people.go.
		`SELECT d.id::text, d.code, d.name, COALESCE(d.parent_id::text, ''),
		        COALESCE(d.manager_membership_id::text, ''), '', d.active,
		        (SELECT count(*) FROM organisation_people op
		          WHERE op.department_id = d.id AND op.tenant_id = d.tenant_id),
		        d.tenant_id::text, ''
		   FROM departments d
		  WHERE d.tenant_id = ANY($1::uuid[])
		  ORDER BY d.tenant_id, d.active DESC, d.name`, tenantIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]organisation.Department, 0)
	for rows.Next() {
		var d organisation.Department
		if err := rows.Scan(&d.ID, &d.Code, &d.Name, &d.ParentID, &d.ManagerID,
			&d.ManagerName, &d.Active, &d.PeopleCount, &d.TenantID, &d.TenantName); err != nil {
			return nil, organisation.Failed("could not read the departments", err)
		}
		list = append(list, d)
	}
	if err := rows.Err(); err != nil {
		return nil, organisation.Failed("could not read the departments", err)
	}
	return list, nil
}

func (s *Store) CreateDepartment(ctx context.Context, tenantID string, edit organisation.DepartmentEdit) (string, error) {
	var id string
	err := s.db.QueryRow(ctx,
		`INSERT INTO departments (tenant_id, code, name, parent_id, manager_membership_id)
		 VALUES ($1, $2, $3, $4::uuid, $5::uuid) RETURNING id::text`,
		tenantID, edit.Code, edit.Name, edit.Parent(), edit.Manager()).Scan(&id)
	if isUniqueViolation(err) {
		return "", organisation.ErrDuplicateCode
	}
	if isForeignKeyViolation(err) {
		// The composite foreign keys make this the schema's answer to a parent
		// or a manager from another tenant, rather than a check somebody has to
		// remember to write.
		return "", organisation.ErrForeignUnit
	}
	return id, err
}

func (s *Store) UpdateDepartment(ctx context.Context, tenantID, id string, edit organisation.DepartmentEdit) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE departments SET name = $3, parent_id = $4::uuid,
		        manager_membership_id = $5::uuid, updated_at = NOW()
		 WHERE id = $1 AND tenant_id = $2`,
		id, tenantID, edit.Name, edit.Parent(), edit.Manager())
	if isForeignKeyViolation(err) {
		return false, organisation.ErrForeignUnit
	}
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Store) IsDescendant(ctx context.Context, tenantID, ancestorID, candidateID string) (bool, error) {
	var descendant bool
	err := s.db.QueryRow(ctx,
		`WITH RECURSIVE below AS (
		    SELECT id FROM departments WHERE parent_id = $1 AND tenant_id = $3
		    UNION ALL
		    SELECT d.id FROM departments d JOIN below b ON d.parent_id = b.id
		     WHERE d.tenant_id = $3
		 )
		 SELECT EXISTS (SELECT 1 FROM below WHERE id = $2::uuid)`,
		ancestorID, candidateID, tenantID).Scan(&descendant)
	return descendant, err
}

func (s *Store) Parent(ctx context.Context, tenantID, id string) (string, bool, error) {
	var name string
	var archived bool
	err := s.db.QueryRow(ctx,
		`SELECT p.name, NOT p.active
		   FROM departments d JOIN departments p ON p.id = d.parent_id
		  WHERE d.id = $1 AND d.tenant_id = $2`, id, tenantID).Scan(&name, &archived)
	// No row is a root — or a unit that is not there at all, which the update
	// after this will answer for.
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return name, archived, err
}

func (s *Store) SetDepartmentArchived(ctx context.Context, tenantID, id string, archived bool) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE departments SET active = $3, updated_at = NOW()
		 WHERE id = $1 AND tenant_id = $2`, id, tenantID, !archived)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Store) CountChildren(ctx context.Context, tenantID, id string) (int, int, error) {
	var people, units int
	err := s.db.QueryRow(ctx,
		// organisation_people, not memberships: the department a person is in
		// is this app's fact now, on this app's table. See migration 00076.
		`SELECT (SELECT count(*) FROM organisation_people WHERE department_id = $1 AND tenant_id = $2),
		        (SELECT count(*) FROM departments         WHERE parent_id     = $1 AND tenant_id = $2)`,
		id, tenantID).Scan(&people, &units)
	return people, units, err
}

func (s *Store) DeleteDepartment(ctx context.Context, tenantID, id string) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM departments WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func isUniqueViolation(err error) bool { return sqlState(err) == "23505" }

func isForeignKeyViolation(err error) bool { return sqlState(err) == "23503" }

func sqlState(err error) string {
	if err == nil {
		return ""
	}
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState()
	}
	return ""
}

// The Store answers the department half of the port. The people half is the
// platform's and reaches the domain through the module — see
// internal/apps/organisation/people.go, which is where the assertion now is.

// PersonDetail is what this app knows about a membership: the two things that
// used to be columns on the platform's own table.
type PersonDetail struct {
	JobTitle       string
	DepartmentID   string
	DepartmentName string
}

// Details is the app's half of a staff list, keyed by membership.
//
// The other half — who the person is, which organisation, which roles — is the
// platform's and comes from nexus.Directory. The two are joined in the module,
// which is the only layer allowed to know both; see ADR 0001 and
// internal/apps/organisation/people.go.
func (s *Store) Details(ctx context.Context, tenantIDs []string) (map[string]PersonDetail, error) {
	rows, err := s.db.Query(ctx,
		`SELECT op.membership_id::text, op.job_title,
		        COALESCE(op.department_id::text, ''), COALESCE(d.name, '')
		   FROM organisation_people op
		   LEFT JOIN departments d ON d.id = op.department_id
		  WHERE op.tenant_id = ANY($1::uuid[])`, tenantIDs)
	if err != nil {
		return nil, organisation.Failed("could not read the people", err)
	}
	defer rows.Close()

	details := map[string]PersonDetail{}
	for rows.Next() {
		var id string
		var detail PersonDetail
		if err := rows.Scan(&id, &detail.JobTitle, &detail.DepartmentID, &detail.DepartmentName); err != nil {
			return nil, organisation.Failed("could not read the people", err)
		}
		details[id] = detail
	}
	if err := rows.Err(); err != nil {
		return nil, organisation.Failed("could not read the people", err)
	}
	return details, nil
}
