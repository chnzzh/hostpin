package sqlstore

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/config"
	"github.com/chnzzh/hostpin/internal/store"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type SQLStore struct {
	db     *sql.DB
	driver string
}

func Open(ctx context.Context, cfg config.DatabaseConfig) (*SQLStore, error) {
	driver := strings.ToLower(strings.TrimSpace(cfg.Driver))
	openDriver := "sqlite"
	dsn := cfg.DSN
	if driver == "postgresql" {
		driver = "postgres"
	}
	if driver == "postgres" {
		openDriver = "pgx"
	} else {
		driver = "sqlite"
		if err := os.MkdirAll(filepath.Dir(cfg.DSN), 0o750); err != nil {
			return nil, fmt.Errorf("create sqlite directory: %w", err)
		}
		if !strings.HasPrefix(dsn, "file:") {
			dsn = "file:" + filepath.ToSlash(dsn)
		}
		separator := "?"
		if strings.Contains(dsn, "?") {
			separator = "&"
		}
		dsn += separator + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	}

	db, err := sql.Open(openDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if driver == "sqlite" {
		// SQLite permits concurrent readers in WAL mode, but upgrading parallel
		// read transactions to writers can still produce SQLITE_BUSY_SNAPSHOT.
		// database/sql's connection wait queue gives this deployment mode the
		// required single ordered write lane and keeps enrollment/report writes
		// deterministic under burst load.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(24)
		db.SetMaxIdleConns(8)
		db.SetConnMaxIdleTime(5 * time.Minute)
	}

	s := &SQLStore{db: db, driver: driver}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.EnsureMetricPartitions(ctx, time.Now().UTC()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLStore) EnsureMetricPartitions(ctx context.Context, now time.Time) error {
	if s.driver != "postgres" {
		return nil
	}
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for offset := -1; offset <= 2; offset++ {
		if err := s.ensurePostgresPartitionMonth(ctx, month.AddDate(0, offset, 0)); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLStore) Driver() string                 { return s.driver }
func (s *SQLStore) Close() error                   { return s.db.Close() }
func (s *SQLStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *SQLStore) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version BIGINT PRIMARY KEY, applied_at BIGINT NOT NULL)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	migrations, err := store.Migrations(s.driver)
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		var found int
		err := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`), migration.Version).Scan(&found)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", migration.Name, err)
		}
		if found > 0 {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", migration.Name, err)
		}
		for _, statement := range strings.Split(migration.SQL, "-- hostpin:split") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				tx.Rollback()
				return fmt.Errorf("apply migration %s: %w", migration.Name, err)
			}
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`), migration.Version, time.Now().UnixMilli()); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", migration.Name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", migration.Name, err)
		}
	}
	return nil
}

func (s *SQLStore) q(query string) string {
	if s.driver != "postgres" {
		return query
	}
	var b strings.Builder
	index := 1
	for _, r := range query {
		if r == '?' {
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(index))
			index++
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func millis(t time.Time) int64 { return t.UTC().UnixMilli() }

func timeFromMillis(value int64) time.Time {
	return time.UnixMilli(value).UTC()
}

func nullableMillis(t *time.Time) any {
	if t == nil {
		return nil
	}
	return millis(*t)
}

func boolValue(value bool) any {
	if value {
		return 1
	}
	return 0
}

func (s *SQLStore) boolArg(value bool) any {
	if s.driver == "postgres" {
		return value
	}
	return boolValue(value)
}

func encodeJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func decodeJSON[T any](raw string, target *T) {
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), target)
	}
}

func mapSQLError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}

func constantTimeEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
