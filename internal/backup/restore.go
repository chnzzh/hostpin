package backup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/config"
	"github.com/google/uuid"
)

type RestoreReceipt struct {
	Manifest       Manifest  `json:"manifest"`
	Actor          string    `json:"actor"`
	AppliedAt      time.Time `json:"applied_at"`
	DatabaseSaved  string    `json:"database_saved,omitempty"`
	MasterKeySaved string    `json:"master_key_saved,omitempty"`
	ThemesSaved    string    `json:"themes_saved,omitempty"`
}

func ApplyPendingRestore(ctx context.Context, cfg config.Config) (_ *RestoreReceipt, finalErr error) {
	pendingRoot := filepath.Join(cfg.DataDir, pendingDirectory)
	if err := validatePendingTree(pendingRoot); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, err)
	}
	metadataPath := filepath.Join(pendingRoot, pendingMetadata)
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("read pending restore: %w", err)
	}
	if cfg.Database.Driver != "sqlite" || !plainSQLitePath(cfg.Database.DSN) {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, ErrBackupUnsupported)
	}
	var pending PendingRestore
	if len(metadataBytes) > int(maxManifestBytes) || json.Unmarshal(metadataBytes, &pending) != nil ||
		pending.Manifest.Format != archiveFormat || pending.Manifest.Version != archiveVersion ||
		pending.Manifest.SourceDriver != "sqlite" || pending.Manifest.CreatedAt.IsZero() {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, fmt.Errorf("%w: pending restore metadata is invalid", ErrInvalidBackup))
	}
	if err := verifyPendingChecksums(pendingRoot, pending.Manifest); err != nil {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, err)
	}
	backupKey, err := validateRestoredDatabase(ctx, pendingRoot)
	if err != nil {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, err)
	}
	if cfg.Security.MasterKey != "" {
		configured, decodeErr := base64.StdEncoding.DecodeString(cfg.Security.MasterKey)
		if decodeErr != nil || !equalBytes(configured, backupKey) {
			return nil, quarantineRestore(cfg.DataDir, pendingRoot, errors.New("configured master key does not match pending backup"))
		}
	}

	databasePath, err := filepath.Abs(cfg.Database.DSN)
	if err != nil {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, err)
	}
	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, err)
	}
	identifier := uuid.NewString()
	timestamp := time.Now().UTC().Format("20060102T150405Z")
	preparedDatabase := filepath.Join(filepath.Dir(databasePath), "."+filepath.Base(databasePath)+".restore-new-"+identifier)
	preparedKey := filepath.Join(dataDir, ".master.key.restore-new-"+identifier)
	preparedThemes := filepath.Join(dataDir, ".themes.restore-new-"+identifier)
	for _, item := range []string{preparedDatabase, preparedKey, preparedThemes} {
		defer os.RemoveAll(item)
	}
	if err := copyRegularFile(filepath.Join(pendingRoot, "hostpin.db"), preparedDatabase, 0o600); err != nil {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, fmt.Errorf("prepare restored database: %w", err))
	}
	if err := copyRegularFile(filepath.Join(pendingRoot, "master.key"), preparedKey, 0o600); err != nil {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, fmt.Errorf("prepare restored master key: %w", err))
	}
	if err := copyRegularTree(filepath.Join(pendingRoot, "themes"), preparedThemes); err != nil {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, fmt.Errorf("prepare restored themes: %w", err))
	}

	receipt := &RestoreReceipt{Manifest: pending.Manifest, Actor: pending.Actor, AppliedAt: time.Now().UTC()}
	databaseRollback := databasePath + ".pre-restore-" + timestamp + "-" + identifier
	keyPath := filepath.Join(dataDir, "master.key")
	keyRollback := keyPath + ".pre-restore-" + timestamp + "-" + identifier
	themesPath := filepath.Join(dataDir, "themes")
	themesRollback := themesPath + ".pre-restore-" + timestamp + "-" + identifier
	type movedPath struct{ current, rollback string }
	moved := make([]movedPath, 0, 5)
	activated := make([]string, 0, 3)
	rollback := func(applyErr error) error {
		var rollbackErrors []string
		for index := len(activated) - 1; index >= 0; index-- {
			if err := os.RemoveAll(activated[index]); err != nil && !errors.Is(err, os.ErrNotExist) {
				rollbackErrors = append(rollbackErrors, err.Error())
			}
		}
		for index := len(moved) - 1; index >= 0; index-- {
			item := moved[index]
			if err := os.Rename(item.rollback, item.current); err != nil {
				rollbackErrors = append(rollbackErrors, err.Error())
			}
		}
		if len(rollbackErrors) > 0 {
			return fmt.Errorf("%w; rollback also failed: %s", applyErr, strings.Join(rollbackErrors, "; "))
		}
		return applyErr
	}
	moveExisting := func(current, saved string) (bool, error) {
		if _, err := os.Lstat(current); errors.Is(err, os.ErrNotExist) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		if err := os.Rename(current, saved); err != nil {
			return false, err
		}
		moved = append(moved, movedPath{current: current, rollback: saved})
		return true, nil
	}
	databaseMoved, err := moveExisting(databasePath, databaseRollback)
	if err != nil {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, rollback(fmt.Errorf("save current database: %w", err)))
	}
	if databaseMoved {
		receipt.DatabaseSaved = databaseRollback
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := moveExisting(databasePath+suffix, databaseRollback+suffix); err != nil {
			return nil, quarantineRestore(cfg.DataDir, pendingRoot, rollback(fmt.Errorf("save SQLite sidecar: %w", err)))
		}
	}
	if err := os.Rename(preparedDatabase, databasePath); err != nil {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, rollback(fmt.Errorf("activate restored database: %w", err)))
	}
	activated = append(activated, databasePath)
	keyMoved, err := moveExisting(keyPath, keyRollback)
	if err != nil {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, rollback(fmt.Errorf("save current master key: %w", err)))
	}
	if keyMoved {
		receipt.MasterKeySaved = keyRollback
	}
	if err := os.Rename(preparedKey, keyPath); err != nil {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, rollback(fmt.Errorf("activate restored master key: %w", err)))
	}
	activated = append(activated, keyPath)
	themesMoved, err := moveExisting(themesPath, themesRollback)
	if err != nil {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, rollback(fmt.Errorf("save current themes: %w", err)))
	}
	if themesMoved {
		receipt.ThemesSaved = themesRollback
	}
	if err := os.Rename(preparedThemes, themesPath); err != nil {
		return nil, quarantineRestore(cfg.DataDir, pendingRoot, rollback(fmt.Errorf("activate restored themes: %w", err)))
	}
	activated = append(activated, themesPath)
	if err := os.RemoveAll(pendingRoot); err != nil {
		return receipt, fmt.Errorf("restore applied but pending workspace cleanup failed: %w", err)
	}
	return receipt, nil
}

func validatePendingTree(root string) error {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: pending restore root is not a regular directory", ErrInvalidBackup)
	}
	files := 0
	var total int64
	return filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("%w: pending restore contains a non-regular path", ErrInvalidBackup)
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		allowed := relative == pendingMetadata || relative == "hostpin.db" || relative == "master.key" ||
			relative == "hostpin.db-wal" || relative == "hostpin.db-shm" || relative == "themes" || strings.HasPrefix(relative, "themes/")
		if !allowed {
			return fmt.Errorf("%w: unexpected pending restore path %s", ErrInvalidBackup, relative)
		}
		if info.Mode().IsRegular() {
			files++
			total += info.Size()
			if files > maxArchiveFiles+4 || total > maxArchiveBytes {
				return fmt.Errorf("%w: pending restore exceeds limits", ErrInvalidBackup)
			}
		}
		return nil
	})
}

func verifyPendingChecksums(root string, manifest Manifest) error {
	expected := make(map[string]FileRecord, len(manifest.Files))
	for _, record := range manifest.Files {
		if !safePayloadPath(record.Path) {
			return fmt.Errorf("%w: unsafe pending manifest path", ErrInvalidBackup)
		}
		if _, duplicate := expected[record.Path]; duplicate {
			return fmt.Errorf("%w: duplicate pending manifest path", ErrInvalidBackup)
		}
		expected[record.Path] = record
	}
	actual := make(map[string]struct{}, len(expected))
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root || entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if name == pendingMetadata || name == "hostpin.db-wal" || name == "hostpin.db-shm" {
			return nil
		}
		record, exists := expected[name]
		if !exists {
			return fmt.Errorf("%w: undeclared pending file %s", ErrInvalidBackup, name)
		}
		observed, err := (archiveSource{name: name, diskPath: current}).record()
		if err != nil {
			return err
		}
		if observed.Size != record.Size || !strings.EqualFold(observed.SHA256, record.SHA256) {
			return fmt.Errorf("%w: pending checksum mismatch for %s", ErrInvalidBackup, name)
		}
		actual[name] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("%w: pending restore file set is incomplete", ErrInvalidBackup)
	}
	return nil
}

func plainSQLitePath(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && !strings.HasPrefix(strings.ToLower(trimmed), "file:") &&
		!strings.ContainsAny(trimmed, "?\x00")
}

func copyRegularFile(source, destination string, mode os.FileMode) (finalErr error) {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("source is not a regular file")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := output.Close(); finalErr == nil {
			finalErr = closeErr
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	return output.Sync()
}

func copyRegularTree(source, destination string) error {
	if err := os.MkdirAll(destination, 0o750); err != nil {
		return err
	}
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(source, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return errors.New("restore themes contain a non-regular path")
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		return copyRegularFile(current, target, 0o640)
	})
}

func quarantineRestore(dataDir, pendingRoot string, cause error) error {
	failed := filepath.Join(dataDir, ".restore-failed-"+uuid.NewString())
	if err := os.Rename(pendingRoot, failed); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w; could not quarantine pending restore: %v", cause, err)
	}
	return fmt.Errorf("%w; staged files retained at %s", cause, failed)
}
