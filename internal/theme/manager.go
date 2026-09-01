package theme

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/google/uuid"
)

const (
	maxArchiveBytes      = 25 << 20
	maxUncompressedBytes = 100 << 20
	maxThemeFiles        = 2000
	maxThemeFileBytes    = 20 << 20
	maxManifestBytes     = 1 << 20
)

var shortPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type repository interface {
	GetTheme(context.Context, string) (model.Theme, error)
	SaveTheme(context.Context, model.Theme) error
	DeleteTheme(context.Context, string) error
}

type Manager struct {
	store repository
	root  string
	http  *http.Client
}

func New(repository repository, dataDir string) (*Manager, error) {
	root := filepath.Join(dataDir, "themes")
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	return &Manager{store: repository, root: root, http: &http.Client{Transport: transport, Timeout: 45 * time.Second}}, nil
}

func (m *Manager) Install(ctx context.Context, archive []byte, expectedChecksum, sourceURL string) (model.Theme, error) {
	if len(archive) == 0 || len(archive) > maxArchiveBytes {
		return model.Theme{}, fmt.Errorf("theme archive must be between 1 byte and %d MiB", maxArchiveBytes>>20)
	}
	sum := sha256.Sum256(archive)
	checksum := hex.EncodeToString(sum[:])
	if expectedChecksum != "" && !strings.EqualFold(strings.TrimSpace(expectedChecksum), checksum) {
		return model.Theme{}, errors.New("theme SHA-256 checksum does not match")
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return model.Theme{}, fmt.Errorf("open theme archive: %w", err)
	}
	if len(reader.File) == 0 || len(reader.File) > maxThemeFiles {
		return model.Theme{}, errors.New("theme archive has an invalid file count")
	}
	entries, manifest, err := validateArchive(reader)
	if err != nil {
		return model.Theme{}, err
	}
	temporary, err := os.MkdirTemp(m.root, ".install-")
	if err != nil {
		return model.Theme{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(temporary)
		}
	}()
	var extracted int64
	for _, entry := range entries {
		written, err := extractEntry(temporary, entry)
		if err != nil {
			return model.Theme{}, err
		}
		extracted += written
		if extracted > maxUncompressedBytes {
			return model.Theme{}, errors.New("theme expands beyond the uncompressed size limit")
		}
	}
	if _, err := os.Stat(filepath.Join(temporary, "dist", "index.html")); err != nil {
		return model.Theme{}, errors.New("theme must contain dist/index.html")
	}

	now := time.Now().UTC()
	theme := model.Theme{Manifest: manifest, Settings: map[string]any{}, SourceURL: sourceURL, Checksum: checksum, Installed: now, Updated: now}
	if existing, err := m.store.GetTheme(ctx, manifest.Short); err == nil {
		theme.Settings, theme.Installed = existing.Settings, existing.Installed
	}
	target := filepath.Join(m.root, manifest.Short)
	backup := target + ".backup-" + uuid.NewString()
	hadExisting := false
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return model.Theme{}, err
		}
		hadExisting = true
	}
	if err := os.Rename(temporary, target); err != nil {
		if hadExisting {
			_ = os.Rename(backup, target)
		}
		return model.Theme{}, err
	}
	cleanup = false
	if err := m.store.SaveTheme(ctx, theme); err != nil {
		_ = os.RemoveAll(target)
		if hadExisting {
			_ = os.Rename(backup, target)
		}
		return model.Theme{}, err
	}
	if hadExisting {
		_ = os.RemoveAll(backup)
	}
	return theme, nil
}

func validateArchive(reader *zip.Reader) ([]*zip.File, model.ThemeManifest, error) {
	entries := make([]*zip.File, 0, len(reader.File))
	seen := make(map[string]struct{}, len(reader.File))
	var total uint64
	var manifest model.ThemeManifest
	manifestFound := false
	for _, entry := range reader.File {
		name, err := safeArchivePath(entry.Name)
		if err != nil {
			return nil, manifest, err
		}
		if _, exists := seen[name]; exists {
			return nil, manifest, fmt.Errorf("duplicate theme path %q", name)
		}
		seen[name] = struct{}{}
		if entry.FileInfo().Mode()&os.ModeSymlink != 0 {
			return nil, manifest, fmt.Errorf("symbolic links are not allowed: %s", name)
		}
		if entry.UncompressedSize64 > maxThemeFileBytes {
			return nil, manifest, fmt.Errorf("theme file is too large: %s", name)
		}
		total += entry.UncompressedSize64
		if total > maxUncompressedBytes {
			return nil, manifest, errors.New("theme expands beyond the uncompressed size limit")
		}
		if entry.UncompressedSize64 > 1<<20 && entry.CompressedSize64 > 0 && entry.UncompressedSize64/entry.CompressedSize64 > 250 {
			return nil, manifest, fmt.Errorf("suspicious compression ratio for %s", name)
		}
		entries = append(entries, entry)
		if name == "komari-theme.json" {
			if entry.UncompressedSize64 > maxManifestBytes {
				return nil, manifest, errors.New("theme manifest is too large")
			}
			data, err := readZipFile(entry, maxManifestBytes)
			if err != nil {
				return nil, manifest, err
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				return nil, manifest, fmt.Errorf("decode komari-theme.json: %w", err)
			}
			manifestFound = true
		}
	}
	if !manifestFound {
		return nil, manifest, errors.New("theme archive must contain komari-theme.json at its root")
	}
	// Komari's official web bundle publishes the reserved short name
	// "default". Keep Hostpin's built-in theme sentinel intact while accepting
	// the upstream archive unchanged.
	if strings.EqualFold(manifest.Short, "default") {
		manifest.Short = "komari-default"
	}
	if err := validateManifest(manifest, seen); err != nil {
		return nil, manifest, err
	}
	return entries, manifest, nil
}

func validateManifest(manifest model.ThemeManifest, files map[string]struct{}) error {
	if !validLocalizedText(manifest.Name) {
		return errors.New("theme name must be a string or localized string object")
	}
	if !shortPattern.MatchString(manifest.Short) {
		return errors.New("theme short must contain only letters, digits, underscores, or hyphens")
	}
	if _, ok := files["dist/index.html"]; !ok {
		return errors.New("theme must contain dist/index.html")
	}
	if manifest.Preview != "" {
		preview, err := safeArchivePath(manifest.Preview)
		if err != nil {
			return errors.New("theme preview path is invalid")
		}
		if _, ok := files[preview]; !ok {
			return errors.New("theme preview file is missing")
		}
	}
	typeName := manifest.Configuration.Type
	if typeName == "" {
		typeName = "managed"
	}
	switch typeName {
	case "managed":
		if len(manifest.Configuration.Data) > 0 && string(manifest.Configuration.Data) != "null" {
			var items []map[string]any
			if err := json.Unmarshal(manifest.Configuration.Data, &items); err != nil {
				return errors.New("managed theme configuration data must be an array")
			}
		}
	case "raw":
		var raw string
		if json.Unmarshal(manifest.Configuration.Data, &raw) != nil || strings.TrimSpace(raw) == "" {
			return errors.New("raw theme configuration data must be non-empty HTML")
		}
	case "redirect":
		var target string
		if json.Unmarshal(manifest.Configuration.Data, &target) != nil || !validRedirect(target) {
			return errors.New("redirect theme configuration must be a safe site-relative path")
		}
	default:
		return errors.New("theme configuration type must be managed, raw, or redirect")
	}
	return nil
}

func safeArchivePath(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("unsafe theme path %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unsafe theme path %q", name)
	}
	return clean, nil
}

func validLocalizedText(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text) != ""
	}
	var localized map[string]string
	if json.Unmarshal(raw, &localized) != nil || len(localized) == 0 {
		return false
	}
	for _, value := range localized {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func validRedirect(target string) bool {
	if strings.TrimSpace(target) == "" || strings.Contains(target, "\\") || strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		return false
	}
	if parsed, err := url.Parse(target); err != nil || parsed.IsAbs() || parsed.Host != "" {
		return false
	}
	trimmed := target
	for strings.HasPrefix(trimmed, "../") {
		trimmed = strings.TrimPrefix(trimmed, "../")
	}
	for _, segment := range strings.Split(strings.SplitN(trimmed, "?", 2)[0], "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

func extractEntry(root string, entry *zip.File) (int64, error) {
	name, err := safeArchivePath(entry.Name)
	if err != nil {
		return 0, err
	}
	target := filepath.Join(root, filepath.FromSlash(name))
	if entry.FileInfo().IsDir() || strings.HasSuffix(entry.Name, "/") {
		return 0, os.MkdirAll(target, 0o750)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return 0, err
	}
	input, err := entry.Open()
	if err != nil {
		return 0, err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, maxThemeFileBytes+1))
	closeErr := output.Close()
	if copyErr != nil {
		return written, copyErr
	}
	if closeErr != nil {
		return written, closeErr
	}
	if written > maxThemeFileBytes {
		return written, fmt.Errorf("theme file is too large: %s", name)
	}
	return written, nil
}

func readZipFile(entry *zip.File, limit int64) ([]byte, error) {
	input, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer input.Close()
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("theme manifest is too large")
	}
	return data, nil
}

func (m *Manager) InstallFromURL(ctx context.Context, sourceURL, checksum string) (model.Theme, error) {
	parsed, err := url.Parse(sourceURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return model.Theme{}, errors.New("theme URL must be absolute http(s)")
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	request.Header.Set("User-Agent", "Hostpin-Theme-Installer/1")
	response, err := m.http.Do(request)
	if err != nil {
		return model.Theme{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return model.Theme{}, fmt.Errorf("theme server returned %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxArchiveBytes+1))
	if err != nil {
		return model.Theme{}, err
	}
	return m.Install(ctx, data, checksum, sourceURL)
}

func (m *Manager) Delete(ctx context.Context, short string) error {
	if !shortPattern.MatchString(short) || strings.EqualFold(short, "default") {
		return errors.New("invalid theme identifier")
	}
	if err := m.store.DeleteTheme(ctx, short); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(m.root, short))
}

func (m *Manager) SaveSettings(ctx context.Context, short string, settings map[string]any) (model.Theme, error) {
	theme, err := m.store.GetTheme(ctx, short)
	if err != nil {
		return model.Theme{}, err
	}
	if encoded, _ := json.Marshal(settings); len(encoded) > maxManifestBytes {
		return model.Theme{}, errors.New("theme settings are too large")
	}
	theme.Settings = settings
	if err := m.store.SaveTheme(ctx, theme); err != nil {
		return model.Theme{}, err
	}
	return theme, nil
}

func (m *Manager) PublicSettings(ctx context.Context, short string) map[string]any {
	if short == "" || short == "default" {
		return map[string]any{}
	}
	theme, err := m.store.GetTheme(ctx, short)
	if err != nil {
		return map[string]any{}
	}
	result := managedDefaults(theme.Manifest)
	for key, value := range theme.Settings {
		result[key] = normalizeSelectorValue(value)
	}
	return result
}

func managedDefaults(manifest model.ThemeManifest) map[string]any {
	result := map[string]any{}
	typeName := manifest.Configuration.Type
	if typeName != "" && typeName != "managed" {
		return result
	}
	var items []map[string]any
	if json.Unmarshal(manifest.Configuration.Data, &items) != nil {
		return result
	}
	for _, item := range items {
		key, _ := item["key"].(string)
		kind, _ := item["type"].(string)
		if key == "" || kind == "title" || kind == "textbox" {
			continue
		}
		value, exists := item["default"]
		if !exists {
			switch kind {
			case "number":
				value = 0
			case "switch":
				value = false
			case "select":
				options, _ := item["options"].(string)
				value = strings.TrimSpace(strings.Split(options, ",")[0])
			default:
				value = ""
			}
		}
		result[key] = normalizeSelectorValue(value)
	}
	return result
}

func normalizeSelectorValue(value any) any {
	text, ok := value.(string)
	if !ok {
		return value
	}
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "[") {
		var result []any
		if json.Unmarshal([]byte(trimmed), &result) == nil {
			return result
		}
	}
	return value
}

func (m *Manager) ServeAsset(w http.ResponseWriter, r *http.Request, short, relative string) {
	if !shortPattern.MatchString(short) {
		http.NotFound(w, r)
		return
	}
	clean, err := safeArchivePath(relative)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m.serveFile(w, r, filepath.Join(m.root, short, filepath.FromSlash(clean)), true)
}

func (m *Manager) ServeTheme(w http.ResponseWriter, r *http.Request, short string, settings model.SiteSettings) bool {
	if short == "" || short == "default" || !shortPattern.MatchString(short) {
		return false
	}
	if _, err := m.store.GetTheme(r.Context(), short); err != nil {
		return false
	}
	relative := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if relative != "" && relative != "." {
		candidate := filepath.Join(m.root, short, "dist", filepath.FromSlash(relative))
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			m.serveFile(w, r, candidate, true)
			return true
		}
	}
	indexPath := filepath.Join(m.root, short, "dist", "index.html")
	data, err := os.ReadFile(indexPath)
	if err != nil || len(data) > 5<<20 {
		return false
	}
	html := string(data)
	html = strings.Replace(html, "<title>Komari Monitor</title>", "<title>"+htmlEscape(settings.Name)+"</title>", 1)
	html = strings.Replace(html, "A simple server monitor tool.", htmlEscape(settings.Description), 1)
	if settings.CustomHead != "" {
		html = strings.Replace(html, "</head>", settings.CustomHead+"\n</head>", 1)
	}
	if settings.CustomBody != "" {
		html = strings.Replace(html, "</body>", settings.CustomBody+"\n</body>", 1)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, html)
	return true
}

func (m *Manager) FetchMarket(ctx context.Context, sourceURL string) (json.RawMessage, error) {
	parsed, err := url.Parse(sourceURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("market source must be an absolute http(s) URL")
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	request.Header.Set("User-Agent", "Hostpin-Theme-Market/1")
	response, err := m.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("market server returned %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 5<<20+1))
	if err != nil || len(data) > 5<<20 {
		return nil, errors.New("theme market response exceeds 5 MiB")
	}
	if !json.Valid(data) {
		return nil, errors.New("theme market returned invalid JSON")
	}
	return json.RawMessage(data), nil
}

func (m *Manager) serveFile(w http.ResponseWriter, r *http.Request, filename string, cache bool) {
	file, err := os.Open(filename)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if contentType := mime.TypeByExtension(filepath.Ext(filename)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if cache {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func htmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&#34;", "'", "&#39;")
	return replacer.Replace(value)
}
