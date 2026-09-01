package httpapi

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/backup"
	"github.com/chnzzh/hostpin/internal/security"
)

func (a *API) handleAdminBackupStatus(w http.ResponseWriter, r *http.Request) {
	if a.backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backup_unavailable", "backup manager is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": a.backups.Status()})
}

func (a *API) handleAdminExportBackup(w http.ResponseWriter, r *http.Request) {
	if a.backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backup_unavailable", "backup manager is unavailable")
		return
	}
	var request struct {
		CurrentPassword string `json:"current_password"`
		Passphrase      string `json:"passphrase"`
	}
	if !decodeJSON(w, r, &request, 8<<10) {
		return
	}
	admin := adminFromContext(r.Context())
	if !security.VerifyHash(admin.PasswordHash, request.CurrentPassword) {
		writeError(w, http.StatusUnauthorized, "invalid_password", "administrator password is invalid")
		return
	}
	_ = a.store.AppendAudit(r.Context(), admin.Username, "backup.export", "site", "encrypted portable backup", time.Now().UTC())
	exported, err := a.backups.Export(r.Context(), request.Passphrase)
	request.CurrentPassword, request.Passphrase = "", ""
	if err != nil {
		a.writeBackupError(w, err, "could not create backup")
		return
	}
	defer exported.Cleanup()
	file, err := os.Open(exported.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_read_failed", "could not read generated backup")
		return
	}
	defer file.Close()
	if _, err := file.Stat(); err != nil {
		writeError(w, http.StatusInternalServerError, "backup_read_failed", "could not inspect generated backup")
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": exported.Filename})
	w.Header().Set("Content-Type", "application/vnd.hostpin.backup")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("X-Hostpin-Backup-Version", fmt.Sprint(exported.Manifest.Version))
	http.ServeContent(w, r, exported.Filename, exported.Manifest.CreatedAt, file)
}

func (a *API) handleAdminImportBackup(w http.ResponseWriter, r *http.Request) {
	if a.backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backup_unavailable", "backup manager is unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, backup.MaximumUploadBytes)
	parts, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_backup_upload", "backup upload is malformed or exceeds the size limit")
		return
	}
	values := make(map[string]string, 3)
	var manifest backup.Manifest
	admin := adminFromContext(r.Context())
	for {
		part, nextErr := parts.NextPart()
		if errors.Is(nextErr, io.EOF) {
			writeError(w, http.StatusBadRequest, "backup_file_required", "select a Hostpin backup file")
			return
		}
		if nextErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_backup_upload", "backup upload is malformed or exceeds the size limit")
			return
		}
		name := part.FormName()
		if name == "backup" {
			if part.FileName() == "" || values["current_password"] == "" || values["passphrase"] == "" || values["confirmation"] == "" {
				part.Close()
				writeError(w, http.StatusBadRequest, "invalid_backup_upload", "password and confirmation fields must precede the backup file")
				return
			}
			if !security.VerifyHash(admin.PasswordHash, values["current_password"]) {
				part.Close()
				writeError(w, http.StatusUnauthorized, "invalid_password", "administrator password is invalid")
				return
			}
			if strings.TrimSpace(values["confirmation"]) != "RESTORE" {
				part.Close()
				writeError(w, http.StatusBadRequest, "restore_confirmation_required", "type RESTORE to confirm replacement")
				return
			}
			manifest, err = a.backups.StageRestore(r.Context(), part, values["passphrase"], admin.Username)
			part.Close()
			values["current_password"], values["passphrase"] = "", ""
			break
		}
		if name != "current_password" && name != "passphrase" && name != "confirmation" {
			part.Close()
			writeError(w, http.StatusBadRequest, "invalid_backup_upload", "backup upload contains an unknown field")
			return
		}
		if _, duplicate := values[name]; duplicate {
			part.Close()
			writeError(w, http.StatusBadRequest, "invalid_backup_upload", "backup upload contains a duplicate field")
			return
		}
		raw, readErr := io.ReadAll(io.LimitReader(part, 2049))
		part.Close()
		if readErr != nil || len(raw) > 2048 {
			writeError(w, http.StatusBadRequest, "invalid_backup_upload", "backup form field exceeds the size limit")
			return
		}
		values[name] = string(raw)
	}
	if err != nil {
		a.writeBackupError(w, err, "could not validate backup")
		return
	}
	_ = a.store.AppendAudit(r.Context(), admin.Username, "backup.restore.stage", "site", manifest.CreatedAt.Format(time.RFC3339), time.Now().UTC())
	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true, "restart": "automatic", "created_at": manifest.CreatedAt,
		"source_version": manifest.HostpinVersion, "sessions_revoked": true,
	})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if a.requestRestore != nil {
		go func() {
			timer := time.NewTimer(250 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
				a.requestRestore()
			case <-a.shutdown:
			}
		}()
	}
}

func (a *API) writeBackupError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, backup.ErrBackupUnsupported):
		writeError(w, http.StatusConflict, "backup_unsupported", err.Error())
	case errors.Is(err, backup.ErrBackupBusy):
		writeError(w, http.StatusConflict, "backup_busy", err.Error())
	case errors.Is(err, backup.ErrMasterKeyMismatch):
		writeError(w, http.StatusConflict, "backup_master_key_mismatch", err.Error())
	case errors.Is(err, backup.ErrInvalidPassphrase), errors.Is(err, backup.ErrInvalidBackup):
		writeError(w, http.StatusBadRequest, "invalid_backup", err.Error())
	case strings.Contains(err.Error(), "passphrase") || strings.Contains(err.Error(), "restore is already pending"):
		writeError(w, http.StatusBadRequest, "invalid_backup_request", err.Error())
	default:
		a.logger.Error(fallback, "error", err)
		writeError(w, http.StatusInternalServerError, "backup_failed", fallback)
	}
}
