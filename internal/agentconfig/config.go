package agentconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chnzzh/hostpin/internal/model"
)

type Config struct {
	Endpoint  string            `json:"endpoint"`
	NodeID    string            `json:"node_id"`
	InstallID string            `json:"install_id"`
	Token     string            `json:"token"`
	Role      model.NodeRole    `json:"role"`
	Agent     model.AgentConfig `json:"agent"`
	Metadata  LocalMetadata     `json:"metadata"`
}

type LocalMetadata struct {
	Name  string   `json:"name"`
	Group string   `json:"group,omitempty"`
	Tags  []string `json:"tags,omitempty"`
}

// PendingEnrollment keeps the generated installation identity durable while
// the first enrollment request is in flight. It deliberately never stores the
// PIN. Reusing this identity makes a retry idempotent if the server committed
// the node but the response was lost.
type PendingEnrollment struct {
	Endpoint  string         `json:"endpoint"`
	InstallID string         `json:"install_id"`
	Token     string         `json:"token"`
	Role      model.NodeRole `json:"role"`
}

func (c Config) Validate() error {
	if !strings.HasPrefix(c.Endpoint, "http://") && !strings.HasPrefix(c.Endpoint, "https://") {
		return errors.New("endpoint must be an absolute http(s) URL")
	}
	if c.NodeID == "" || c.InstallID == "" || c.Token == "" {
		return errors.New("agent identity is incomplete")
	}
	if c.Role != "" && c.Role != model.NodeRoleMonitor && c.Role != model.NodeRoleProbe {
		return errors.New("agent role must be monitor or probe")
	}
	if c.Agent.CollectIntervalSeconds <= 0 {
		return errors.New("collect interval must be positive")
	}
	return nil
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode agent config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (p PendingEnrollment) Validate() error {
	if !strings.HasPrefix(p.Endpoint, "http://") && !strings.HasPrefix(p.Endpoint, "https://") {
		return errors.New("pending enrollment endpoint must be an absolute http(s) URL")
	}
	if p.InstallID == "" || p.Token == "" {
		return errors.New("pending enrollment identity is incomplete")
	}
	if p.Role != model.NodeRoleMonitor && p.Role != model.NodeRoleProbe {
		return errors.New("pending enrollment role must be monitor or probe")
	}
	return nil
}

func PendingPath(configPath string) string { return configPath + ".enrollment-pending" }

func LoadPending(configPath string) (PendingEnrollment, error) {
	data, err := os.ReadFile(PendingPath(configPath))
	if err != nil {
		return PendingEnrollment{}, err
	}
	var pending PendingEnrollment
	if err := json.Unmarshal(data, &pending); err != nil {
		return PendingEnrollment{}, fmt.Errorf("decode pending enrollment: %w", err)
	}
	if err := pending.Validate(); err != nil {
		return PendingEnrollment{}, err
	}
	return pending, nil
}

func SavePending(configPath string, pending PendingEnrollment) error {
	if err := pending.Validate(); err != nil {
		return err
	}
	path := PendingPath(configPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func RemovePending(configPath string) error {
	err := os.Remove(PendingPath(configPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func DefaultPath() string {
	if override := strings.TrimSpace(os.Getenv("HOSTPIN_AGENT_CONFIG")); override != "" {
		return override
	}
	if runtime.GOOS == "windows" {
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "Hostpin", "agent.json")
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Hostpin", "agent.json")
	}
	if isPrivileged() {
		return "/etc/hostpin/agent.json"
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "hostpin", "agent.json")
}

func InstallBinaryPath() string {
	if override := strings.TrimSpace(os.Getenv("HOSTPIN_AGENT_BINARY")); filepath.IsAbs(override) {
		return override
	}
	if runtime.GOOS == "windows" {
		base := os.Getenv("ProgramFiles")
		if base == "" {
			base = `C:\Program Files`
		}
		return filepath.Join(base, "Hostpin", "hostpin-agent.exe")
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Hostpin", "hostpin-agent")
	}
	if isPrivileged() {
		return "/usr/local/bin/hostpin-agent"
	}
	return filepath.Join(home, ".local", "bin", "hostpin-agent")
}
