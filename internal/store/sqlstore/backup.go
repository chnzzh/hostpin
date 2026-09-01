package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	modernsqlite "modernc.org/sqlite"
)

// BackupSQLite writes a transactionally consistent online snapshot. The
// destination must not exist so callers cannot accidentally overwrite an
// operator-managed file.
func (s *SQLStore) BackupSQLite(ctx context.Context, destination string) (finalErr error) {
	if s.driver != "sqlite" {
		return errors.New("online backup is available only for SQLite")
	}
	if _, err := os.Lstat(destination); err == nil {
		return errors.New("backup destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect backup destination: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	defer func() {
		if finalErr != nil {
			_ = os.Remove(destination)
		}
	}()

	type backuper interface {
		NewBackup(string) (*modernsqlite.Backup, error)
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve SQLite connection: %w", err)
	}
	defer connection.Close()
	return connection.Raw(func(driverConnection any) error {
		provider, ok := driverConnection.(backuper)
		if !ok {
			return errors.New("SQLite driver does not provide online backup")
		}
		operation, err := provider.NewBackup(filepath.ToSlash(destination))
		if err != nil {
			return fmt.Errorf("start SQLite backup: %w", err)
		}
		finished := false
		defer func() {
			if !finished {
				_ = operation.Finish()
			}
		}()
		for more := true; more; {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, err = operation.Step(256)
			if err != nil {
				return fmt.Errorf("copy SQLite pages: %w", err)
			}
		}
		if err := operation.Finish(); err != nil {
			return fmt.Errorf("finish SQLite backup: %w", err)
		}
		finished = true
		return nil
	})
}
