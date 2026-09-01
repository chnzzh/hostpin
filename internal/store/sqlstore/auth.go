package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
)

func (s *SQLStore) SetupComplete(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *SQLStore) Initialize(ctx context.Context, admin store.Admin, enrollmentPINHash string, settings model.SiteSettings) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return store.ErrAlreadySetup
	}
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO admins(id, username, password_hash, totp_secret_enc, recovery_hashes, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?)`),
		admin.ID, admin.Username, admin.PasswordHash, admin.TOTPSecretEnc, encodeJSON(admin.RecoveryHashes), millis(admin.CreatedAt), millis(admin.UpdatedAt)); err != nil {
		return fmt.Errorf("create admin: %w", err)
	}
	now := time.Now().UTC().UnixMilli()
	for key, value := range map[string]string{
		"enrollment_pin_hash": enrollmentPINHash,
		"site_settings":       encodeJSON(settings),
		"setup_complete":      "true",
	} {
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO settings(key, value, updated_at) VALUES(?, ?, ?)`), key, value, now); err != nil {
			return fmt.Errorf("save initial setting %s: %w", key, err)
		}
	}
	defaultRules := []model.AlertRule{
		{Name: "Node offline", Metric: "online", Operator: "<", Threshold: 0.5, RecoveryThreshold: 0.5, DurationSeconds: 90, CooldownSeconds: 300, Severity: "critical", Enabled: true},
		{Name: "CPU sustained high", Metric: "cpu", Operator: ">", Threshold: 90, RecoveryThreshold: 80, DurationSeconds: 300, CooldownSeconds: 1800, Severity: "warning", Enabled: true},
		{Name: "Memory sustained high", Metric: "memory", Operator: ">", Threshold: 90, RecoveryThreshold: 80, DurationSeconds: 300, CooldownSeconds: 1800, Severity: "warning", Enabled: true},
		{Name: "Disk capacity high", Metric: "disk", Operator: ">", Threshold: 90, RecoveryThreshold: 85, DurationSeconds: 300, CooldownSeconds: 1800, Severity: "critical", Enabled: true},
	}
	for _, rule := range defaultRules {
		_, err := tx.ExecContext(ctx, s.q(`INSERT INTO alert_rules(name, metric, operator, threshold, recovery_threshold, duration_seconds, cooldown_seconds, severity, scope_json, enabled, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			rule.Name, rule.Metric, rule.Operator, rule.Threshold, rule.RecoveryThreshold,
			rule.DurationSeconds, rule.CooldownSeconds, rule.Severity, encodeJSON(rule.Scope),
			s.boolArg(rule.Enabled), now, now)
		if err != nil {
			return fmt.Errorf("create default alert rule: %w", err)
		}
	}
	return tx.Commit()
}

func (s *SQLStore) GetAdminByUsername(ctx context.Context, username string) (store.Admin, error) {
	return s.scanAdmin(s.db.QueryRowContext(ctx, s.q(`SELECT id, username, password_hash, totp_secret_enc, recovery_hashes, created_at, updated_at FROM admins WHERE username = ?`), username))
}

func (s *SQLStore) GetAdminByID(ctx context.Context, id string) (store.Admin, error) {
	return s.scanAdmin(s.db.QueryRowContext(ctx, s.q(`SELECT id, username, password_hash, totp_secret_enc, recovery_hashes, created_at, updated_at FROM admins WHERE id = ?`), id))
}

func (s *SQLStore) scanAdmin(row *sql.Row) (store.Admin, error) {
	var admin store.Admin
	var recovery string
	var createdAt, updatedAt int64
	if err := row.Scan(&admin.ID, &admin.Username, &admin.PasswordHash, &admin.TOTPSecretEnc, &recovery, &createdAt, &updatedAt); err != nil {
		return store.Admin{}, mapSQLError(err)
	}
	_ = json.Unmarshal([]byte(recovery), &admin.RecoveryHashes)
	admin.CreatedAt = timeFromMillis(createdAt)
	admin.UpdatedAt = timeFromMillis(updatedAt)
	return admin, nil
}

func (s *SQLStore) UpdateAdmin(ctx context.Context, admin store.Admin) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE admins SET username = ?, password_hash = ?, totp_secret_enc = ?, recovery_hashes = ?, updated_at = ? WHERE id = ?`),
		admin.Username, admin.PasswordHash, admin.TOTPSecretEnc, encodeJSON(admin.RecoveryHashes), millis(admin.UpdatedAt), admin.ID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *SQLStore) CreateSession(ctx context.Context, session store.Session) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO sessions(token_hash, admin_id, csrf_hash, ip_address, user_agent, created_at, expires_at) VALUES(?, ?, ?, ?, ?, ?, ?)`),
		session.TokenHash, session.AdminID, session.CSRFHash, session.IPAddress, session.UserAgent, millis(session.CreatedAt), millis(session.ExpiresAt))
	return err
}

func (s *SQLStore) GetSession(ctx context.Context, tokenHash string) (store.Session, error) {
	var session store.Session
	var createdAt, expiresAt int64
	err := s.db.QueryRowContext(ctx, s.q(`SELECT token_hash, admin_id, csrf_hash, ip_address, user_agent, created_at, expires_at FROM sessions WHERE token_hash = ?`), tokenHash).
		Scan(&session.TokenHash, &session.AdminID, &session.CSRFHash, &session.IPAddress, &session.UserAgent, &createdAt, &expiresAt)
	if err != nil {
		return store.Session{}, mapSQLError(err)
	}
	session.CreatedAt = timeFromMillis(createdAt)
	session.ExpiresAt = timeFromMillis(expiresAt)
	if time.Now().After(session.ExpiresAt) {
		_ = s.DeleteSession(ctx, tokenHash)
		return store.Session{}, store.ErrNotFound
	}
	return session, nil
}

func (s *SQLStore) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM sessions WHERE token_hash = ?`), tokenHash)
	return err
}

func (s *SQLStore) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM sessions WHERE expires_at < ?`), millis(now))
	return err
}

func (s *SQLStore) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	if err := s.db.QueryRowContext(ctx, s.q(`SELECT value FROM settings WHERE key = ?`), key).Scan(&value); err != nil {
		return "", mapSQLError(err)
	}
	return value, nil
}

func (s *SQLStore) SetSetting(ctx context.Context, key, value string) error {
	query := `INSERT INTO settings(key, value, updated_at) VALUES(?, ?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`
	_, err := s.db.ExecContext(ctx, s.q(query), key, value, time.Now().UTC().UnixMilli())
	return err
}

func (s *SQLStore) SiteSettings(ctx context.Context) (model.SiteSettings, error) {
	raw, err := s.GetSetting(ctx, "site_settings")
	if errors.Is(err, store.ErrNotFound) {
		return model.DefaultSiteSettings(), nil
	}
	if err != nil {
		return model.SiteSettings{}, err
	}
	settings := model.DefaultSiteSettings()
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return model.SiteSettings{}, fmt.Errorf("decode site settings: %w", err)
	}
	return settings, nil
}

func (s *SQLStore) SaveSiteSettings(ctx context.Context, settings model.SiteSettings) error {
	return s.SetSetting(ctx, "site_settings", encodeJSON(settings))
}
