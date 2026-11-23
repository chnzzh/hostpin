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
	"sync"
	"time"

	"github.com/chnzzh/hostpin/internal/config"
	"github.com/chnzzh/hostpin/internal/store"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

const (
	sqliteWriterParams = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	sqliteMemoryParams = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	sqliteReaderParams = "mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-16000)"
)

type SQLStore struct {
	db           *sql.DB
	readDB       *sql.DB
	driver       string
	sqliteFile   bool
	rollupMu     sync.Mutex
	lastRollup5m time.Time
	lastRollup1h time.Time
}

func isSQLiteMemoryDSN(dsn string) bool {
	trimmed := strings.TrimSpace(dsn)
	lower := strings.ToLower(trimmed)
	return trimmed == "" ||
		lower == ":memory:" ||
		strings.HasPrefix(lower, "file::memory:") ||
		strings.Contains(lower, "mode=memory")
}

func appendDSNParams(dsn, params string) string {
	if strings.Contains(dsn, "?") {
		return dsn + "&" + params
	}
	return dsn + "?" + params
}

func sqliteDirFromDSN(dsn string) string {
	raw := strings.TrimSpace(dsn)
	if strings.HasPrefix(raw, "file:") {
		pathPart, _, _ := strings.Cut(strings.TrimPrefix(raw, "file:"), "?")
		if pathPart == "" {
			return ""
		}
		return filepath.Dir(filepath.FromSlash(pathPart))
	}
	return filepath.Dir(raw)
}

func Open(ctx context.Context, cfg config.DatabaseConfig) (*SQLStore, error) {
	driver := strings.ToLower(strings.TrimSpace(cfg.Driver))
	openDriver := "sqlite"
	dsn := cfg.DSN
	var baseSQLiteDSN string
	sqliteFile := false
	if driver == "postgresql" {
		driver = "postgres"
	}
	if driver == "postgres" {
		openDriver = "pgx"
	} else {
		driver = "sqlite"
		sqliteFile = !isSQLiteMemoryDSN(cfg.DSN)
		if sqliteFile {
			if dir := sqliteDirFromDSN(cfg.DSN); dir != "" {
				if err := os.MkdirAll(dir, 0o750); err != nil {
					return nil, fmt.Errorf("create sqlite directory: %w", err)
				}
			}
		}
		baseSQLiteDSN = strings.TrimSpace(dsn)
		if !strings.HasPrefix(baseSQLiteDSN, "file:") {
			if sqliteFile {
				baseSQLiteDSN = "file:" + filepath.ToSlash(baseSQLiteDSN)
			} else {
				baseSQLiteDSN = "file:" + baseSQLiteDSN
			}
		}
		if sqliteFile {
			dsn = appendDSNParams(baseSQLiteDSN, sqliteWriterParams)
		} else {
			dsn = appendDSNParams(baseSQLiteDSN, sqliteMemoryParams)
		}
	}

	db, err := sql.Open(openDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if driver == "sqlite" {
		// SQLite permits concurrent readers in WAL mode, while writer transactions
		// serialize cleanly through a dedicated single-connection writer pool.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(24)
		db.SetMaxIdleConns(8)
		db.SetConnMaxIdleTime(5 * time.Minute)
	}

	s := &SQLStore{db: db, readDB: db, driver: driver, sqliteFile: sqliteFile}
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
	if sqliteFile {
		readDSN := appendDSNParams(baseSQLiteDSN, sqliteReaderParams)
		readDB, err := sql.Open(openDriver, readDSN)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("open sqlite reader pool: %w", err)
		}
		readDB.SetMaxOpenConns(6)
		readDB.SetMaxIdleConns(4)
		readDB.SetConnMaxIdleTime(5 * time.Minute)
		if err := readDB.PingContext(ctx); err != nil {
			readDB.Close()
			db.Close()
			return nil, fmt.Errorf("ping sqlite reader pool: %w", err)
		}
		s.readDB = readDB
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

func (s *SQLStore) Driver() string { return s.driver }

func (s *SQLStore) Close() error {
	var readErr error
	if s.readDB != nil && s.readDB != s.db {
		readErr = s.readDB.Close()
	}
	if s.sqliteFile && s.db != nil {
		checkpointCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = s.db.ExecContext(checkpointCtx, `PRAGMA wal_checkpoint(TRUNCATE)`)
		cancel()
	}
	var writeErr error
	if s.db != nil {
		writeErr = s.db.Close()
	}
	return errors.Join(readErr, writeErr)
}

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
