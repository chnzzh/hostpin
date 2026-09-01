export interface SiteSettings {
  name: string
  description: string
  private: boolean
  enrollment_enabled: boolean
  enrollment_pin_weak: boolean
  theme: string
  record_enabled: boolean
  raw_retention_hours: number
  five_minute_retention_hours: number
  hourly_retention_hours: number
  geoip_enabled: boolean
  geoip_provider: string
  custom_head?: string
  custom_body?: string
  theme_market_sources?: string[]
}

export interface TemporaryEnrollmentPIN {
  id: string
  pin?: string
  status: 'active' | 'used' | 'expired' | 'revoked'
  created_at: string
  expires_at: string
  used_at?: string
  revoked_at?: string
}

export interface PublicNode {
  id: string
  role: 'monitor' | 'probe'
  latency_enabled: boolean
  name: string
  group?: string
  region?: string
  country_code?: string
  latitude?: number
  longitude?: number
  tags: string[]
  public_remark?: string
  hidden: boolean
  weight: number
  price: number
  currency: string
  billing_cycle_days: number
  expires_at?: string
  auto_renewal: boolean
  traffic_limit: number
  traffic_limit_type: string
  traffic_reset_day: number
  os?: string
  arch?: string
  cpu_name?: string
  cpu_cores?: number
  virtualization?: string
  kernel_version?: string
  created_at: string
  updated_at: string
  last_seen_at?: string
  online: boolean
}

export interface AdminNode extends PublicNode {
  hostname?: string
  private_remark?: string
  agent_version?: string
  ipv4?: string
  ipv6?: string
}

export interface TrafficCorrectionStatus {
  available: boolean
  active: boolean
  period_start: string
  sample_received_at?: string
  raw_rx_bytes: number
  raw_tx_bytes: number
  rx_bytes: number
  tx_bytes: number
  rx_adjustment: number
  tx_adjustment: number
  updated_at?: string
}

export interface AgentConfig {
  collect_interval_seconds: number
  persist_interval_seconds: number
  include_nics?: string[]
  exclude_nics?: string[]
  include_mountpoints?: string[]
  enable_gpu: boolean
  auto_update: boolean
  probe_concurrency: number
  config_version: number
}

export interface DiskMetric {
  mountpoint: string
  filesystem?: string
  total: number
  used: number
  read_bps?: number
  write_bps?: number
  read_iops?: number
  write_iops?: number
}

export interface NetworkMetric {
  interface: string
  rx_bps: number
  tx_bps: number
  rx_bytes: number
  tx_bytes: number
}

export interface GPUMetric {
  index: number
  name: string
  utilization: number
  memory_total?: number
  memory_used?: number
  temperature?: number
}

export interface MetricSample {
  node_id?: string
  sequence: number
  collected_at: string
  received_at?: string
  boot_id?: string
  clock_offset_ms?: number
  cpu: number
  load1: number
  load5: number
  load15: number
  memory_total: number
  memory_used: number
  swap_total: number
  swap_used: number
  disk_total: number
  disk_used: number
  net_rx_bps: number
  net_tx_bps: number
  net_rx_bytes: number
  net_tx_bytes: number
  monthly_rx_bytes?: number
  monthly_tx_bytes?: number
  tcp_connections: number
  udp_connections: number
  processes: number
  temperature?: number
  uptime_seconds: number
  disks?: DiskMetric[]
  networks?: NetworkMetric[]
  gpus?: GPUMetric[]
  message?: string
}

export interface NodeSnapshot {
  node: PublicNode
  metric?: MetricSample
}

export interface ProbeTask {
  id: number
  name: string
  type: 'icmp' | 'tcp' | 'http' | 'dns'
  target: string
  interval_seconds: number
  timeout_seconds: number
  expected_status?: number
  expected_value?: string
  node_ids?: string[]
  purpose?: 'custom' | 'latency' | 'carrier.telecom' | 'carrier.unicom' | 'carrier.mobile'
  run_on?: 'monitor' | 'probe'
  target_node_id?: string
  public: boolean
  samples: number
  enabled: boolean
  created_at?: string
  updated_at?: string
}

export interface ProbeResult {
  node_id?: string
  task_id: number
  collected_at: string
  received_at?: string
  success: boolean
  latency_ms: number
  loss_percent: number
  status_code?: number
  value?: string
  error?: string
}

export interface PublicProbeSnapshot {
  task: ProbeTask
  results: ProbeResult[]
}

export interface LatencyProbeNode {
  id: string
  name: string
  region?: string
  country_code?: string
  tags: string[]
  os?: string
  arch?: string
  last_seen_at?: string
  online: boolean
}

export interface LatencyTarget {
  task_id: number
  node: PublicNode
  type: 'icmp' | 'tcp'
  interval_seconds: number
  samples: number
}

export interface LatencyResult {
  probe_node_id: string
  target_node_id: string
  task_id: number
  collected_at: string
  received_at: string
  success: boolean
  latency_ms: number
  loss_percent: number
  error?: string
}

export interface LatencyWindowSummary {
  average_latency_ms: number
  average_loss_percent: number
  sample_count: number
  success_count: number
}

export interface LatencyOverview {
  probe_nodes: LatencyProbeNode[]
  targets: LatencyTarget[]
  latest: LatencyResult[]
  offline_after_ms: number
}

export interface AlertScope {
  groups?: string[]
  node_ids?: string[]
  excluded_node_ids?: string[]
}

export interface AlertRule {
  id: number
  name: string
  metric: string
  operator: string
  threshold: number
  recovery_threshold: number
  duration_seconds: number
  cooldown_seconds: number
  severity: 'info' | 'warning' | 'critical'
  scope: AlertScope
  enabled: boolean
  created_at?: string
  updated_at?: string
}

export interface AlertEvent {
  event_id: string
  type: string
  status: 'firing' | 'resolved'
  severity: string
  occurred_at: string
  node: PublicNode
  metric: string
  value: number
  threshold: number
  link: string
  message: string
}

export interface NotificationChannel {
  id: number
  name: string
  type: 'smtp' | 'telegram' | 'bark' | 'webhook'
  config?: Record<string, unknown>
  enabled: boolean
  created_at?: string
  updated_at?: string
}

export interface ThemeManifest {
  name: string | Record<string, string>
  short: string
  description?: string | Record<string, string>
  version?: string
  author?: string | Record<string, string>
  url?: string
  preview?: string
  configuration?: { type?: 'managed' | 'raw' | 'redirect'; icon?: string; name?: unknown; data?: unknown }
}

export interface Theme {
  manifest: ThemeManifest
  settings: Record<string, unknown>
  source_url?: string
  checksum: string
  installed_at: string
  updated_at: string
}

export interface AuditEntry {
  id: number
  actor: string
  action: string
  target: string
  detail: string
  occurred_at: string
}
