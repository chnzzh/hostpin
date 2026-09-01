package model

import (
	"encoding/json"
	"time"
)

const (
	ProtocolVersion = 1
	SchemaVersion   = 1
)

type NodeRole string

const (
	NodeRoleMonitor NodeRole = "monitor"
	NodeRoleProbe   NodeRole = "probe"
)

func NormalizeNodeRole(role NodeRole) NodeRole {
	if role == NodeRoleProbe {
		return NodeRoleProbe
	}
	return NodeRoleMonitor
}

type Node struct {
	ID                           string     `json:"id"`
	Role                         NodeRole   `json:"role"`
	LatencyEnabled               bool       `json:"latency_enabled"`
	InstallID                    string     `json:"-"`
	Name                         string     `json:"name"`
	Hostname                     string     `json:"hostname,omitempty"`
	Group                        string     `json:"group,omitempty"`
	Region                       string     `json:"region,omitempty"`
	CountryCode                  string     `json:"country_code,omitempty"`
	Latitude                     *float64   `json:"latitude,omitempty"`
	Longitude                    *float64   `json:"longitude,omitempty"`
	LocationManual               bool       `json:"location_manual,omitempty"`
	Tags                         []string   `json:"tags"`
	PublicRemark                 string     `json:"public_remark,omitempty"`
	PrivateRemark                string     `json:"private_remark,omitempty"`
	Hidden                       bool       `json:"hidden"`
	Weight                       int        `json:"weight"`
	Price                        float64    `json:"price"`
	Currency                     string     `json:"currency"`
	BillingCycleDays             int        `json:"billing_cycle_days"`
	ExpiresAt                    *time.Time `json:"expires_at,omitempty"`
	AutoRenewal                  bool       `json:"auto_renewal"`
	TrafficLimit                 int64      `json:"traffic_limit"`
	TrafficLimitType             string     `json:"traffic_limit_type"`
	TrafficResetDay              int        `json:"traffic_reset_day"`
	TrafficRXCorrection          int64      `json:"traffic_rx_correction"`
	TrafficTXCorrection          int64      `json:"traffic_tx_correction"`
	TrafficCorrectionPeriodStart *time.Time `json:"traffic_correction_period_start,omitempty"`
	TrafficCorrectionUpdatedAt   *time.Time `json:"traffic_correction_updated_at,omitempty"`
	AgentVersion                 string     `json:"agent_version,omitempty"`
	OS                           string     `json:"os,omitempty"`
	Arch                         string     `json:"arch,omitempty"`
	CPUName                      string     `json:"cpu_name,omitempty"`
	CPUCores                     int        `json:"cpu_cores,omitempty"`
	Virtualization               string     `json:"virtualization,omitempty"`
	KernelVersion                string     `json:"kernel_version,omitempty"`
	IPv4                         string     `json:"ipv4,omitempty"`
	IPv6                         string     `json:"ipv6,omitempty"`
	SourceIP                     string     `json:"-"`
	CreatedAt                    time.Time  `json:"created_at"`
	UpdatedAt                    time.Time  `json:"updated_at"`
	LastSeenAt                   *time.Time `json:"last_seen_at,omitempty"`
	Online                       bool       `json:"online"`
}

type PublicNode struct {
	ID               string     `json:"id"`
	Role             NodeRole   `json:"role"`
	LatencyEnabled   bool       `json:"latency_enabled"`
	Name             string     `json:"name"`
	Group            string     `json:"group,omitempty"`
	Region           string     `json:"region,omitempty"`
	CountryCode      string     `json:"country_code,omitempty"`
	Latitude         *float64   `json:"latitude,omitempty"`
	Longitude        *float64   `json:"longitude,omitempty"`
	Tags             []string   `json:"tags"`
	PublicRemark     string     `json:"public_remark,omitempty"`
	Hidden           bool       `json:"hidden"`
	Weight           int        `json:"weight"`
	Price            float64    `json:"price"`
	Currency         string     `json:"currency"`
	BillingCycleDays int        `json:"billing_cycle_days"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	AutoRenewal      bool       `json:"auto_renewal"`
	TrafficLimit     int64      `json:"traffic_limit"`
	TrafficLimitType string     `json:"traffic_limit_type"`
	TrafficResetDay  int        `json:"traffic_reset_day"`
	OS               string     `json:"os,omitempty"`
	Arch             string     `json:"arch,omitempty"`
	CPUName          string     `json:"cpu_name,omitempty"`
	CPUCores         int        `json:"cpu_cores,omitempty"`
	Virtualization   string     `json:"virtualization,omitempty"`
	KernelVersion    string     `json:"kernel_version,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	LastSeenAt       *time.Time `json:"last_seen_at,omitempty"`
	Online           bool       `json:"online"`
}

func (n Node) Public() PublicNode {
	n.Role = NormalizeNodeRole(n.Role)
	return PublicNode{
		ID: n.ID, Role: n.Role, LatencyEnabled: n.CanMeasureLatency(), Name: n.Name, Group: n.Group, Region: n.Region,
		CountryCode: n.CountryCode, Latitude: n.Latitude, Longitude: n.Longitude,
		Tags: append([]string{}, n.Tags...), PublicRemark: n.PublicRemark,
		Hidden: n.Hidden, Weight: n.Weight, Price: n.Price, Currency: n.Currency,
		BillingCycleDays: n.BillingCycleDays, ExpiresAt: n.ExpiresAt,
		AutoRenewal: n.AutoRenewal, TrafficLimit: n.TrafficLimit,
		TrafficLimitType: n.TrafficLimitType, TrafficResetDay: n.TrafficResetDay, OS: n.OS, Arch: n.Arch,
		CPUName: n.CPUName, CPUCores: n.CPUCores, Virtualization: n.Virtualization,
		KernelVersion: n.KernelVersion, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
		LastSeenAt: n.LastSeenAt, Online: n.Online,
	}
}

// CanMeasureLatency reports whether the node may receive latency-matrix tasks.
// Probe-only nodes always have this capability; monitor nodes opt in explicitly.
func (n Node) CanMeasureLatency() bool {
	return NormalizeNodeRole(n.Role) == NodeRoleProbe || n.LatencyEnabled
}

type DiskMetric struct {
	Mountpoint  string  `json:"mountpoint"`
	Filesystem  string  `json:"filesystem,omitempty"`
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	ReadBPS     float64 `json:"read_bps,omitempty"`
	WriteBPS    float64 `json:"write_bps,omitempty"`
	ReadIOPS    float64 `json:"read_iops,omitempty"`
	WriteIOPS   float64 `json:"write_iops,omitempty"`
	AwaitMS     float64 `json:"await_ms,omitempty"`
	Utilization float64 `json:"utilization,omitempty"`
}

type NetworkMetric struct {
	Interface string  `json:"interface"`
	RxBPS     float64 `json:"rx_bps"`
	TxBPS     float64 `json:"tx_bps"`
	RxBytes   uint64  `json:"rx_bytes"`
	TxBytes   uint64  `json:"tx_bytes"`
}

type GPUMetric struct {
	Index       int     `json:"index"`
	Name        string  `json:"name"`
	Utilization float64 `json:"utilization"`
	MemoryTotal uint64  `json:"memory_total,omitempty"`
	MemoryUsed  uint64  `json:"memory_used,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
}

type MetricSample struct {
	NodeID         string          `json:"node_id,omitempty"`
	Sequence       uint64          `json:"sequence"`
	CollectedAt    time.Time       `json:"collected_at"`
	ReceivedAt     time.Time       `json:"received_at,omitempty"`
	BootID         string          `json:"boot_id,omitempty"`
	ClockOffsetMS  int64           `json:"clock_offset_ms,omitempty"`
	CPU            float64         `json:"cpu"`
	Load1          float64         `json:"load1"`
	Load5          float64         `json:"load5"`
	Load15         float64         `json:"load15"`
	MemoryTotal    uint64          `json:"memory_total"`
	MemoryUsed     uint64          `json:"memory_used"`
	SwapTotal      uint64          `json:"swap_total"`
	SwapUsed       uint64          `json:"swap_used"`
	DiskTotal      uint64          `json:"disk_total"`
	DiskUsed       uint64          `json:"disk_used"`
	NetRxBPS       float64         `json:"net_rx_bps"`
	NetTxBPS       float64         `json:"net_tx_bps"`
	NetRxBytes     uint64          `json:"net_rx_bytes"`
	NetTxBytes     uint64          `json:"net_tx_bytes"`
	MonthlyRxBytes uint64          `json:"monthly_rx_bytes,omitempty"`
	MonthlyTxBytes uint64          `json:"monthly_tx_bytes,omitempty"`
	TCPConnections int             `json:"tcp_connections"`
	UDPConnections int             `json:"udp_connections"`
	Processes      int             `json:"processes"`
	Temperature    float64         `json:"temperature,omitempty"`
	UptimeSeconds  uint64          `json:"uptime_seconds"`
	Disks          []DiskMetric    `json:"disks,omitempty"`
	Networks       []NetworkMetric `json:"networks,omitempty"`
	GPUs           []GPUMetric     `json:"gpus,omitempty"`
	Message        string          `json:"message,omitempty"`
}

type AgentIdentity struct {
	Version        string `json:"version"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	Hostname       string `json:"hostname"`
	CPUName        string `json:"cpu_name,omitempty"`
	CPUCores       int    `json:"cpu_cores,omitempty"`
	Virtualization string `json:"virtualization,omitempty"`
	KernelVersion  string `json:"kernel_version,omitempty"`
	IPv4           string `json:"ipv4,omitempty"`
	IPv6           string `json:"ipv6,omitempty"`
}

type AgentConfig struct {
	CollectIntervalSeconds int      `json:"collect_interval_seconds"`
	PersistIntervalSeconds int      `json:"persist_interval_seconds"`
	IncludeNICs            []string `json:"include_nics,omitempty"`
	ExcludeNICs            []string `json:"exclude_nics,omitempty"`
	IncludeMountpoints     []string `json:"include_mountpoints,omitempty"`
	EnableGPU              bool     `json:"enable_gpu"`
	AutoUpdate             bool     `json:"auto_update"`
	ProbeConcurrency       int      `json:"probe_concurrency"`
	ConfigVersion          int64    `json:"config_version"`
}

func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		CollectIntervalSeconds: 3,
		PersistIntervalSeconds: 60,
		ProbeConcurrency:       4,
		ConfigVersion:          1,
	}
}

type EnrollmentMetadata struct {
	Name             string     `json:"name"`
	Group            string     `json:"group,omitempty"`
	Region           string     `json:"region,omitempty"`
	CountryCode      string     `json:"country_code,omitempty"`
	Latitude         *float64   `json:"latitude,omitempty"`
	Longitude        *float64   `json:"longitude,omitempty"`
	Tags             []string   `json:"tags,omitempty"`
	PublicRemark     string     `json:"public_remark,omitempty"`
	PrivateRemark    string     `json:"private_remark,omitempty"`
	Hidden           bool       `json:"hidden"`
	Price            float64    `json:"price,omitempty"`
	Currency         string     `json:"currency,omitempty"`
	BillingCycleDays int        `json:"billing_cycle_days,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	AutoRenewal      bool       `json:"auto_renewal"`
	TrafficLimit     int64      `json:"traffic_limit,omitempty"`
	TrafficLimitType string     `json:"traffic_limit_type,omitempty"`
	TrafficResetDay  int        `json:"traffic_reset_day,omitempty"`
}

type EnrollmentRequest struct {
	PIN       string             `json:"pin"`
	InstallID string             `json:"install_id"`
	Token     string             `json:"token"`
	Role      NodeRole           `json:"role,omitempty"`
	Identity  AgentIdentity      `json:"identity"`
	Metadata  EnrollmentMetadata `json:"metadata"`
	Config    AgentConfig        `json:"config"`
}

type EnrollmentResponse struct {
	NodeID          string      `json:"node_id"`
	Role            NodeRole    `json:"role"`
	ProtocolVersion int         `json:"protocol_version"`
	StreamURL       string      `json:"stream_url"`
	ReportURL       string      `json:"report_url"`
	Config          AgentConfig `json:"config"`
	Created         bool        `json:"created"`
}

type AgentHello struct {
	Type            string        `json:"type"`
	ProtocolVersion int           `json:"protocol_version"`
	InstallID       string        `json:"install_id"`
	Version         string        `json:"version"`
	ConfigVersion   int64         `json:"config_version"`
	Identity        AgentIdentity `json:"identity"`
}

type AgentFrame struct {
	Type        string          `json:"type"`
	Sample      *MetricSample   `json:"sample,omitempty"`
	ProbeResult *ProbeResult    `json:"probe_result,omitempty"`
	Hello       *AgentHello     `json:"hello,omitempty"`
	Raw         json.RawMessage `json:"-"`
}

type AgentAck struct {
	Type         string      `json:"type"`
	Accepted     bool        `json:"accepted"`
	Persisted    bool        `json:"persisted"`
	ServerTime   time.Time   `json:"server_time"`
	NextReportMS int         `json:"next_report_ms,omitempty"`
	Config       AgentConfig `json:"config"`
	Tasks        []ProbeTask `json:"tasks,omitempty"`
	Error        string      `json:"error,omitempty"`
}

type AgentReport struct {
	Identity     AgentIdentity `json:"identity"`
	Sample       *MetricSample `json:"sample,omitempty"`
	ProbeResults []ProbeResult `json:"probe_results,omitempty"`
}

type NodeSnapshot struct {
	Node   PublicNode    `json:"node"`
	Metric *MetricSample `json:"metric,omitempty"`
}

type LiveMessage struct {
	Type    string          `json:"type"`
	At      time.Time       `json:"at"`
	NodeID  string          `json:"node_id,omitempty"`
	Sample  *MetricSample   `json:"sample,omitempty"`
	Online  *bool           `json:"online,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type ProbeType string

const (
	ProbeICMP ProbeType = "icmp"
	ProbeTCP  ProbeType = "tcp"
	ProbeHTTP ProbeType = "http"
	ProbeDNS  ProbeType = "dns"

	ProbePurposeCustom         = "custom"
	ProbePurposeLatency        = "latency"
	ProbePurposeCarrierTelecom = "carrier.telecom"
	ProbePurposeCarrierUnicom  = "carrier.unicom"
	ProbePurposeCarrierMobile  = "carrier.mobile"
)

func IsCarrierProbePurpose(purpose string) bool {
	switch purpose {
	case ProbePurposeCarrierTelecom, ProbePurposeCarrierUnicom, ProbePurposeCarrierMobile:
		return true
	default:
		return false
	}
}

type ProbeTask struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Type            ProbeType `json:"type"`
	Target          string    `json:"target"`
	IntervalSeconds int       `json:"interval_seconds"`
	TimeoutSeconds  int       `json:"timeout_seconds"`
	ExpectedStatus  int       `json:"expected_status,omitempty"`
	ExpectedValue   string    `json:"expected_value,omitempty"`
	NodeIDs         []string  `json:"node_ids,omitempty"`
	Purpose         string    `json:"purpose,omitempty"`
	RunOn           NodeRole  `json:"run_on,omitempty"`
	TargetNodeID    string    `json:"target_node_id,omitempty"`
	Public          bool      `json:"public"`
	Samples         int       `json:"samples"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type ProbeResult struct {
	NodeID      string    `json:"node_id,omitempty"`
	TaskID      int64     `json:"task_id"`
	CollectedAt time.Time `json:"collected_at"`
	ReceivedAt  time.Time `json:"received_at,omitempty"`
	Success     bool      `json:"success"`
	LatencyMS   float64   `json:"latency_ms"`
	LossPercent float64   `json:"loss_percent"`
	StatusCode  int       `json:"status_code,omitempty"`
	Value       string    `json:"value,omitempty"`
	Error       string    `json:"error,omitempty"`
}

type PublicProbeSnapshot struct {
	Task    ProbeTask     `json:"task"`
	Results []ProbeResult `json:"results"`
}

type LatencyProbeNode struct {
	ID          string     `json:"id"`
	Role        NodeRole   `json:"role"`
	Name        string     `json:"name"`
	Region      string     `json:"region,omitempty"`
	CountryCode string     `json:"country_code,omitempty"`
	Tags        []string   `json:"tags"`
	OS          string     `json:"os,omitempty"`
	Arch        string     `json:"arch,omitempty"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	Online      bool       `json:"online"`
}

type LatencyTarget struct {
	TaskID          int64      `json:"task_id"`
	Node            PublicNode `json:"node"`
	Type            ProbeType  `json:"type"`
	IntervalSeconds int        `json:"interval_seconds"`
	Samples         int        `json:"samples"`
}

type LatencyResult struct {
	ProbeNodeID  string    `json:"probe_node_id"`
	TargetNodeID string    `json:"target_node_id"`
	TaskID       int64     `json:"task_id"`
	CollectedAt  time.Time `json:"collected_at"`
	ReceivedAt   time.Time `json:"received_at"`
	Success      bool      `json:"success"`
	LatencyMS    float64   `json:"latency_ms"`
	LossPercent  float64   `json:"loss_percent"`
	Error        string    `json:"error,omitempty"`
}

type LatencyWindowSummary struct {
	AverageLatencyMS   float64 `json:"average_latency_ms"`
	AverageLossPercent float64 `json:"average_loss_percent"`
	SampleCount        int64   `json:"sample_count"`
	SuccessCount       int64   `json:"success_count"`
}

type LatencyOverview struct {
	ProbeNodes     []LatencyProbeNode `json:"probe_nodes"`
	Targets        []LatencyTarget    `json:"targets"`
	Latest         []LatencyResult    `json:"latest"`
	OfflineAfterMS int64              `json:"offline_after_ms"`
}

type SiteSettings struct {
	Name                     string   `json:"name"`
	Description              string   `json:"description"`
	Private                  bool     `json:"private"`
	EnrollmentEnabled        bool     `json:"enrollment_enabled"`
	EnrollmentPINWeak        bool     `json:"enrollment_pin_weak"`
	Theme                    string   `json:"theme"`
	RecordEnabled            bool     `json:"record_enabled"`
	RawRetentionHours        int      `json:"raw_retention_hours"`
	FiveMinuteRetentionHours int      `json:"five_minute_retention_hours"`
	HourlyRetentionHours     int      `json:"hourly_retention_hours"`
	GeoIPEnabled             bool     `json:"geoip_enabled"`
	GeoIPProvider            string   `json:"geoip_provider"`
	CustomHead               string   `json:"custom_head,omitempty"`
	CustomBody               string   `json:"custom_body,omitempty"`
	ThemeMarketSources       []string `json:"theme_market_sources,omitempty"`
}

func DefaultSiteSettings() SiteSettings {
	return SiteSettings{
		Name: "Hostpin", Description: "",
		EnrollmentEnabled: true, Theme: "default", RecordEnabled: true,
		RawRetentionHours: 7 * 24, FiveMinuteRetentionHours: 90 * 24,
		HourlyRetentionHours: 365 * 24, GeoIPEnabled: true,
		GeoIPProvider:      "https://ipwho.is/{ip}",
		ThemeMarketSources: []string{"https://raw.githubusercontent.com/komari-monitor/theme-market/main/v1.json"},
	}
}

type AlertStatus string

const (
	AlertFiring   AlertStatus = "firing"
	AlertResolved AlertStatus = "resolved"
)

type AlertEvent struct {
	ID         string      `json:"event_id"`
	Type       string      `json:"type"`
	Status     AlertStatus `json:"status"`
	Severity   string      `json:"severity"`
	OccurredAt time.Time   `json:"occurred_at"`
	Node       PublicNode  `json:"node"`
	Metric     string      `json:"metric"`
	Value      float64     `json:"value"`
	Threshold  float64     `json:"threshold"`
	Link       string      `json:"link"`
	Message    string      `json:"message"`
}

type AlertScope struct {
	Groups   []string `json:"groups,omitempty"`
	NodeIDs  []string `json:"node_ids,omitempty"`
	Excluded []string `json:"excluded_node_ids,omitempty"`
}

type AlertRule struct {
	ID                int64      `json:"id"`
	Name              string     `json:"name"`
	Metric            string     `json:"metric"`
	Operator          string     `json:"operator"`
	Threshold         float64    `json:"threshold"`
	RecoveryThreshold float64    `json:"recovery_threshold"`
	DurationSeconds   int        `json:"duration_seconds"`
	CooldownSeconds   int        `json:"cooldown_seconds"`
	Severity          string     `json:"severity"`
	Scope             AlertScope `json:"scope"`
	Enabled           bool       `json:"enabled"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type NotificationChannel struct {
	ID              int64          `json:"id"`
	Name            string         `json:"name"`
	Type            string         `json:"type"`
	Config          map[string]any `json:"config,omitempty"`
	EncryptedConfig string         `json:"-"`
	Enabled         bool           `json:"enabled"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type NotificationDelivery struct {
	ID            string              `json:"id"`
	Event         AlertEvent          `json:"event"`
	Channel       NotificationChannel `json:"channel"`
	Status        string              `json:"status"`
	Attempts      int                 `json:"attempts"`
	NextAttemptAt time.Time           `json:"next_attempt_at"`
	LastError     string              `json:"last_error,omitempty"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

type AuditEntry struct {
	ID         int64     `json:"id"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	Target     string    `json:"target"`
	Detail     string    `json:"detail"`
	OccurredAt time.Time `json:"occurred_at"`
}

type ThemeConfiguration struct {
	Type string          `json:"type,omitempty"`
	Icon string          `json:"icon,omitempty"`
	Name json.RawMessage `json:"name,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

type ThemeManifest struct {
	Name          json.RawMessage    `json:"name"`
	Short         string             `json:"short"`
	Description   json.RawMessage    `json:"description,omitempty"`
	Version       string             `json:"version,omitempty"`
	Author        json.RawMessage    `json:"author,omitempty"`
	URL           string             `json:"url,omitempty"`
	Preview       string             `json:"preview,omitempty"`
	Configuration ThemeConfiguration `json:"configuration,omitempty"`
}

type Theme struct {
	Manifest  ThemeManifest  `json:"manifest"`
	Settings  map[string]any `json:"settings"`
	SourceURL string         `json:"source_url,omitempty"`
	Checksum  string         `json:"checksum"`
	Installed time.Time      `json:"installed_at"`
	Updated   time.Time      `json:"updated_at"`
}

type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type ShareLink struct {
	ID        string     `json:"id"`
	NodeIDs   []string   `json:"node_ids"`
	ExpiresAt time.Time  `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Envelope[T any] struct {
	Data  T         `json:"data"`
	Error *APIError `json:"error,omitempty"`
}
