package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/config"
	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/chnzzh/hostpin/internal/store/sqlstore"
	"github.com/google/uuid"
)

const testBackupPassphrase = "correct horse battery staple"

func TestEncryptedContainerRoundTripAndTamperDetection(t *testing.T) {
	payload := bytes.Repeat([]byte("hostpin-backup-payload\n"), 100_000)
	var encrypted bytes.Buffer
	if err := encryptContainer(&encrypted, bytes.NewReader(payload), testBackupPassphrase); err != nil {
		t.Fatal(err)
	}
	var decrypted bytes.Buffer
	if err := decryptContainer(&decrypted, bytes.NewReader(encrypted.Bytes()), testBackupPassphrase, int64(len(payload)+1)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted.Bytes(), payload) {
		t.Fatal("decrypted backup payload differs from source")
	}
	if err := decryptContainer(io.Discard, bytes.NewReader(encrypted.Bytes()), "wrong backup passphrase", int64(len(payload)+1)); !errors.Is(err, ErrInvalidPassphrase) {
		t.Fatalf("wrong passphrase returned %v", err)
	}
	tampered := append([]byte(nil), encrypted.Bytes()...)
	tampered[len(tampered)-8] ^= 0x40
	if err := decryptContainer(io.Discard, bytes.NewReader(tampered), testBackupPassphrase, int64(len(payload)+1)); err == nil {
		t.Fatal("tampered encrypted backup was accepted")
	}
}

func TestSQLiteExportStageAndRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "hostpin.db")
	masterKey := bytes.Repeat([]byte{0x2a}, 32)
	if err := os.WriteFile(filepath.Join(dataDir, "master.key"), []byte(base64.StdEncoding.EncodeToString(masterKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := sqlstore.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	passwordHash, err := security.HashPassword("backup-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	admin := store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: passwordHash, CreatedAt: now, UpdatedAt: now}
	settings := model.DefaultSiteSettings()
	settings.Name = "BACKUP SOURCE"
	if err := repository.Initialize(ctx, admin, "pin-hash", settings); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateSession(ctx, store.Session{
		TokenHash: "old-session", AdminID: admin.ID, CSRFHash: "csrf", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnrollNode(ctx, store.EnrollParams{
		Request: model.EnrollmentRequest{InstallID: uuid.NewString(), Metadata: model.EnrollmentMetadata{Name: "backup-node"}, Config: model.DefaultAgentConfig()},
		NodeID:  uuid.NewString(), TokenID: "backup-token", TokenHash: "backup-hash", Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	themeFile := filepath.Join(dataDir, "themes", "sample", "dist", "index.html")
	if err := os.MkdirAll(filepath.Dir(themeFile), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(themeFile, []byte("original theme"), 0o640); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(dataDir, databasePath, "test-version", masterKey, false, repository)
	exported, err := manager.Export(ctx, testBackupPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer exported.Cleanup()
	mismatchDir := t.TempDir()
	mismatchManager := NewManager(mismatchDir, filepath.Join(mismatchDir, "hostpin.db"), "test-version", bytes.Repeat([]byte{0x7f}, 32), true, stubSnapshotter{})
	mismatchFile, err := os.Open(exported.Path)
	if err != nil {
		t.Fatal(err)
	}
	_, mismatchErr := mismatchManager.StageRestore(ctx, mismatchFile, testBackupPassphrase, "admin")
	mismatchFile.Close()
	if !errors.Is(mismatchErr, ErrMasterKeyMismatch) {
		t.Fatalf("external master-key mismatch returned %v", mismatchErr)
	}
	backupFile, err := os.Open(exported.Path)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := manager.StageRestore(ctx, backupFile, testBackupPassphrase, "admin")
	backupFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SourceDriver != "sqlite" || manifest.HostpinVersion != "test-version" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	changed := settings
	changed.Name = "CHANGED AFTER EXPORT"
	if err := repository.SaveSiteSettings(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(themeFile, []byte("changed theme"), 0o640); err != nil {
		t.Fatal(err)
	}
	differentKey := bytes.Repeat([]byte{0x7f}, 32)
	if err := os.WriteFile(filepath.Join(dataDir, "master.key"), []byte(base64.StdEncoding.EncodeToString(differentKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.Database = config.DatabaseConfig{Driver: "sqlite", DSN: databasePath}
	receipt, err := ApplyPendingRestore(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil || receipt.DatabaseSaved == "" || receipt.MasterKeySaved == "" || receipt.ThemesSaved == "" {
		t.Fatalf("restore did not retain rollback paths: %#v", receipt)
	}
	for _, saved := range []string{receipt.DatabaseSaved, receipt.MasterKeySaved, receipt.ThemesSaved} {
		if _, err := os.Stat(saved); err != nil {
			t.Fatalf("rollback path %s is missing: %v", saved, err)
		}
	}
	restored, err := sqlstore.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restoredSettings, err := restored.SiteSettings(ctx)
	if err != nil || restoredSettings.Name != settings.Name {
		t.Fatalf("settings were not restored: %#v, %v", restoredSettings, err)
	}
	nodes, err := restored.ListNodes(ctx, true)
	if err != nil || len(nodes) != 1 || nodes[0].Name != "backup-node" {
		t.Fatalf("nodes were not restored: %#v, %v", nodes, err)
	}
	sessions, err := restored.ListSessions(ctx, admin.ID)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("restored sessions were not revoked: %#v, %v", sessions, err)
	}
	themeContents, err := os.ReadFile(themeFile)
	if err != nil || string(themeContents) != "original theme" {
		t.Fatalf("theme was not restored: %q, %v", themeContents, err)
	}
	keyContents, err := os.ReadFile(filepath.Join(dataDir, "master.key"))
	if err != nil || strings.TrimSpace(string(keyContents)) != base64.StdEncoding.EncodeToString(masterKey) {
		t.Fatalf("master key was not restored: %q, %v", keyContents, err)
	}
	if pending, _ := filepath.Glob(filepath.Join(dataDir, pendingDirectory)); len(pending) != 0 {
		t.Fatalf("pending restore was not removed: %v", pending)
	}
}

func TestRestoreRejectsArchiveTraversal(t *testing.T) {
	dataDir := t.TempDir()
	var payload bytes.Buffer
	writer := zip.NewWriter(&payload)
	entry, err := writer.Create("../outside")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("escape")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := encryptContainer(&encrypted, bytes.NewReader(payload.Bytes()), testBackupPassphrase); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(dataDir, filepath.Join(dataDir, "hostpin.db"), "test", make([]byte, 32), false, stubSnapshotter{})
	if _, err := manager.StageRestore(context.Background(), bytes.NewReader(encrypted.Bytes()), testBackupPassphrase, "admin"); !errors.Is(err, ErrInvalidBackup) {
		t.Fatalf("archive traversal returned %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dataDir), "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("archive traversal wrote outside the restore workspace")
	}
}

func TestPendingRestoreChecksumsDetectLocalTampering(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"hostpin.db":             []byte("database"),
		"master.key":             []byte("key"),
		"themes/demo/index.html": []byte("theme"),
	}
	manifest := Manifest{Format: archiveFormat, Version: archiveVersion, SourceDriver: "sqlite", CreatedAt: time.Now().UTC()}
	for name, contents := range files {
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		record, err := (archiveSource{name: name, diskPath: target}).record()
		if err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, record)
	}
	if err := verifyPendingChecksums(root, manifest); err != nil {
		t.Fatalf("untampered pending restore failed verification: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "themes", "demo", "index.html"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyPendingChecksums(root, manifest); !errors.Is(err, ErrInvalidBackup) {
		t.Fatalf("tampered pending restore returned %v", err)
	}
}

type stubSnapshotter struct{}

func (stubSnapshotter) Driver() string { return "sqlite" }
func (stubSnapshotter) BackupSQLite(context.Context, string) error {
	return errors.New("not used")
}
