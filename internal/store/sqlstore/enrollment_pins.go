package sqlstore

import (
	"context"
	"database/sql"
	"time"

	"github.com/chnzzh/hostpin/internal/store"
)

const temporaryEnrollmentPINColumns = `id, pin_hash, claimed_install_id, claimed_token_id, created_at, expires_at, used_at, revoked_at`

func scanTemporaryEnrollmentPIN(row rowScanner) (store.TemporaryEnrollmentPIN, error) {
	var pin store.TemporaryEnrollmentPIN
	var createdAt, expiresAt int64
	var usedAt, revokedAt sql.NullInt64
	if err := row.Scan(
		&pin.ID, &pin.PINHash, &pin.ClaimedInstallID, &pin.ClaimedTokenID,
		&createdAt, &expiresAt, &usedAt, &revokedAt,
	); err != nil {
		return store.TemporaryEnrollmentPIN{}, mapSQLError(err)
	}
	pin.CreatedAt = timeFromMillis(createdAt)
	pin.ExpiresAt = timeFromMillis(expiresAt)
	if usedAt.Valid {
		value := timeFromMillis(usedAt.Int64)
		pin.UsedAt = &value
	}
	if revokedAt.Valid {
		value := timeFromMillis(revokedAt.Int64)
		pin.RevokedAt = &value
	}
	return pin, nil
}

func (s *SQLStore) ReplaceTemporaryEnrollmentPIN(ctx context.Context, pin store.TemporaryEnrollmentPIN, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE temporary_enrollment_pins SET revoked_at = ? WHERE revoked_at IS NULL`), millis(now)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO temporary_enrollment_pins(
		id, pin_hash, claimed_install_id, claimed_token_id, created_at, expires_at, used_at, revoked_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`),
		pin.ID, pin.PINHash, pin.ClaimedInstallID, pin.ClaimedTokenID,
		millis(pin.CreatedAt), millis(pin.ExpiresAt), nullableMillis(pin.UsedAt), nullableMillis(pin.RevokedAt),
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) LatestTemporaryEnrollmentPIN(ctx context.Context) (store.TemporaryEnrollmentPIN, error) {
	return scanTemporaryEnrollmentPIN(s.db.QueryRowContext(ctx,
		`SELECT `+temporaryEnrollmentPINColumns+` FROM temporary_enrollment_pins ORDER BY created_at DESC LIMIT 1`,
	))
}

func (s *SQLStore) ActiveTemporaryEnrollmentPIN(ctx context.Context, now time.Time) (store.TemporaryEnrollmentPIN, error) {
	return scanTemporaryEnrollmentPIN(s.db.QueryRowContext(ctx, s.q(
		`SELECT `+temporaryEnrollmentPINColumns+` FROM temporary_enrollment_pins WHERE revoked_at IS NULL AND expires_at > ? ORDER BY created_at DESC LIMIT 1`,
	), millis(now)))
}

func (s *SQLStore) RevokeTemporaryEnrollmentPIN(ctx context.Context, id string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE temporary_enrollment_pins SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`), millis(now), id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func claimTemporaryEnrollmentPIN(ctx context.Context, tx *sql.Tx, query func(string) string, pinID, installID, tokenID string, now time.Time) error {
	result, err := tx.ExecContext(ctx, query(`UPDATE temporary_enrollment_pins
		SET claimed_install_id = ?, claimed_token_id = ?, used_at = COALESCE(used_at, ?)
		WHERE id = ? AND revoked_at IS NULL AND expires_at > ?
		AND (claimed_install_id = '' OR (claimed_install_id = ? AND claimed_token_id = ?))`),
		installID, tokenID, millis(now), pinID, millis(now), installID, tokenID,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return store.ErrTemporaryPINUnavailable
	}
	return nil
}
