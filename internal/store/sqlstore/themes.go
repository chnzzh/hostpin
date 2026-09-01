package sqlstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
)

func scanTheme(row rowScanner) (model.Theme, error) {
	var theme model.Theme
	var manifest, settings string
	var installedAt, updatedAt int64
	err := row.Scan(&manifest, &settings, &theme.SourceURL, &theme.Checksum, &installedAt, &updatedAt)
	if err != nil {
		return model.Theme{}, mapSQLError(err)
	}
	if err := json.Unmarshal([]byte(manifest), &theme.Manifest); err != nil {
		return model.Theme{}, err
	}
	_ = json.Unmarshal([]byte(settings), &theme.Settings)
	if theme.Settings == nil {
		theme.Settings = map[string]any{}
	}
	theme.Installed, theme.Updated = timeFromMillis(installedAt), timeFromMillis(updatedAt)
	return theme, nil
}

func (s *SQLStore) ListThemes(ctx context.Context) ([]model.Theme, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT manifest_json, settings_json, source_url, checksum, installed_at, updated_at FROM themes ORDER BY short`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	themes := make([]model.Theme, 0)
	for rows.Next() {
		theme, err := scanTheme(rows)
		if err != nil {
			return nil, err
		}
		themes = append(themes, theme)
	}
	return themes, rows.Err()
}

func (s *SQLStore) GetTheme(ctx context.Context, short string) (model.Theme, error) {
	return scanTheme(s.db.QueryRowContext(ctx, s.q(`SELECT manifest_json, settings_json, source_url, checksum, installed_at, updated_at FROM themes WHERE short = ?`), short))
}

func (s *SQLStore) SaveTheme(ctx context.Context, theme model.Theme) error {
	now := time.Now().UTC()
	if theme.Installed.IsZero() {
		theme.Installed = now
	}
	theme.Updated = now
	query := `INSERT INTO themes(short, manifest_json, settings_json, source_url, checksum, installed_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(short) DO UPDATE SET manifest_json=excluded.manifest_json, settings_json=excluded.settings_json, source_url=excluded.source_url, checksum=excluded.checksum, updated_at=excluded.updated_at`
	_, err := s.db.ExecContext(ctx, s.q(query), theme.Manifest.Short, encodeJSON(theme.Manifest),
		encodeJSON(theme.Settings), theme.SourceURL, theme.Checksum, millis(theme.Installed), millis(theme.Updated))
	return err
}

func (s *SQLStore) DeleteTheme(ctx context.Context, short string) error {
	result, err := s.db.ExecContext(ctx, s.q(`DELETE FROM themes WHERE short = ?`), short)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return store.ErrNotFound
	}
	return nil
}
