package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultDatabaseIsSQLite(t *testing.T) {
	cfg := Default()
	if cfg.Database.Driver != "sqlite" {
		t.Fatalf("default database driver = %q, want sqlite", cfg.Database.Driver)
	}
	if filepath.Clean(cfg.Database.DSN) != filepath.Join(cfg.DataDir, "hostpin.db") {
		t.Fatalf("default database DSN = %q, want data directory SQLite file", cfg.Database.DSN)
	}
}

func TestValidateRejectsUnsafeNetworkConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"public URL credentials", func(cfg *Config) { cfg.PublicURL = "https://user:secret@example.com" }},
		{"public URL query", func(cfg *Config) { cfg.PublicURL = "https://example.com/?token=secret" }},
		{"public plain HTTP", func(cfg *Config) { cfg.PublicURL = "http://198.51.100.20" }},
		{"invalid trusted proxy", func(cfg *Config) { cfg.Security.TrustedProxies = []string{"127.0.0.1"} }},
		{"invalid allowed origin", func(cfg *Config) { cfg.Security.AllowedOrigins = []string{"javascript:alert(1)"} }},
		{"GeoIP template missing", func(cfg *Config) { cfg.GeoIP.Provider = "https://geo.example.com/lookup" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("unsafe configuration was accepted")
			}
		})
	}
}

func TestExplicitPublicHTTPOverride(t *testing.T) {
	cfg := Default()
	cfg.PublicURL = "http://198.51.100.20"
	cfg.Security.AllowInsecureHTTP = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit public HTTP override was rejected: %v", err)
	}
}

func TestLoadAppliesRuntimeEnvironment(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("HOSTPIN_DATA_DIR", dataDir)
	t.Setenv("HOSTPIN_PUBLIC_URL", "https://monitor.example.com")
	t.Setenv("HOSTPIN_OFFLINE_AFTER", "45s")
	t.Setenv("HOSTPIN_SHUTDOWN_TIMEOUT", "30s")
	t.Setenv("HOSTPIN_GEOIP_CACHE_TTL", "48h")
	t.Setenv("HOSTPIN_TRUSTED_PROXIES", "127.0.0.1/32, ::1/128")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime.OfflineAfter != 45*time.Second || cfg.Runtime.ShutdownTimeout != 30*time.Second || cfg.GeoIP.CacheTTL != 48*time.Hour {
		t.Fatalf("duration environment was not applied: %+v %+v", cfg.Runtime, cfg.GeoIP)
	}
	if len(cfg.Security.TrustedProxies) != 2 || !strings.HasPrefix(cfg.Database.DSN, dataDir) {
		t.Fatalf("list or data path environment was not applied: %+v", cfg)
	}
}

func TestLoadRejectsMalformedEnvironment(t *testing.T) {
	t.Setenv("HOSTPIN_DATA_DIR", t.TempDir())
	t.Setenv("HOSTPIN_OFFLINE_AFTER", "eventually")
	if _, err := Load(""); err == nil {
		t.Fatal("malformed duration environment was silently ignored")
	}
}
