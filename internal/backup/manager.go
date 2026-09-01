package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const (
	archiveFormat       = "hostpin-portable-backup"
	archiveVersion      = 1
	maxArchiveBytes     = int64(8 << 30)
	maxArchiveFiles     = 50_000
	maxArchiveFileBytes = int64(6 << 30)
	maxManifestBytes    = int64(1 << 20)
	maxCompressionRatio = uint64(1_000)
	pendingDirectory    = ".restore-pending"
	pendingMetadata     = "restore.json"
	// MaximumUploadBytes includes encrypted framing overhead above the maximum
	// decrypted archive size.
	MaximumUploadBytes = int64(9 << 30)
)

type FileRecord struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Format         string       `json:"format"`
	Version        int          `json:"version"`
	CreatedAt      time.Time    `json:"created_at"`
	HostpinVersion string       `json:"hostpin_version"`
	SourceDriver   string       `json:"source_driver"`
	Files          []FileRecord `json:"files"`
}

type PendingRestore struct {
	Manifest          Manifest  `json:"manifest"`
	Actor             string    `json:"actor"`
	StagedAt          time.Time `json:"staged_at"`
	ExternalMasterKey bool      `json:"external_master_key"`
}

type Status struct {
	Driver            string `json:"driver"`
	Available         bool   `json:"available"`
	Encrypted         bool   `json:"encrypted"`
	FormatVersion     int    `json:"format_version"`
	PendingRestore    bool   `json:"pending_restore"`
	ExternalMasterKey bool   `json:"external_master_key"`
	MaximumBytes      int64  `json:"maximum_bytes"`
}

type Snapshotter interface {
	Driver() string
	BackupSQLite(context.Context, string) error
}

type Manager struct {
	dataDir           string
	databasePath      string
	version           string
	masterKey         []byte
	externalMasterKey bool
	snapshotter       Snapshotter
	operation         sync.Mutex
}

type Export struct {
	Path     string
	Filename string
	Manifest Manifest
	cleanup  func()
}

func NewManager(dataDir, databasePath, version string, masterKey []byte, externalMasterKey bool, snapshotter Snapshotter) *Manager {
	return &Manager{
		dataDir: filepath.Clean(dataDir), databasePath: databasePath, version: version,
		masterKey: append([]byte(nil), masterKey...), externalMasterKey: externalMasterKey,
		snapshotter: snapshotter,
	}
}

func (m *Manager) Status() Status {
	_, pendingErr := os.Stat(filepath.Join(m.dataDir, pendingDirectory, pendingMetadata))
	available := m.snapshotter.Driver() == "sqlite" && plainSQLitePath(m.databasePath)
	return Status{
		Driver: m.snapshotter.Driver(), Available: available,
		Encrypted: true, FormatVersion: archiveVersion, PendingRestore: pendingErr == nil,
		ExternalMasterKey: m.externalMasterKey, MaximumBytes: maxArchiveBytes,
	}
}

func (m *Manager) Export(ctx context.Context, passphrase string) (_ Export, finalErr error) {
	if m.snapshotter.Driver() != "sqlite" || !plainSQLitePath(m.databasePath) {
		return Export{}, ErrBackupUnsupported
	}
	if err := validatePassphrase(passphrase); err != nil {
		return Export{}, err
	}
	if !m.operation.TryLock() {
		return Export{}, ErrBackupBusy
	}
	defer m.operation.Unlock()

	workDirectory, err := os.MkdirTemp(m.dataDir, ".backup-export-")
	if err != nil {
		return Export{}, fmt.Errorf("create backup workspace: %w", err)
	}
	if err := os.Chmod(workDirectory, 0o700); err != nil {
		_ = os.RemoveAll(workDirectory)
		return Export{}, err
	}
	cleanup := func() { _ = os.RemoveAll(workDirectory) }
	defer func() {
		if finalErr != nil {
			cleanup()
		}
	}()

	databaseSnapshot := filepath.Join(workDirectory, "hostpin.db")
	if err := m.snapshotter.BackupSQLite(ctx, databaseSnapshot); err != nil {
		return Export{}, fmt.Errorf("create consistent database snapshot: %w", err)
	}
	masterKeyContents := []byte(base64.StdEncoding.EncodeToString(m.masterKey) + "\n")
	sources := []archiveSource{
		{name: "hostpin.db", diskPath: databaseSnapshot},
		{name: "master.key", contents: masterKeyContents},
	}
	themeSnapshot := filepath.Join(workDirectory, "theme-snapshot")
	themeSources, err := collectThemeSources(filepath.Join(m.dataDir, "themes"), themeSnapshot)
	if err != nil {
		return Export{}, err
	}
	sources = append(sources, themeSources...)
	sort.Slice(sources, func(left, right int) bool { return sources[left].name < sources[right].name })

	manifest := Manifest{
		Format: archiveFormat, Version: archiveVersion, CreatedAt: time.Now().UTC(),
		HostpinVersion: m.version, SourceDriver: m.snapshotter.Driver(),
		Files: make([]FileRecord, 0, len(sources)),
	}
	var total int64
	for _, source := range sources {
		record, err := source.record()
		if err != nil {
			return Export{}, fmt.Errorf("inspect backup file %s: %w", source.name, err)
		}
		if record.Size > maxArchiveFileBytes || total+record.Size > maxArchiveBytes {
			return Export{}, errors.New("backup contents exceed the one-click size limit")
		}
		total += record.Size
		manifest.Files = append(manifest.Files, record)
	}

	zipPath := filepath.Join(workDirectory, "payload.zip")
	if err := writeArchive(zipPath, manifest, sources); err != nil {
		return Export{}, err
	}
	if err := os.Remove(databaseSnapshot); err != nil {
		return Export{}, fmt.Errorf("release database snapshot: %w", err)
	}
	if err := os.RemoveAll(themeSnapshot); err != nil {
		return Export{}, fmt.Errorf("release theme snapshot: %w", err)
	}
	archive, err := os.Open(zipPath)
	if err != nil {
		return Export{}, err
	}
	defer archive.Close()
	identifier, _ := uuid.NewV7()
	backupPath := filepath.Join(workDirectory, identifier.String()+".hostpin-backup")
	destination, err := os.OpenFile(backupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Export{}, err
	}
	if err := encryptContainer(destination, archive, passphrase); err != nil {
		destination.Close()
		return Export{}, fmt.Errorf("encrypt backup: %w", err)
	}
	if err := destination.Sync(); err != nil {
		destination.Close()
		return Export{}, err
	}
	if err := destination.Close(); err != nil {
		return Export{}, err
	}
	if err := archive.Close(); err != nil {
		return Export{}, err
	}
	if err := os.Remove(zipPath); err != nil {
		return Export{}, fmt.Errorf("release archive payload: %w", err)
	}
	filename := "hostpin-" + manifest.CreatedAt.Format("20060102T150405Z") + ".hostpin-backup"
	return Export{Path: backupPath, Filename: filename, Manifest: manifest, cleanup: cleanup}, nil
}

func (e Export) Cleanup() {
	if e.cleanup != nil {
		e.cleanup()
	}
}

func (m *Manager) StageRestore(ctx context.Context, source io.Reader, passphrase, actor string) (Manifest, error) {
	if m.snapshotter.Driver() != "sqlite" || !plainSQLitePath(m.databasePath) {
		return Manifest{}, ErrBackupUnsupported
	}
	if !m.operation.TryLock() {
		return Manifest{}, ErrBackupBusy
	}
	defer m.operation.Unlock()
	if _, err := os.Stat(filepath.Join(m.dataDir, pendingDirectory)); err == nil {
		return Manifest{}, errors.New("a restore is already pending")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, fmt.Errorf("inspect pending restore: %w", err)
	}

	workDirectory, err := os.MkdirTemp(m.dataDir, ".backup-import-")
	if err != nil {
		return Manifest{}, fmt.Errorf("create restore workspace: %w", err)
	}
	if err := os.Chmod(workDirectory, 0o700); err != nil {
		_ = os.RemoveAll(workDirectory)
		return Manifest{}, err
	}
	defer os.RemoveAll(workDirectory)
	zipPath := filepath.Join(workDirectory, "payload.zip")
	decrypted, err := os.OpenFile(zipPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Manifest{}, err
	}
	if err := decryptContainer(decrypted, source, passphrase, maxArchiveBytes); err != nil {
		decrypted.Close()
		return Manifest{}, err
	}
	if err := decrypted.Sync(); err != nil {
		decrypted.Close()
		return Manifest{}, err
	}
	if err := decrypted.Close(); err != nil {
		return Manifest{}, err
	}
	extracted := filepath.Join(workDirectory, "extracted")
	manifest, err := extractAndValidateArchive(zipPath, extracted)
	if err != nil {
		return Manifest{}, err
	}
	backupKey, err := validateRestoredDatabase(ctx, extracted)
	if err != nil {
		return Manifest{}, err
	}
	if err := refreshManifestRecord(&manifest, extracted, "hostpin.db"); err != nil {
		return Manifest{}, err
	}
	if m.externalMasterKey && !equalBytes(backupKey, m.masterKey) {
		return Manifest{}, ErrMasterKeyMismatch
	}
	pending := PendingRestore{
		Manifest: manifest, Actor: strings.TrimSpace(actor), StagedAt: time.Now().UTC(),
		ExternalMasterKey: m.externalMasterKey,
	}
	metadata, _ := json.MarshalIndent(pending, "", "  ")
	if err := os.WriteFile(filepath.Join(extracted, pendingMetadata), append(metadata, '\n'), 0o600); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(extracted, filepath.Join(m.dataDir, pendingDirectory)); err != nil {
		return Manifest{}, fmt.Errorf("stage restore: %w", err)
	}
	return manifest, nil
}

type archiveSource struct {
	name     string
	diskPath string
	contents []byte
}

func (s archiveSource) open() (io.ReadCloser, error) {
	if s.diskPath != "" {
		return os.Open(s.diskPath)
	}
	return io.NopCloser(bytes.NewReader(s.contents)), nil
}

func (s archiveSource) record() (FileRecord, error) {
	reader, err := s.open()
	if err != nil {
		return FileRecord{}, err
	}
	defer reader.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, reader)
	if err != nil {
		return FileRecord{}, err
	}
	return FileRecord{Path: s.name, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func collectThemeSources(root, snapshotRoot string) ([]archiveSource, error) {
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("inspect themes: %w", err)
	}
	result := make([]archiveSource, 0)
	err := filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
			return fmt.Errorf("theme path %s is not a regular file or directory", current)
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		name := "themes/" + filepath.ToSlash(relative)
		if !safeArchivePath(name) {
			return fmt.Errorf("unsafe theme path %s", name)
		}
		snapshot := filepath.Join(snapshotRoot, relative)
		if err := copyRegularFile(current, snapshot, 0o600); err != nil {
			return fmt.Errorf("snapshot theme file %s: %w", relative, err)
		}
		result = append(result, archiveSource{name: name, diskPath: snapshot})
		if len(result) > maxArchiveFiles {
			return errors.New("theme file count exceeds backup limit")
		}
		return nil
	})
	return result, err
}

func writeArchive(destination string, manifest Manifest, sources []archiveSource) (finalErr error) {
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := output.Close(); finalErr == nil {
			finalErr = closeErr
		}
	}()
	writer := zip.NewWriter(output)
	defer func() {
		if closeErr := writer.Close(); finalErr == nil {
			finalErr = closeErr
		}
	}()
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	manifestHeader := &zip.FileHeader{Name: "manifest.json", Method: zip.Deflate}
	manifestHeader.SetMode(0o600)
	manifestEntry, err := writer.CreateHeader(manifestHeader)
	if err != nil {
		return err
	}
	if _, err := manifestEntry.Write(append(manifestBytes, '\n')); err != nil {
		return err
	}
	for _, source := range sources {
		header := &zip.FileHeader{Name: source.name, Method: zip.Deflate}
		header.SetMode(0o600)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		reader, err := source.open()
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(entry, reader)
		closeErr := reader.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func extractAndValidateArchive(zipPath, destination string) (Manifest, error) {
	archive, err := zip.OpenReader(zipPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: payload is not a ZIP archive", ErrInvalidBackup)
	}
	defer archive.Close()
	if len(archive.File) == 0 || len(archive.File) > maxArchiveFiles+1 {
		return Manifest{}, fmt.Errorf("%w: invalid file count", ErrInvalidBackup)
	}
	seen := make(map[string]*zip.File, len(archive.File))
	for _, file := range archive.File {
		if !safeArchivePath(file.Name) || file.FileInfo().Mode()&os.ModeSymlink != 0 || file.FileInfo().IsDir() {
			return Manifest{}, fmt.Errorf("%w: unsafe archive entry %q", ErrInvalidBackup, file.Name)
		}
		if _, exists := seen[file.Name]; exists {
			return Manifest{}, fmt.Errorf("%w: duplicate archive entry %q", ErrInvalidBackup, file.Name)
		}
		if file.UncompressedSize64 > uint64(maxArchiveFileBytes) ||
			(file.CompressedSize64 > 0 && file.UncompressedSize64/file.CompressedSize64 > maxCompressionRatio) {
			return Manifest{}, fmt.Errorf("%w: archive entry exceeds limits", ErrInvalidBackup)
		}
		seen[file.Name] = file
	}
	manifestFile := seen["manifest.json"]
	if manifestFile == nil || manifestFile.UncompressedSize64 > uint64(maxManifestBytes) {
		return Manifest{}, fmt.Errorf("%w: manifest is missing or too large", ErrInvalidBackup)
	}
	reader, err := manifestFile.Open()
	if err != nil {
		return Manifest{}, err
	}
	manifestBytes, readErr := io.ReadAll(io.LimitReader(reader, maxManifestBytes+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || int64(len(manifestBytes)) > maxManifestBytes {
		return Manifest{}, fmt.Errorf("%w: read manifest", ErrInvalidBackup)
	}
	var manifest Manifest
	if json.Unmarshal(manifestBytes, &manifest) != nil || manifest.Format != archiveFormat ||
		manifest.Version != archiveVersion || manifest.SourceDriver != "sqlite" || manifest.CreatedAt.IsZero() {
		return Manifest{}, fmt.Errorf("%w: unsupported manifest", ErrInvalidBackup)
	}
	records := make(map[string]FileRecord, len(manifest.Files))
	var declaredTotal int64
	for _, record := range manifest.Files {
		if !safePayloadPath(record.Path) || record.Size < 0 || record.Size > maxArchiveFileBytes ||
			len(record.SHA256) != sha256.Size*2 {
			return Manifest{}, fmt.Errorf("%w: invalid manifest file", ErrInvalidBackup)
		}
		if _, exists := records[record.Path]; exists {
			return Manifest{}, fmt.Errorf("%w: duplicate manifest path", ErrInvalidBackup)
		}
		records[record.Path] = record
		declaredTotal += record.Size
		if declaredTotal > maxArchiveBytes {
			return Manifest{}, fmt.Errorf("%w: archive exceeds size limit", ErrInvalidBackup)
		}
	}
	if records["hostpin.db"].Path == "" || records["master.key"].Path == "" || len(records)+1 != len(seen) {
		return Manifest{}, fmt.Errorf("%w: required or declared files do not match", ErrInvalidBackup)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return Manifest{}, err
	}
	for name, record := range records {
		file := seen[name]
		if file == nil || int64(file.UncompressedSize64) != record.Size {
			return Manifest{}, fmt.Errorf("%w: file %s is missing or has the wrong size", ErrInvalidBackup, name)
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return Manifest{}, err
		}
		input, err := file.Open()
		if err != nil {
			return Manifest{}, err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			input.Close()
			return Manifest{}, err
		}
		hash := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, record.Size+1))
		closeOutputErr := output.Close()
		closeInputErr := input.Close()
		if copyErr != nil || closeOutputErr != nil || closeInputErr != nil || written != record.Size ||
			!strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), record.SHA256) {
			return Manifest{}, fmt.Errorf("%w: checksum mismatch for %s", ErrInvalidBackup, name)
		}
	}
	return manifest, nil
}

func validateRestoredDatabase(ctx context.Context, extracted string) ([]byte, error) {
	keyContents, err := os.ReadFile(filepath.Join(extracted, "master.key"))
	if err != nil {
		return nil, fmt.Errorf("read backup master key: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyContents)))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%w: master key is invalid", ErrInvalidBackup)
	}
	databasePath := filepath.Join(extracted, "hostpin.db")
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open restored database: %w", err)
	}
	defer database.Close()
	var integrity string
	if err := database.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return nil, fmt.Errorf("%w: SQLite integrity check failed", ErrInvalidBackup)
	}
	for _, table := range []string{"schema_migrations", "settings", "admins", "nodes"} {
		var found int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&found); err != nil || found != 1 {
			return nil, fmt.Errorf("%w: required table %s is missing", ErrInvalidBackup, table)
		}
	}
	migrations, err := store.Migrations("sqlite")
	if err != nil {
		return nil, err
	}
	var latest int64
	for _, migration := range migrations {
		latest = max(latest, migration.Version)
	}
	var schemaVersion sql.NullInt64
	if err := database.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&schemaVersion); err != nil || !schemaVersion.Valid || schemaVersion.Int64 > latest {
		return nil, fmt.Errorf("%w: database schema is newer than this Hostpin version", ErrInvalidBackup)
	}
	var admins int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&admins); err != nil || admins != 1 {
		return nil, fmt.Errorf("%w: backup must contain exactly one administrator", ErrInvalidBackup)
	}
	box, err := security.NewSecretBox(key)
	if err != nil {
		return nil, err
	}
	if err := validateEncryptedColumn(ctx, database, box, `SELECT totp_secret_enc FROM admins WHERE totp_secret_enc <> ''`); err != nil {
		return nil, err
	}
	if err := validateEncryptedColumn(ctx, database, box, `SELECT config_enc FROM notification_channels WHERE config_enc <> ''`); err != nil {
		return nil, err
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return nil, fmt.Errorf("clear restored sessions: %w", err)
	}
	if _, err := database.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return nil, fmt.Errorf("checkpoint restored database: %w", err)
	}
	if err := database.Close(); err != nil {
		return nil, err
	}
	_ = os.Remove(databasePath + "-wal")
	_ = os.Remove(databasePath + "-shm")
	return key, nil
}

func validateEncryptedColumn(ctx context.Context, database *sql.DB, box *security.SecretBox, query string) error {
	rows, err := database.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("read encrypted backup fields: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var encrypted string
		if err := rows.Scan(&encrypted); err != nil {
			return err
		}
		if _, err := box.Open(encrypted); err != nil {
			return fmt.Errorf("%w: master key does not match encrypted database fields", ErrInvalidBackup)
		}
	}
	return rows.Err()
}

func refreshManifestRecord(manifest *Manifest, root, name string) error {
	record, err := (archiveSource{name: name, diskPath: filepath.Join(root, filepath.FromSlash(name))}).record()
	if err != nil {
		return err
	}
	for index := range manifest.Files {
		if manifest.Files[index].Path == name {
			manifest.Files[index] = record
			return nil
		}
	}
	return fmt.Errorf("%w: manifest file %s is missing", ErrInvalidBackup, name)
}

func safeArchivePath(name string) bool {
	return name != "" && !strings.Contains(name, "\\") && !strings.HasPrefix(name, "/") &&
		path.Clean(name) == name && name != "." && name != ".." && !strings.HasPrefix(name, "../")
}

func safePayloadPath(name string) bool {
	return safeArchivePath(name) && (name == "hostpin.db" || name == "master.key" || strings.HasPrefix(name, "themes/"))
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}
