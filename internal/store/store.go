package store

import (
	"context"
	"errors"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

var (
	ErrNotFound                = errors.New("not found")
	ErrAlreadySetup            = errors.New("hostpin is already configured")
	ErrInstallConflict         = errors.New("install identity already belongs to another credential")
	ErrTemporaryPINUnavailable = errors.New("temporary enrollment PIN is no longer available")
	ErrUnauthorized            = errors.New("unauthorized")
)

type Admin struct {
	ID             string
	Username       string
	PasswordHash   string
	TOTPSecretEnc  string
	RecoveryHashes []string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Session struct {
	TokenHash string
	AdminID   string
	CSRFHash  string
	IPAddress string
	UserAgent string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type APIKeyRecord struct {
	Key       model.APIKey
	AdminID   string
	TokenID   string
	TokenHash string
}

type ShareLinkRecord struct {
	Link      model.ShareLink
	TokenHash string
}

type EnrollmentRecord struct {
	Node    model.Node
	Config  model.AgentConfig
	Created bool
}

type TemporaryEnrollmentPIN struct {
	ID               string
	PINHash          string
	ClaimedInstallID string
	ClaimedTokenID   string
	CreatedAt        time.Time
	ExpiresAt        time.Time
	UsedAt           *time.Time
	RevokedAt        *time.Time
}

type EnrollParams struct {
	Request        model.EnrollmentRequest
	NodeID         string
	TokenID        string
	TokenHash      string
	SourceIP       string
	LocationManual bool
	TemporaryPINID string
	Now            time.Time
}

type HistoryQuery struct {
	NodeID    string
	Start     time.Time
	End       time.Time
	MaxPoints int
}

// LifecycleRepository owns the database connection and health boundary.
type LifecycleRepository interface {
	Close() error
	Ping(context.Context) error
	Driver() string
}

// IdentityRepository contains administrator, session, API key, and share-link state.
type IdentityRepository interface {
	SetupComplete(context.Context) (bool, error)
	Initialize(context.Context, Admin, string, model.SiteSettings) error
	GetAdminByUsername(context.Context, string) (Admin, error)
	GetAdminByID(context.Context, string) (Admin, error)
	UpdateAdmin(context.Context, Admin) error
	CreateSession(context.Context, Session) error
	GetSession(context.Context, string) (Session, error)
	DeleteSession(context.Context, string) error
	DeleteExpiredSessions(context.Context, time.Time) error
	ListSessions(context.Context, string) ([]Session, error)
	DeleteAdminSessions(context.Context, string, string) error
	CreateAPIKey(context.Context, APIKeyRecord) error
	ListAPIKeys(context.Context, string) ([]model.APIKey, error)
	AuthenticateAPIKey(context.Context, string, string, time.Time) (Admin, model.APIKey, error)
	DeleteAPIKey(context.Context, string, string) error
	CreateShareLink(context.Context, ShareLinkRecord) error
	ListShareLinks(context.Context) ([]model.ShareLink, error)
	ResolveShareLink(context.Context, string, time.Time) (model.ShareLink, error)
	RevokeShareLink(context.Context, string, time.Time) error
}

// SettingsRepository stores site-wide configuration and encrypted-setting references.
type SettingsRepository interface {
	GetSetting(context.Context, string) (string, error)
	SetSetting(context.Context, string, string) error
	SiteSettings(context.Context) (model.SiteSettings, error)
	SaveSiteSettings(context.Context, model.SiteSettings) error
}

// EnrollmentRepository owns short-lived credentials used only to create nodes.
type EnrollmentRepository interface {
	ReplaceTemporaryEnrollmentPIN(context.Context, TemporaryEnrollmentPIN, time.Time) error
	LatestTemporaryEnrollmentPIN(context.Context) (TemporaryEnrollmentPIN, error)
	ActiveTemporaryEnrollmentPIN(context.Context, time.Time) (TemporaryEnrollmentPIN, error)
	RevokeTemporaryEnrollmentPIN(context.Context, string, time.Time) error
}

// NodeRepository owns enrollment, node metadata, credentials, and Agent configuration.
type NodeRepository interface {
	EnrollNode(context.Context, EnrollParams) (EnrollmentRecord, error)
	AuthenticateAgent(context.Context, string, string) (model.Node, model.AgentConfig, error)
	ListNodes(context.Context, bool) ([]model.Node, error)
	ListLatencyNodes(context.Context, bool) ([]model.Node, error)
	GetNode(context.Context, string) (model.Node, error)
	UpdateNode(context.Context, model.Node) error
	UpdateTrafficCorrection(context.Context, string, int64, int64, *time.Time, time.Time) error
	DeleteNode(context.Context, string) error
	UpdateAgentSeen(context.Context, string, model.AgentIdentity, time.Time, string) error
	AgentConfig(context.Context, string) (model.AgentConfig, error)
	SaveAgentConfig(context.Context, string, model.AgentConfig) error
}

// MetricRepository is the durable telemetry and retention boundary.
type MetricRepository interface {
	SaveMetric(context.Context, model.MetricSample) error
	LatestMetric(context.Context, string) (model.MetricSample, error)
	LatestMetrics(context.Context, []string) (map[string]model.MetricSample, error)
	EnsureMetricPartitions(context.Context, time.Time) error
	RecentMetrics(context.Context, string, time.Time) ([]model.MetricSample, error)
	History(context.Context, HistoryQuery) ([]model.MetricSample, error)
	Rollup(context.Context, time.Time) error
	ApplyRetention(context.Context, model.SiteSettings, time.Time) error
}

// ProbeRepository stores both service probes and outbound latency-node measurements.
type ProbeRepository interface {
	ListProbeTasks(context.Context, string) ([]model.ProbeTask, error)
	ListAllProbeTasks(context.Context) ([]model.ProbeTask, error)
	SaveProbeTask(context.Context, model.ProbeTask) (model.ProbeTask, error)
	DeleteProbeTask(context.Context, int64) error
	SaveProbeResult(context.Context, model.ProbeResult) error
	ProbeHistory(context.Context, string, int64, time.Time, time.Time, int) ([]model.ProbeResult, error)
	LatestLatencyResults(context.Context, time.Time) ([]model.LatencyResult, error)
	LatencyHistory(context.Context, string, string, time.Time, time.Time, int) ([]model.LatencyResult, error)
	LatencyWindowSummary(context.Context, string, string, time.Time, time.Time) (model.LatencyWindowSummary, error)
}

// AlertRepository owns rules, immutable event snapshots, and durable deliveries.
type AlertRepository interface {
	ListAlertRules(context.Context) ([]model.AlertRule, error)
	SaveAlertRule(context.Context, model.AlertRule) (model.AlertRule, error)
	DeleteAlertRule(context.Context, int64) error
	SaveAlertEvent(context.Context, model.AlertEvent, *int64) error
	ListAlertEvents(context.Context, int) ([]model.AlertEvent, error)
	ListNotificationChannels(context.Context) ([]model.NotificationChannel, error)
	SaveNotificationChannel(context.Context, model.NotificationChannel, string) (model.NotificationChannel, error)
	DeleteNotificationChannel(context.Context, int64) error
	EnqueueNotificationDeliveries(context.Context, string, time.Time) error
	DueNotificationDeliveries(context.Context, time.Time, int) ([]model.NotificationDelivery, error)
	UpdateNotificationDelivery(context.Context, string, string, int, time.Time, string) error
}

// ThemeRepository is intentionally separate from the monitoring repositories.
// Third-party theme compatibility cannot mutate core telemetry through this API.
type ThemeRepository interface {
	ListThemes(context.Context) ([]model.Theme, error)
	GetTheme(context.Context, string) (model.Theme, error)
	SaveTheme(context.Context, model.Theme) error
	DeleteTheme(context.Context, string) error
}

type AuditRepository interface {
	AppendAudit(context.Context, string, string, string, string, time.Time) error
	ListAudit(context.Context, int) ([]model.AuditEntry, error)
}

// Store composes explicit capability interfaces. Subsystems should accept the
// narrowest capability they need; the HTTP composition root uses the full set.
type Store interface {
	LifecycleRepository
	IdentityRepository
	SettingsRepository
	EnrollmentRepository
	NodeRepository
	MetricRepository
	ProbeRepository
	AlertRepository
	ThemeRepository
	AuditRepository
}
