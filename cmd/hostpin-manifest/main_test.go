package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chnzzh/hostpin/internal/updater"
)

func TestGenerateSigningKeysAndValidatePair(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "update-keys")
	if err := generateSigningKeys(directory); err != nil {
		t.Fatal(err)
	}
	publicRaw, err := os.ReadFile(filepath.Join(directory, "public.key"))
	if err != nil {
		t.Fatal(err)
	}
	privateRaw, err := os.ReadFile(filepath.Join(directory, "private.key"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"public.key", "private.key"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s permissions = %v, want 0600", name, info.Mode().Perm())
		}
	}
	t.Setenv("HOSTPIN_UPDATE_PUBLIC_KEY", string(publicRaw))
	t.Setenv("HOSTPIN_UPDATE_PRIVATE_KEY", string(privateRaw))
	if _, _, err := signingKeysFromEnvironment(); err != nil {
		t.Fatalf("generated signing pair was rejected: %v", err)
	}
	if err := generateSigningKeys(directory); err == nil {
		t.Fatal("existing signing-key directory was overwritten")
	}

	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOSTPIN_UPDATE_PUBLIC_KEY", base64.StdEncoding.EncodeToString(otherPublic))
	if _, _, err := signingKeysFromEnvironment(); err == nil {
		t.Fatal("mismatched signing keys were accepted")
	}
}

func TestTargetFromName(t *testing.T) {
	tests := map[string][2]string{
		"hostpin-agent-linux-amd64":       {"linux", "amd64"},
		"hostpin-agent-linux-armv7":       {"linux", "arm"},
		"hostpin-agent-windows-arm64.exe": {"windows", "arm64"},
	}
	for name, expected := range tests {
		osName, arch, ok := targetFromName(name)
		if !ok || osName != expected[0] || arch != expected[1] {
			t.Errorf("targetFromName(%q) = %q/%q/%v, want %q/%q/true", name, osName, arch, ok, expected[0], expected[1])
		}
	}
	if _, _, ok := targetFromName("hostpin-server-linux-amd64"); ok {
		t.Fatal("server artifact was accepted as an Agent target")
	}
}

func TestWriteManifestProducesVerifiableArtifacts(t *testing.T) {
	directory := t.TempDir()
	artifactName := "hostpin-agent-linux-amd64"
	artifactData := []byte("hostpin-agent-test-binary")
	if err := os.WriteFile(filepath.Join(directory, artifactName), artifactData, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, artifactName+".sha256"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "hostpin-server-linux-amd64"), []byte("ignored"), 0o755); err != nil {
		t.Fatal(err)
	}

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "update-manifest.json")
	baseURL := "https://github.com/example/hostpin/releases/download/v0.1.0"
	if err := writeManifest("v0.1.0", directory, baseURL, output, private); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var manifest updater.Manifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "v0.1.0" || len(manifest.Artifacts) != 1 {
		t.Fatalf("unexpected manifest payload: %#v", manifest.Payload)
	}
	artifact := manifest.Artifacts[0]
	digest := sha256.Sum256(artifactData)
	if artifact.OS != "linux" || artifact.Arch != "amd64" || artifact.URL != baseURL+"/"+artifactName || artifact.Size != int64(len(artifactData)) || artifact.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("unexpected manifest artifact: %#v", artifact)
	}
	signature, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(manifest.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(public, canonical, signature) {
		t.Fatal("generated manifest signature did not verify")
	}
	if err := writeManifest("v0.1.0", directory, "http://example.com/downloads", output, private); err == nil {
		t.Fatal("non-HTTPS release URL was accepted")
	}
}
