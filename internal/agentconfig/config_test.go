package agentconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chnzzh/hostpin/internal/model"
)

func TestSaveUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "agent.json")
	cfg := Config{Endpoint: "https://monitor.example", NodeID: "node", InstallID: "install", Token: "secret", Agent: model.DefaultAgentConfig()}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("configuration mode=%v, want 0600", info.Mode().Perm())
	}
	loaded, err := Load(path)
	if err != nil || loaded.Token != cfg.Token {
		t.Fatalf("configuration did not round-trip: %v", err)
	}
}

func TestInstallBinaryOverrideRequiresAbsolutePath(t *testing.T) {
	absolute := filepath.Join(t.TempDir(), "hostpin-agent")
	t.Setenv("HOSTPIN_AGENT_BINARY", absolute)
	if got := InstallBinaryPath(); got != absolute {
		t.Fatalf("absolute binary override=%q, want %q", got, absolute)
	}
	t.Setenv("HOSTPIN_AGENT_BINARY", "relative/hostpin-agent")
	if got := InstallBinaryPath(); got == "relative/hostpin-agent" {
		t.Fatal("relative binary override was accepted")
	}
}

func TestPendingEnrollmentUsesPrivatePermissionsAndRoundTrips(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "nested", "agent.json")
	pending := PendingEnrollment{
		Endpoint: "https://monitor.example", InstallID: "install-id", Token: "agent-token", Role: model.NodeRoleProbe,
	}
	if err := SavePending(configPath, pending); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(PendingPath(configPath))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("pending enrollment mode=%v, want 0600", info.Mode().Perm())
	}
	loaded, err := LoadPending(configPath)
	if err != nil || loaded != pending {
		t.Fatalf("pending enrollment did not round-trip: %#v %v", loaded, err)
	}
	if err := RemovePending(configPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(PendingPath(configPath)); !os.IsNotExist(err) {
		t.Fatalf("pending enrollment still exists: %v", err)
	}
}
