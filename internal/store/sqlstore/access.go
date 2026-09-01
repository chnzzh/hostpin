package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
)

func (s *SQLStore) ListSessions(ctx context.Context, adminID string) ([]store.Session, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT token_hash, admin_id, csrf_hash, ip_address, user_agent, created_at, expires_at FROM sessions WHERE admin_id = ? ORDER BY created_at DESC`), adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := make([]store.Session, 0)
	for rows.Next() {
		var session store.Session
		var createdAt, expiresAt int64
		if err := rows.Scan(&session.TokenHash, &session.AdminID, &session.CSRFHash, &session.IPAddress,
			&session.UserAgent, &createdAt, &expiresAt); err != nil {
			return nil, err
		}
		session.CreatedAt, session.ExpiresAt = timeFromMillis(createdAt), timeFromMillis(expiresAt)
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *SQLStore) DeleteAdminSessions(ctx context.Context, adminID, exceptHash string) error {
	if exceptHash == "" {
		_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM sessions WHERE admin_id = ?`), adminID)
		return err
	}
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM sessions WHERE admin_id = ? AND token_hash <> ?`), adminID, exceptHash)
	return err
}

func (s *SQLStore) CreateAPIKey(ctx context.Context, record store.APIKeyRecord) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO api_keys(id, admin_id, name, token_id, token_hash, scopes_json, last_used_at, expires_at, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		record.Key.ID, record.AdminID, record.Key.Name, record.TokenID, record.TokenHash,
		encodeJSON(record.Key.Scopes), nullableMillis(record.Key.LastUsedAt), nullableMillis(record.Key.ExpiresAt), millis(record.Key.CreatedAt))
	return err
}

func (s *SQLStore) ListAPIKeys(ctx context.Context, adminID string) ([]model.APIKey, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id, name, scopes_json, last_used_at, expires_at, created_at FROM api_keys WHERE admin_id = ? ORDER BY created_at DESC`), adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]model.APIKey, 0)
	for rows.Next() {
		var key model.APIKey
		var scopes string
		var lastUsedAt, expiresAt sql.NullInt64
		var createdAt int64
		if err := rows.Scan(&key.ID, &key.Name, &scopes, &lastUsedAt, &expiresAt, &createdAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(scopes), &key.Scopes)
		if key.Scopes == nil {
			key.Scopes = []string{}
		}
		if lastUsedAt.Valid {
			value := timeFromMillis(lastUsedAt.Int64)
			key.LastUsedAt = &value
		}
		if expiresAt.Valid {
			value := timeFromMillis(expiresAt.Int64)
			key.ExpiresAt = &value
		}
		key.CreatedAt = timeFromMillis(createdAt)
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *SQLStore) AuthenticateAPIKey(ctx context.Context, tokenID, tokenHash string, now time.Time) (store.Admin, model.APIKey, error) {
	var record store.APIKeyRecord
	var scopes string
	var lastUsedAt, expiresAt sql.NullInt64
	var createdAt int64
	err := s.db.QueryRowContext(ctx, s.q(`SELECT id, admin_id, name, token_id, token_hash, scopes_json, last_used_at, expires_at, created_at FROM api_keys WHERE token_id = ?`), tokenID).
		Scan(&record.Key.ID, &record.AdminID, &record.Key.Name, &record.TokenID, &record.TokenHash,
			&scopes, &lastUsedAt, &expiresAt, &createdAt)
	if err != nil || !constantTimeEqual(record.TokenHash, tokenHash) {
		return store.Admin{}, model.APIKey{}, store.ErrUnauthorized
	}
	_ = json.Unmarshal([]byte(scopes), &record.Key.Scopes)
	if expiresAt.Valid {
		value := timeFromMillis(expiresAt.Int64)
		record.Key.ExpiresAt = &value
		if !now.Before(value) {
			return store.Admin{}, model.APIKey{}, store.ErrUnauthorized
		}
	}
	if lastUsedAt.Valid {
		value := timeFromMillis(lastUsedAt.Int64)
		record.Key.LastUsedAt = &value
	}
	record.Key.CreatedAt = timeFromMillis(createdAt)
	admin, err := s.GetAdminByID(ctx, record.AdminID)
	if err != nil {
		return store.Admin{}, model.APIKey{}, store.ErrUnauthorized
	}
	_, _ = s.db.ExecContext(ctx, s.q(`UPDATE api_keys SET last_used_at = ? WHERE id = ?`), millis(now), record.Key.ID)
	return admin, record.Key, nil
}

func (s *SQLStore) DeleteAPIKey(ctx context.Context, adminID, id string) error {
	result, err := s.db.ExecContext(ctx, s.q(`DELETE FROM api_keys WHERE id = ? AND admin_id = ?`), id, adminID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *SQLStore) CreateShareLink(ctx context.Context, record store.ShareLinkRecord) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO share_links(id, token_hash, node_ids, expires_at, created_at, revoked_at) VALUES(?, ?, ?, ?, ?, ?)`),
		record.Link.ID, record.TokenHash, encodeJSON(record.Link.NodeIDs), millis(record.Link.ExpiresAt), millis(record.Link.CreatedAt), nullableMillis(record.Link.RevokedAt))
	return err
}

func (s *SQLStore) ListShareLinks(ctx context.Context) ([]model.ShareLink, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_ids, expires_at, created_at, revoked_at FROM share_links ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	links := make([]model.ShareLink, 0)
	for rows.Next() {
		link, err := scanShareLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func scanShareLink(row rowScanner) (model.ShareLink, error) {
	var link model.ShareLink
	var nodes string
	var expiresAt, createdAt int64
	var revokedAt sql.NullInt64
	if err := row.Scan(&link.ID, &nodes, &expiresAt, &createdAt, &revokedAt); err != nil {
		return model.ShareLink{}, mapSQLError(err)
	}
	_ = json.Unmarshal([]byte(nodes), &link.NodeIDs)
	if link.NodeIDs == nil {
		link.NodeIDs = []string{}
	}
	link.ExpiresAt, link.CreatedAt = timeFromMillis(expiresAt), timeFromMillis(createdAt)
	if revokedAt.Valid {
		value := timeFromMillis(revokedAt.Int64)
		link.RevokedAt = &value
	}
	return link, nil
}

func (s *SQLStore) ResolveShareLink(ctx context.Context, tokenHash string, now time.Time) (model.ShareLink, error) {
	link, err := scanShareLink(s.db.QueryRowContext(ctx, s.q(`SELECT id, node_ids, expires_at, created_at, revoked_at FROM share_links WHERE token_hash = ?`), tokenHash))
	if err != nil || link.RevokedAt != nil || !now.Before(link.ExpiresAt) {
		return model.ShareLink{}, store.ErrNotFound
	}
	return link, nil
}

func (s *SQLStore) RevokeShareLink(ctx context.Context, id string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE share_links SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`), millis(now), id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return store.ErrNotFound
	}
	return nil
}
