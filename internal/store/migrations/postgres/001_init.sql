CREATE TABLE IF NOT EXISTS schema_migrations (
  version BIGINT PRIMARY KEY,
  applied_at BIGINT NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at BIGINT NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS admins (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  totp_secret_enc TEXT NOT NULL DEFAULT '',
  recovery_hashes TEXT NOT NULL DEFAULT '[]',
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  csrf_hash TEXT NOT NULL,
  ip_address TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL
);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
-- hostpin:split
CREATE TABLE IF NOT EXISTS nodes (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  hostname TEXT NOT NULL DEFAULT '',
  node_group TEXT NOT NULL DEFAULT '',
  region TEXT NOT NULL DEFAULT '',
  country_code TEXT NOT NULL DEFAULT '',
  latitude DOUBLE PRECISION,
  longitude DOUBLE PRECISION,
  tags TEXT NOT NULL DEFAULT '[]',
  public_remark TEXT NOT NULL DEFAULT '',
  private_remark TEXT NOT NULL DEFAULT '',
  hidden BOOLEAN NOT NULL DEFAULT FALSE,
  weight INTEGER NOT NULL DEFAULT 0,
  price DOUBLE PRECISION NOT NULL DEFAULT 0,
  currency TEXT NOT NULL DEFAULT '$',
  billing_cycle_days INTEGER NOT NULL DEFAULT 30,
  expires_at BIGINT,
  auto_renewal BOOLEAN NOT NULL DEFAULT FALSE,
  traffic_limit BIGINT NOT NULL DEFAULT 0,
  traffic_limit_type TEXT NOT NULL DEFAULT 'sum',
  traffic_reset_day INTEGER NOT NULL DEFAULT 1,
  agent_version TEXT NOT NULL DEFAULT '',
  os TEXT NOT NULL DEFAULT '',
  arch TEXT NOT NULL DEFAULT '',
  cpu_name TEXT NOT NULL DEFAULT '',
  cpu_cores INTEGER NOT NULL DEFAULT 0,
  virtualization TEXT NOT NULL DEFAULT '',
  kernel_version TEXT NOT NULL DEFAULT '',
  ipv4 TEXT NOT NULL DEFAULT '',
  ipv6 TEXT NOT NULL DEFAULT '',
  source_ip TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  last_seen_at BIGINT
);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_nodes_group ON nodes(node_group, weight DESC, name);
-- hostpin:split
CREATE TABLE IF NOT EXISTS agent_credentials (
  node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
  install_id TEXT NOT NULL UNIQUE,
  token_id TEXT NOT NULL UNIQUE,
  token_hash TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  rotated_at BIGINT NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS agent_configs (
  node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
  config_json TEXT NOT NULL,
  config_version BIGINT NOT NULL DEFAULT 1,
  updated_at BIGINT NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS metrics_raw (
  node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  ts BIGINT NOT NULL,
  sequence BIGINT NOT NULL DEFAULT 0,
  collected_at BIGINT NOT NULL,
  boot_id TEXT NOT NULL DEFAULT '',
  clock_offset_ms BIGINT NOT NULL DEFAULT 0,
  cpu DOUBLE PRECISION NOT NULL DEFAULT 0,
  load1 DOUBLE PRECISION NOT NULL DEFAULT 0,
  load5 DOUBLE PRECISION NOT NULL DEFAULT 0,
  load15 DOUBLE PRECISION NOT NULL DEFAULT 0,
  memory_total BIGINT NOT NULL DEFAULT 0,
  memory_used BIGINT NOT NULL DEFAULT 0,
  swap_total BIGINT NOT NULL DEFAULT 0,
  swap_used BIGINT NOT NULL DEFAULT 0,
  disk_total BIGINT NOT NULL DEFAULT 0,
  disk_used BIGINT NOT NULL DEFAULT 0,
  net_rx_bps DOUBLE PRECISION NOT NULL DEFAULT 0,
  net_tx_bps DOUBLE PRECISION NOT NULL DEFAULT 0,
  net_rx_bytes BIGINT NOT NULL DEFAULT 0,
  net_tx_bytes BIGINT NOT NULL DEFAULT 0,
  monthly_rx_bytes BIGINT NOT NULL DEFAULT 0,
  monthly_tx_bytes BIGINT NOT NULL DEFAULT 0,
  tcp_connections INTEGER NOT NULL DEFAULT 0,
  udp_connections INTEGER NOT NULL DEFAULT 0,
  processes INTEGER NOT NULL DEFAULT 0,
  temperature DOUBLE PRECISION NOT NULL DEFAULT 0,
  uptime_seconds BIGINT NOT NULL DEFAULT 0,
  details_json TEXT NOT NULL DEFAULT '{}',
  message TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(node_id, ts)
) PARTITION BY RANGE (ts);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_metrics_raw_ts ON metrics_raw(ts);
-- hostpin:split
CREATE TABLE IF NOT EXISTS metrics_5m (LIKE metrics_raw INCLUDING DEFAULTS INCLUDING CONSTRAINTS) PARTITION BY RANGE (ts);
-- hostpin:split
CREATE UNIQUE INDEX IF NOT EXISTS idx_metrics_5m_node_ts ON metrics_5m(node_id, ts);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_metrics_5m_ts ON metrics_5m(ts);
-- hostpin:split
CREATE TABLE IF NOT EXISTS metrics_1h (LIKE metrics_raw INCLUDING DEFAULTS INCLUDING CONSTRAINTS) PARTITION BY RANGE (ts);
-- hostpin:split
CREATE UNIQUE INDEX IF NOT EXISTS idx_metrics_1h_node_ts ON metrics_1h(node_id, ts);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_metrics_1h_ts ON metrics_1h(ts);
-- hostpin:split
CREATE TABLE IF NOT EXISTS probe_tasks (
  id BIGSERIAL PRIMARY KEY,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  target TEXT NOT NULL,
  interval_seconds INTEGER NOT NULL DEFAULT 60,
  timeout_seconds INTEGER NOT NULL DEFAULT 5,
  expected_status INTEGER NOT NULL DEFAULT 0,
  expected_value TEXT NOT NULL DEFAULT '',
  node_ids TEXT NOT NULL DEFAULT '[]',
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS probe_results (
  node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  task_id BIGINT NOT NULL REFERENCES probe_tasks(id) ON DELETE CASCADE,
  ts BIGINT NOT NULL,
  collected_at BIGINT NOT NULL,
  success BOOLEAN NOT NULL,
  latency_ms DOUBLE PRECISION NOT NULL DEFAULT 0,
  status_code INTEGER NOT NULL DEFAULT 0,
  value TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(node_id, task_id, ts)
);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_probe_results_lookup ON probe_results(node_id, task_id, ts);
-- hostpin:split
CREATE TABLE IF NOT EXISTS alert_rules (
  id BIGSERIAL PRIMARY KEY,
  name TEXT NOT NULL,
  metric TEXT NOT NULL,
  operator TEXT NOT NULL,
  threshold DOUBLE PRECISION NOT NULL,
  recovery_threshold DOUBLE PRECISION NOT NULL,
  duration_seconds INTEGER NOT NULL DEFAULT 0,
  cooldown_seconds INTEGER NOT NULL DEFAULT 1800,
  severity TEXT NOT NULL DEFAULT 'warning',
  scope_json TEXT NOT NULL DEFAULT '{}',
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS alert_events (
  id TEXT PRIMARY KEY,
  rule_id BIGINT,
  node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  event_type TEXT NOT NULL,
  status TEXT NOT NULL,
  severity TEXT NOT NULL,
  metric TEXT NOT NULL DEFAULT '',
  value DOUBLE PRECISION NOT NULL DEFAULT 0,
  threshold DOUBLE PRECISION NOT NULL DEFAULT 0,
  message TEXT NOT NULL,
  occurred_at BIGINT NOT NULL,
  resolved_at BIGINT
);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_alert_events_time ON alert_events(occurred_at DESC);
-- hostpin:split
CREATE TABLE IF NOT EXISTS notification_channels (
  id BIGSERIAL PRIMARY KEY,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  config_enc TEXT NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS notification_deliveries (
  id TEXT PRIMARY KEY,
  event_id TEXT NOT NULL REFERENCES alert_events(id) ON DELETE CASCADE,
  channel_id BIGINT NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
  status TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at BIGINT NOT NULL,
  last_error TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS themes (
  short TEXT PRIMARY KEY,
  manifest_json TEXT NOT NULL,
  settings_json TEXT NOT NULL DEFAULT '{}',
  source_url TEXT NOT NULL DEFAULT '',
  checksum TEXT NOT NULL DEFAULT '',
  installed_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS geoip_cache (
  ip TEXT PRIMARY KEY,
  country_code TEXT NOT NULL DEFAULT '',
  region TEXT NOT NULL DEFAULT '',
  city TEXT NOT NULL DEFAULT '',
  latitude DOUBLE PRECISION,
  longitude DOUBLE PRECISION,
  expires_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS audit_log (
  id BIGSERIAL PRIMARY KEY,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  target TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  occurred_at BIGINT NOT NULL
);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_audit_time ON audit_log(occurred_at DESC);
