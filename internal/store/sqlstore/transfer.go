package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/config"
)

type TransferTable struct {
	Name        string `json:"name"`
	SourceRows  int64  `json:"source_rows"`
	TargetRows  int64  `json:"target_rows"`
	SourceMinMS *int64 `json:"source_min_ms,omitempty"`
	SourceMaxMS *int64 `json:"source_max_ms,omitempty"`
	TargetMinMS *int64 `json:"target_min_ms,omitempty"`
	TargetMaxMS *int64 `json:"target_max_ms,omitempty"`
}

type TransferReport struct {
	Tables []TransferTable `json:"tables"`
	Rows   int64           `json:"rows"`
}

var transferTables = []string{
	"settings", "admins", "sessions", "nodes", "agent_credentials", "agent_configs",
	"temporary_enrollment_pins",
	"metrics_raw", "metrics_5m", "metrics_1h", "probe_tasks", "probe_results",
	"alert_rules", "alert_events", "notification_channels", "notification_deliveries",
	"themes", "geoip_cache", "audit_log", "api_keys", "share_links",
}

var booleanColumns = map[string]map[string]bool{
	"nodes":       {"hidden": true, "auto_renewal": true, "location_manual": true, "latency_enabled": true},
	"probe_tasks": {"enabled": true, "public": true}, "probe_results": {"success": true},
	"alert_rules": {"enabled": true}, "notification_channels": {"enabled": true},
}

var timeColumns = map[string]string{
	"metrics_raw": "ts", "metrics_5m": "ts", "metrics_1h": "ts",
	"probe_results": "ts", "alert_events": "occurred_at", "audit_log": "occurred_at",
	"temporary_enrollment_pins": "created_at",
}

func TransferSQLiteToPostgres(ctx context.Context, sourcePath, targetDSN string) (TransferReport, error) {
	source, err := Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: sourcePath})
	if err != nil {
		return TransferReport{}, fmt.Errorf("open source SQLite: %w", err)
	}
	defer source.Close()
	target, err := Open(ctx, config.DatabaseConfig{Driver: "postgres", DSN: targetDSN})
	if err != nil {
		return TransferReport{}, fmt.Errorf("open target PostgreSQL: %w", err)
	}
	defer target.Close()
	for _, table := range []string{"admins", "nodes", "metrics_raw"} {
		var count int64
		if err := target.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			return TransferReport{}, err
		}
		if count != 0 {
			return TransferReport{}, fmt.Errorf("target PostgreSQL table %s is not empty", table)
		}
	}
	if err := target.ensurePartitionsForSource(ctx, source.db); err != nil {
		return TransferReport{}, err
	}
	report := TransferReport{Tables: make([]TransferTable, 0, len(transferTables))}
	for _, table := range transferTables {
		entry, err := copyTable(ctx, source.db, target, table)
		if err != nil {
			return report, fmt.Errorf("copy table %s: %w", table, err)
		}
		report.Tables = append(report.Tables, entry)
		report.Rows += entry.SourceRows
	}
	for _, table := range []string{"probe_tasks", "alert_rules", "notification_channels", "audit_log"} {
		query := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s','id'), COALESCE((SELECT MAX(id) FROM %s), 1), (SELECT COUNT(*) > 0 FROM %s))`, table, table, table)
		if _, err := target.db.ExecContext(ctx, query); err != nil {
			return report, fmt.Errorf("reset sequence for %s: %w", table, err)
		}
	}
	return report, nil
}

func (s *SQLStore) ensurePartitionsForSource(ctx context.Context, source *sql.DB) error {
	var minimum, maximum sql.NullInt64
	if err := source.QueryRowContext(ctx, `SELECT MIN(ts), MAX(ts) FROM (SELECT ts FROM metrics_raw UNION ALL SELECT ts FROM metrics_5m UNION ALL SELECT ts FROM metrics_1h)`).Scan(&minimum, &maximum); err != nil {
		return err
	}
	if !minimum.Valid || !maximum.Valid {
		return nil
	}
	start := time.UnixMilli(minimum.Int64).UTC()
	end := time.UnixMilli(maximum.Int64).UTC()
	months := (end.Year()-start.Year())*12 + int(end.Month()-start.Month())
	if months > 240 {
		return fmt.Errorf("metric history spans %d months; maximum supported offline transfer span is 240", months)
	}
	for offset := 0; offset <= months; offset++ {
		month := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, offset, 0)
		if err := s.ensurePostgresPartitionMonth(ctx, month); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLStore) ensurePostgresPartitionMonth(ctx context.Context, month time.Time) error {
	start := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	for _, table := range []string{"metrics_raw", "metrics_5m", "metrics_1h"} {
		name := fmt.Sprintf("%s_%04d_%02d", table, start.Year(), int(start.Month()))
		query := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%d) TO (%d)`, name, table, start.UnixMilli(), end.UnixMilli())
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("create PostgreSQL metric partition %s: %w", name, err)
		}
	}
	return nil
}

func copyTable(ctx context.Context, source *sql.DB, target *SQLStore, table string) (TransferTable, error) {
	columns, err := sqliteColumns(ctx, source, table)
	if err != nil {
		return TransferTable{}, err
	}
	entry := TransferTable{Name: table}
	if err := source.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&entry.SourceRows); err != nil {
		return entry, err
	}
	if entry.SourceRows > 0 {
		selectQuery := `SELECT ` + strings.Join(columns, ",") + ` FROM ` + table
		rows, err := source.QueryContext(ctx, selectQuery)
		if err != nil {
			return entry, err
		}
		defer rows.Close()
		transaction, err := target.db.BeginTx(ctx, nil)
		if err != nil {
			return entry, err
		}
		placeholders := make([]string, len(columns))
		for index := range placeholders {
			placeholders[index] = fmt.Sprintf("$%d", index+1)
		}
		insert := `INSERT INTO ` + table + `(` + strings.Join(columns, ",") + `) VALUES(` + strings.Join(placeholders, ",") + `)`
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := rows.Scan(pointers...); err != nil {
				transaction.Rollback()
				return entry, err
			}
			for index, column := range columns {
				if raw, ok := values[index].([]byte); ok {
					values[index] = string(raw)
				}
				if booleanColumns[table][column] {
					switch value := values[index].(type) {
					case int64:
						values[index] = value != 0
					case int:
						values[index] = value != 0
					}
				}
			}
			if _, err := transaction.ExecContext(ctx, insert, values...); err != nil {
				transaction.Rollback()
				return entry, err
			}
		}
		if err := rows.Err(); err != nil {
			transaction.Rollback()
			return entry, err
		}
		if err := transaction.Commit(); err != nil {
			return entry, err
		}
	}
	if err := target.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&entry.TargetRows); err != nil {
		return entry, err
	}
	if entry.SourceRows != entry.TargetRows {
		return entry, fmt.Errorf("row count mismatch: source=%d target=%d", entry.SourceRows, entry.TargetRows)
	}
	if column := timeColumns[table]; column != "" && entry.SourceRows > 0 {
		var sourceMin, sourceMax, targetMin, targetMax int64
		if err := source.QueryRowContext(ctx, `SELECT MIN(`+column+`), MAX(`+column+`) FROM `+table).Scan(&sourceMin, &sourceMax); err != nil {
			return entry, err
		}
		if err := target.db.QueryRowContext(ctx, `SELECT MIN(`+column+`), MAX(`+column+`) FROM `+table).Scan(&targetMin, &targetMax); err != nil {
			return entry, err
		}
		entry.SourceMinMS, entry.SourceMaxMS = &sourceMin, &sourceMax
		entry.TargetMinMS, entry.TargetMaxMS = &targetMin, &targetMax
		if sourceMin != targetMin || sourceMax != targetMax {
			return entry, fmt.Errorf("time range mismatch")
		}
	}
	return entry, nil
}

func sqliteColumns(ctx context.Context, database *sql.DB, table string) ([]string, error) {
	rows, err := database.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make([]string, 0)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	return columns, rows.Err()
}
