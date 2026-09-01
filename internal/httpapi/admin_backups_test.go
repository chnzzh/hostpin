package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/backup"
	"github.com/chnzzh/hostpin/internal/config"
	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/chnzzh/hostpin/internal/store/sqlstore"
	"github.com/google/uuid"
)

func TestAdminBackupExportAndImportHandlers(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "hostpin.db")
	masterKey := bytes.Repeat([]byte{0x33}, 32)
	if err := os.WriteFile(filepath.Join(dataDir, "master.key"), []byte(base64.StdEncoding.EncodeToString(masterKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := sqlstore.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	password := "administrator-backup-password"
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	admin := store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: hash, CreatedAt: now, UpdatedAt: now}
	if err := repository.Initialize(ctx, admin, "pin", model.DefaultSiteSettings()); err != nil {
		t.Fatal(err)
	}
	reload := make(chan struct{}, 1)
	manager := backup.NewManager(dataDir, databasePath, "test", masterKey, false, repository)
	api := &API{
		store: repository, backups: manager, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		requestRestore: func() { reload <- struct{}{} }, shutdown: make(chan struct{}),
	}

	wrong := httptest.NewRequest(http.MethodPost, "/api/v1/admin/backups/export", strings.NewReader(`{"current_password":"wrong","passphrase":"correct horse battery staple"}`))
	wrong = wrong.WithContext(context.WithValue(wrong.Context(), adminContextKey{}, admin))
	wrongResponse := httptest.NewRecorder()
	api.handleAdminExportBackup(wrongResponse, wrong)
	if wrongResponse.Code != http.StatusUnauthorized {
		t.Fatalf("wrong export password status = %d", wrongResponse.Code)
	}

	exportRequest := httptest.NewRequest(http.MethodPost, "/api/v1/admin/backups/export", strings.NewReader(`{"current_password":"`+password+`","passphrase":"correct horse battery staple"}`))
	exportRequest = exportRequest.WithContext(context.WithValue(exportRequest.Context(), adminContextKey{}, admin))
	exportResponse := httptest.NewRecorder()
	api.handleAdminExportBackup(exportResponse, exportRequest)
	if exportResponse.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", exportResponse.Code, exportResponse.Body.String())
	}
	if !strings.Contains(exportResponse.Header().Get("Content-Disposition"), ".hostpin-backup") ||
		bytes.Contains(exportResponse.Body.Bytes(), masterKey) {
		t.Fatal("backup response is missing its filename or leaked the raw master key")
	}

	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	for key, value := range map[string]string{
		"current_password": password,
		"passphrase":       "correct horse battery staple",
		"confirmation":     "RESTORE",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	filePart, err := writer.CreateFormFile("backup", "site.hostpin-backup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := filePart.Write(exportResponse.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	importRequest := httptest.NewRequest(http.MethodPost, "/api/v1/admin/backups/import", &multipartBody)
	importRequest.Header.Set("Content-Type", writer.FormDataContentType())
	importRequest = importRequest.WithContext(context.WithValue(importRequest.Context(), adminContextKey{}, admin))
	importResponse := httptest.NewRecorder()
	api.handleAdminImportBackup(importResponse, importRequest)
	if importResponse.Code != http.StatusAccepted {
		t.Fatalf("import status = %d: %s", importResponse.Code, importResponse.Body.String())
	}
	var result map[string]any
	if json.Unmarshal(importResponse.Body.Bytes(), &result) != nil || result["accepted"] != true || result["sessions_revoked"] != true {
		t.Fatalf("unexpected import response: %s", importResponse.Body.String())
	}
	select {
	case <-reload:
	case <-time.After(time.Second):
		t.Fatal("validated import did not request a control-plane reload")
	}
	status := manager.Status()
	if !status.PendingRestore {
		t.Fatal("validated import was not staged")
	}
}
