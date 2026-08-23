/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * The register number — what a person quotes down a telephone.
 *
 * A UUID is perfect for a machine and useless to anybody else. What an officer
 * at a sum actually says is "our fourteenth assignment, the one from the aimag's
 * eighty-seventh", and until now there was nothing on the row that could be
 * said out loud.
 *
 * See migration 00066 for the format and for why there is no platform prefix on
 * it: the link already carries the other installation's name, given by the
 * administrator who established it, and a second human name for the same thing
 * is one that can disagree with the first.
 */

package urtuu

import (
	"context"
	"time"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/urtuu"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	"github.com/jackc/pgx/v5"
)

// nextNumber allocates one register number inside the caller's transaction.
//
// In the caller's transaction on purpose. The counter is a single row and the
// UPDATE takes a row lock, so two people raising work at the same moment
// serialise on it rather than racing — and a transaction that rolls back gives
// its number back rather than leaving a gap in a register somebody audits.
//
// The year comes from the moment of registration rather than from an envelope's
// stamp: a request that arrives on the 31st of December and is registered on
// the 1st of January belongs to the new year's register, which is the year its
// number has to name.
func nextNumber(ctx context.Context, tx pgx.Tx, tenantID, line string, when time.Time) (string, error) {
	line = domain.LineOf(line)
	// The platform's clock. A request registered at half past midnight in
	// Ulaanbaatar on the first of January belongs to the new year's register,
	// and on a UTC container `when.Year()` would have filed it under the old
	// one — for eight hours every New Year, on the numbers people audit.
	year := when.In(nexus.Location()).Year()

	var sequence int
	if err := tx.QueryRow(ctx, `
		INSERT INTO urtuu_numbers (tenant_id, line, year, next)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (tenant_id, line, year)
		DO UPDATE SET next = urtuu_numbers.next + 1
		RETURNING next`, tenantID, line, year).Scan(&sequence); err != nil {
		return "", err
	}

	return domain.FormatNumber(line, year, sequence), nil
}
