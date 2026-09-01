package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen           string         `yaml:"listen"`
	PublicURL        string         `yaml:"public_url"`
	AgentReleaseBase string         `yaml:"agent_release_base"`
	DataDir          string         `yaml:"data_dir"`
	LogLevel         string         `yaml:"log_level"`
	Database         DatabaseConfig `yaml:"database"`
	Security         SecurityConfig `yaml:"security"`
	GeoIP            GeoIPConfig    `yaml:"geoip"`
	Runtime          RuntimeConfig  `yaml:"runtime"`
}

type DatabaseConfig struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

type SecurityConfig struct {
	MasterKey         string   `yaml:"master_key"`
	AllowInsecureHTTP bool     `yaml:"allow_insecure_http"`
	TrustedProxies    []string `yaml:"trusted_proxies"`
	AllowedOrigins    []string `yaml:"allowed_origins"`
	EnrollmentCIDRs   []string `yaml:"enrollment_cidrs"`
}

type GeoIPConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Provider string        `yaml:"provider"`
	Timeout  time.Duration `yaml:"timeout"`
	CacheTTL time.Duration `yaml:"cache_ttl"`
}

type RuntimeConfig struct {
	PersistQueueSize int           `yaml:"persist_queue_size"`
	OfflineAfter     time.Duration `yaml:"offline_after"`
	ShutdownTimeout  time.Duration `yaml:"shutdown_timeout"`
}

func Default() Config {
	return Config{
		Listen: ":8080", PublicURL: "http://localhost:8080", DataDir: "./data",
		LogLevel: "info",
		Database: DatabaseConfig{Driver: "sqlite", DSN: "./data/hostpin.db"},
		GeoIP: GeoIPConfig{
			Enabled: true, Provider: "https://ipwho.is/{ip}",
			Timeout: 4 * time.Second, CacheTTL: 30 * 24 * time.Hour,
		},
		Runtime: RuntimeConfig{
			PersistQueueSize: 10000, OfflineAfter: 90 * time.Second,
			ShutdownTimeout: 15 * time.Second,
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		if err == nil {
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return Config{}, fmt.Errorf("decode config: %w", err)
			}
		}
	}

	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return Config{}, fmt.Errorf("create data directory: %w", err)
	}
	return cfg, nil
}

func applyEnv(cfg *Config) error {
	setString("HOSTPIN_LISTEN", &cfg.Listen)
	setString("HOSTPIN_PUBLIC_URL", &cfg.PublicURL)
	setString("HOSTPIN_AGENT_RELEASE_BASE", &cfg.AgentReleaseBase)
	setString("HOSTPIN_DATA_DIR", &cfg.DataDir)
	setString("HOSTPIN_LOG_LEVEL", &cfg.LogLevel)
	setString("HOSTPIN_DB_DRIVER", &cfg.Database.Driver)
	setString("HOSTPIN_DB_DSN", &cfg.Database.DSN)
	setString("HOSTPIN_MASTER_KEY", &cfg.Security.MasterKey)
	setString("HOSTPIN_GEOIP_PROVIDER", &cfg.GeoIP.Provider)
	for _, item := range []struct {
		name   string
		target *time.Duration
	}{
		{"HOSTPIN_GEOIP_TIMEOUT", &cfg.GeoIP.Timeout},
		{"HOSTPIN_GEOIP_CACHE_TTL", &cfg.GeoIP.CacheTTL},
		{"HOSTPIN_OFFLINE_AFTER", &cfg.Runtime.OfflineAfter},
		{"HOSTPIN_SHUTDOWN_TIMEOUT", &cfg.Runtime.ShutdownTimeout},
	} {
		if err := setDuration(item.name, item.target); err != nil {
			return err
		}
	}

	if raw, ok := os.LookupEnv("HOSTPIN_TRUSTED_PROXIES"); ok {
		cfg.Security.TrustedProxies = splitCSV(raw)
	}
	if raw, ok := os.LookupEnv("HOSTPIN_ALLOWED_ORIGINS"); ok {
		cfg.Security.AllowedOrigins = splitCSV(raw)
	}
	if raw, ok := os.LookupEnv("HOSTPIN_ENROLLMENT_CIDRS"); ok {
		cfg.Security.EnrollmentCIDRs = splitCSV(raw)
	}
	if raw, ok := os.LookupEnv("HOSTPIN_GEOIP_ENABLED"); ok {
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("HOSTPIN_GEOIP_ENABLED must be a boolean: %w", err)
		}
		cfg.GeoIP.Enabled = value
	}
	if raw, ok := os.LookupEnv("HOSTPIN_ALLOW_INSECURE_HTTP"); ok {
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("HOSTPIN_ALLOW_INSECURE_HTTP must be a boolean: %w", err)
		}
		cfg.Security.AllowInsecureHTTP = value
	}
	if raw, ok := os.LookupEnv("HOSTPIN_PERSIST_QUEUE_SIZE"); ok {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("HOSTPIN_PERSIST_QUEUE_SIZE must be an integer: %w", err)
		}
		cfg.Runtime.PersistQueueSize = value
	}

	if cfg.Database.Driver == "sqlite" && cfg.Database.DSN == "./data/hostpin.db" {
		cfg.Database.DSN = filepath.Join(cfg.DataDir, "hostpin.db")
	}
	return nil
}

func setString(key string, target *string) {
	if value, ok := os.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
		*target = strings.TrimSpace(value)
	}
}

func setDuration(key string, target *time.Duration) error {
	if raw, ok := os.LookupEnv(key); ok {
		value, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("%s must be a duration: %w", key, err)
		}
		*target = value
	}
	return nil
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (cfg Config) Validate() error {
	if strings.TrimSpace(cfg.Listen) == "" {
		return errors.New("listen address is required")
	}
	parsed, err := url.Parse(cfg.PublicURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("public_url must be an absolute http(s) URL")
	}
	if parsed.Scheme == "http" && !cfg.Security.AllowInsecureHTTP && !safeHTTPHost(parsed.Hostname()) {
		return errors.New("public_url may use HTTP only for loopback/private addresses unless allow_insecure_http is explicitly enabled")
	}
	if strings.TrimSpace(cfg.AgentReleaseBase) != "" {
		releaseBase, err := url.Parse(cfg.AgentReleaseBase)
		if err != nil || releaseBase.Host == "" || releaseBase.User != nil || releaseBase.RawQuery != "" || releaseBase.Fragment != "" ||
			(releaseBase.Scheme != "http" && releaseBase.Scheme != "https") {
			return errors.New("agent_release_base must be an absolute HTTP(S) URL without credentials, query, or fragment")
		}
		if releaseBase.Scheme == "http" && !cfg.Security.AllowInsecureHTTP && !safeHTTPHost(releaseBase.Hostname()) {
			return errors.New("agent_release_base may use HTTP only for loopback/private addresses unless allow_insecure_http is explicitly enabled")
		}
	}
	switch cfg.Database.Driver {
	case "sqlite", "postgres", "postgresql":
	default:
		return fmt.Errorf("unsupported database driver %q", cfg.Database.Driver)
	}
	if strings.TrimSpace(cfg.Database.DSN) == "" {
		return errors.New("database DSN is required")
	}
	if cfg.Runtime.PersistQueueSize < 100 || cfg.Runtime.PersistQueueSize > 10_000_000 {
		return errors.New("persist_queue_size must be between 100 and 10000000")
	}
	if cfg.Runtime.OfflineAfter < 10*time.Second || cfg.Runtime.OfflineAfter > time.Hour {
		return errors.New("offline_after must be between 10 seconds and 1 hour")
	}
	if cfg.Runtime.ShutdownTimeout < time.Second || cfg.Runtime.ShutdownTimeout > 5*time.Minute {
		return errors.New("shutdown_timeout must be between 1 second and 5 minutes")
	}
	if cfg.GeoIP.Timeout < time.Second || cfg.GeoIP.Timeout > time.Minute || cfg.GeoIP.CacheTTL < time.Hour {
		return errors.New("GeoIP timeout must be 1 to 60 seconds and cache_ttl at least 1 hour")
	}
	if cfg.GeoIP.Enabled {
		provider, err := url.Parse(cfg.GeoIP.Provider)
		if err != nil || provider.Host == "" || provider.User != nil || (provider.Scheme != "http" && provider.Scheme != "https") || !strings.Contains(cfg.GeoIP.Provider, "{ip}") {
			return errors.New("GeoIP provider must be an absolute HTTP(S) URL containing {ip}")
		}
	}
	if !map[string]bool{"debug": true, "info": true, "warn": true, "warning": true, "error": true}[strings.ToLower(cfg.LogLevel)] {
		return errors.New("log_level must be debug, info, warn, or error")
	}
	for _, cidr := range append(append([]string{}, cfg.Security.TrustedProxies...), cfg.Security.EnrollmentCIDRs...) {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
	}
	for _, raw := range cfg.Security.AllowedOrigins {
		origin, err := url.Parse(raw)
		if err != nil || origin.Host == "" || origin.User != nil || (origin.Scheme != "http" && origin.Scheme != "https") || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" {
			return fmt.Errorf("invalid allowed origin %q", raw)
		}
	}
	if cfg.Security.MasterKey != "" {
		decoded, err := base64.StdEncoding.DecodeString(cfg.Security.MasterKey)
		if err != nil || len(decoded) != 32 {
			return errors.New("master_key must be base64 encoding of exactly 32 bytes")
		}
	}
	return nil
}

func safeHTTPHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	address, err := netip.ParseAddr(strings.TrimSpace(host))
	return err == nil && (address.IsLoopback() || address.IsPrivate())
}
