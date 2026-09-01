package updater

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/buildinfo"
)

func TestManifestSignature(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Payload: Payload{Version: "1.2.3", PublishedAt: time.Now().UTC(), Artifacts: []Artifact{{OS: "linux", Arch: "amd64", URL: "https://example.com/agent", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 12}}}}
	payload, _ := json.Marshal(manifest.Payload)
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, payload))
	if err := verifyManifest(manifest, public); err != nil {
		t.Fatal(err)
	}
	manifest.Version = "9.9.9"
	if err := verifyManifest(manifest, public); err == nil {
		t.Fatal("tampered manifest passed signature verification")
	}
}

func TestManifestURLUsesBuildMetadata(t *testing.T) {
	previous := buildinfo.ReleaseBase
	buildinfo.ReleaseBase = "https://github.example.test/owner/repository/releases/latest/download/"
	t.Cleanup(func() { buildinfo.ReleaseBase = previous })
	if result := updateManifestURL(); result != "https://github.example.test/owner/repository/releases/latest/download/update-manifest.json" {
		t.Fatalf("update manifest URL = %q", result)
	}
}

func TestVersionComparison(t *testing.T) {
	tests := []struct {
		candidate string
		current   string
		newer     bool
	}{
		{"v1.2.0", "1.1.9", true},
		{"1.2.0", "1.2.0", false},
		{"2.0.0", "dev", false},
		{"v0.1.0", "0.1.0-preview.28", true},
		{"0.1.0-preview.29", "0.1.0-preview.28", true},
		{"0.1.0-preview.2", "0.1.0-preview.10", false},
		{"0.1.0-preview.1", "0.1.0", false},
		{"1.0.0-beta.11", "1.0.0-beta.2", true},
		{"1.0.0+build.2", "1.0.0+build.1", false},
		{"not-a-version", "1.0.0", false},
	}
	for _, test := range tests {
		if result := newerVersion(test.candidate, test.current); result != test.newer {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", test.candidate, test.current, result, test.newer)
		}
	}
}
