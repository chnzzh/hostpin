CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at INTEGER NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS admins (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  totp_secret_enc TEXT NOT NULL DEFAULT '',
  recovery_hashes TEXT NOT NULL DEFAULT '[]',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  csrf_hash TEXT NOT NULL,
  ip_address TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
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
  latitude REAL,
  longitude REAL,
  tags TEXT NOT NULL DEFAULT '[]',
  public_remark TEXT NOT NULL DEFAULT '',
  private_remark TEXT NOT NULL DEFAULT '',
  hidden INTEGER NOT NULL DEFAULT 0,
  weight INTEGER NOT NULL DEFAULT 0,
  price REAL NOT NULL DEFAULT 0,
  currency TEXT NOT NULL DEFAULT '$',
  billing_cycle_days INTEGER NOT NULL DEFAULT 30,
  expires_at INTEGER,
  auto_renewal INTEGER NOT NULL DEFAULT 0,
  traffic_limit INTEGER NOT NULL DEFAULT 0,
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
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  last_seen_at INTEGER
);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_nodes_group ON nodes(node_group, weight DESC, name);
-- hostpin:split
CREATE TABLE IF NOT EXISTS agent_credentials (
  node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
  install_id TEXT NOT NULL UNIQUE,
  token_id TEXT NOT NULL UNIQUE,
  token_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  rotated_at INTEGER NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS agent_configs (
  node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
  config_json TEXT NOT NULL,
  config_version INTEGER NOT NULL DEFAULT 1,
  updated_at INTEGER NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS metrics_raw (
  node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  ts INTEGER NOT NULL,
  sequence INTEGER NOT NULL DEFAULT 0,
  collected_at INTEGER NOT NULL,
  boot_id TEXT NOT NULL DEFAULT '',
  clock_offset_ms INTEGER NOT NULL DEFAULT 0,
  cpu REAL NOT NULL DEFAULT 0,
  load1 REAL NOT NULL DEFAULT 0,
  load5 REAL NOT NULL DEFAULT 0,
  load15 REAL NOT NULL DEFAULT 0,
  memory_total INTEGER NOT NULL DEFAULT 0,
  memory_used INTEGER NOT NULL DEFAULT 0,
  swap_total INTEGER NOT NULL DEFAULT 0,
  swap_used INTEGER NOT NULL DEFAULT 0,
  disk_total INTEGER NOT NULL DEFAULT 0,
  disk_used INTEGER NOT NULL DEFAULT 0,
  net_rx_bps REAL NOT NULL DEFAULT 0,
  net_tx_bps REAL NOT NULL DEFAULT 0,
  net_rx_bytes INTEGER NOT NULL DEFAULT 0,
  net_tx_bytes INTEGER NOT NULL DEFAULT 0,
  monthly_rx_bytes INTEGER NOT NULL DEFAULT 0,
  monthly_tx_bytes INTEGER NOT NULL DEFAULT 0,
  tcp_connections INTEGER NOT NULL DEFAULT 0,
  udp_connections INTEGER NOT NULL DEFAULT 0,
  processes INTEGER NOT NULL DEFAULT 0,
  temperature REAL NOT NULL DEFAULT 0,
  uptime_seconds INTEGER NOT NULL DEFAULT 0,
  details_json TEXT NOT NULL DEFAULT '{}',
  message TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(node_id, ts)
);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_metrics_raw_ts ON metrics_raw(ts);
-- hostpin:split
CREATE TABLE IF NOT EXISTS metrics_5m AS SELECT * FROM metrics_raw WHERE 0;
-- hostpin:split
CREATE UNIQUE INDEX IF NOT EXISTS idx_metrics_5m_node_ts ON metrics_5m(node_id, ts);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_metrics_5m_ts ON metrics_5m(ts);
-- hostpin:split
CREATE TABLE IF NOT EXISTS metrics_1h AS SELECT * FROM metrics_raw WHERE 0;
-- hostpin:split
CREATE UNIQUE INDEX IF NOT EXISTS idx_metrics_1h_node_ts ON metrics_1h(node_id, ts);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_metrics_1h_ts ON metrics_1h(ts);
-- hostpin:split
CREATE TABLE IF NOT EXISTS probe_tasks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  target TEXT NOT NULL,
  interval_seconds INTEGER NOT NULL DEFAULT 60,
  timeout_seconds INTEGER NOT NULL DEFAULT 5,
  expected_status INTEGER NOT NULL DEFAULT 0,
  expected_value TEXT NOT NULL DEFAULT '',
  node_ids TEXT NOT NULL DEFAULT '[]',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS probe_results (
  node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  task_id INTEGER NOT NULL REFERENCES probe_tasks(id) ON DELETE CASCADE,
  ts INTEGER NOT NULL,
  collected_at INTEGER NOT NULL,
  success INTEGER NOT NULL,
  latency_ms REAL NOT NULL DEFAULT 0,
  status_code INTEGER NOT NULL DEFAULT 0,
  value TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(node_id, task_id, ts)
);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_probe_results_lookup ON probe_results(node_id, task_id, ts);
-- hostpin:split
CREATE TABLE IF NOT EXISTS alert_rules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  metric TEXT NOT NULL,
  operator TEXT NOT NULL,
  threshold REAL NOT NULL,
  recovery_threshold REAL NOT NULL,
  duration_seconds INTEGER NOT NULL DEFAULT 0,
  cooldown_seconds INTEGER NOT NULL DEFAULT 1800,
  severity TEXT NOT NULL DEFAULT 'warning',
  scope_json TEXT NOT NULL DEFAULT '{}',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS alert_events (
  id TEXT PRIMARY KEY,
  rule_id INTEGER,
  node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  event_type TEXT NOT NULL,
  status TEXT NOT NULL,
  severity TEXT NOT NULL,
  metric TEXT NOT NULL DEFAULT '',
  value REAL NOT NULL DEFAULT 0,
  threshold REAL NOT NULL DEFAULT 0,
  message TEXT NOT NULL,
  occurred_at INTEGER NOT NULL,
  resolved_at INTEGER
);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_alert_events_time ON alert_events(occurred_at DESC);
-- hostpin:split
CREATE TABLE IF NOT EXISTS notification_channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  config_enc TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS notification_deliveries (
  id TEXT PRIMARY KEY,
  event_id TEXT NOT NULL REFERENCES alert_events(id) ON DELETE CASCADE,
  channel_id INTEGER NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
  status TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL,
  last_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS themes (
  short TEXT PRIMARY KEY,
  manifest_json TEXT NOT NULL,
  settings_json TEXT NOT NULL DEFAULT '{}',
  source_url TEXT NOT NULL DEFAULT '',
  checksum TEXT NOT NULL DEFAULT '',
  installed_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS geoip_cache (
  ip TEXT PRIMARY KEY,
  country_code TEXT NOT NULL DEFAULT '',
  region TEXT NOT NULL DEFAULT '',
  city TEXT NOT NULL DEFAULT '',
  latitude REAL,
  longitude REAL,
  expires_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
-- hostpin:split
CREATE TABLE IF NOT EXISTS audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  target TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  occurred_at INTEGER NOT NULL
);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_audit_time ON audit_log(occurred_at DESC);

