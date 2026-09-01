package theme

import (
	"archive/zip"
	"bytes"
	"os"
	"testing"
)

func themeZip(t *testing.T, files map[string]string, symlink string) *zip.Reader {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, body := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write([]byte(body))
	}
	if symlink != "" {
		header := &zip.FileHeader{Name: symlink}
		header.SetMode(0o777 | os.ModeSymlink)
		entry, _ := writer.CreateHeader(header)
		_, _ = entry.Write([]byte("target"))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func TestValidateThemeArchive(t *testing.T) {
	reader := themeZip(t, map[string]string{
		"komari-theme.json": `{"name":{"zh-CN":"测试","en":"Test"},"short":"Test_Theme","version":"1.0.0","configuration":{"type":"managed","data":[]}}`,
		"dist/index.html":   `<title>Komari Monitor</title><meta name="description" content="A simple server monitor tool." /><body></body>`,
	}, "")
	_, manifest, err := validateArchive(reader)
	if err != nil || manifest.Short != "Test_Theme" {
		t.Fatalf("valid theme rejected: %v", err)
	}
}

func TestOfficialDefaultThemeUsesNonConflictingAlias(t *testing.T) {
	reader := themeZip(t, map[string]string{
		"komari-theme.json": `{"name":"Komari","short":"default","version":"1.0.0"}`,
		"dist/index.html":   `<body>Komari</body>`,
	}, "")
	_, manifest, err := validateArchive(reader)
	if err != nil || manifest.Short != "komari-default" {
		t.Fatalf("official default theme alias failed: %#v %v", manifest, err)
	}
}

func TestRejectsThemeTraversalAndSymlink(t *testing.T) {
	base := map[string]string{
		"komari-theme.json": `{"name":"Bad","short":"BadTheme"}`,
		"dist/index.html":   `<body></body>`,
	}
	withTraversal := make(map[string]string, len(base)+1)
	for key, value := range base {
		withTraversal[key] = value
	}
	withTraversal["../escape"] = "bad"
	if _, _, err := validateArchive(themeZip(t, withTraversal, "")); err == nil {
		t.Fatal("path traversal was accepted")
	}
	if _, _, err := validateArchive(themeZip(t, base, "dist/link")); err == nil {
		t.Fatal("symbolic link was accepted")
	}
}

func TestRedirectValidation(t *testing.T) {
	for _, value := range []string{"settings/site", "../settings", "theme/settings?tab=one#top"} {
		if !validRedirect(value) {
			t.Fatalf("valid redirect %q was rejected", value)
		}
	}
	for _, value := range []string{"https://evil.example", "//evil.example", "/admin", `theme\settings`, "theme/../admin"} {
		if validRedirect(value) {
			t.Fatalf("unsafe redirect %q was accepted", value)
		}
	}
}
