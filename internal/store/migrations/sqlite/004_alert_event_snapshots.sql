CREATE TABLE hostpin_alert_events_v4 (
  id TEXT PRIMARY KEY,
  rule_id INTEGER,
  node_id TEXT NOT NULL,
  node_json TEXT NOT NULL DEFAULT '{}',
  event_type TEXT NOT NULL,
  status TEXT NOT NULL,
  severity TEXT NOT NULL,
  metric TEXT NOT NULL DEFAULT '',
  value REAL NOT NULL DEFAULT 0,
  threshold REAL NOT NULL DEFAULT 0,
  message TEXT NOT NULL,
  link TEXT NOT NULL DEFAULT '',
  occurred_at INTEGER NOT NULL,
  resolved_at INTEGER
);
-- hostpin:split
INSERT INTO hostpin_alert_events_v4(
  id, rule_id, node_id, node_json, event_type, status, severity, metric,
  value, threshold, message, link, occurred_at, resolved_at
)
SELECT id, rule_id, node_id, '{}', event_type, status, severity, metric,
  value, threshold, message, '', occurred_at, resolved_at
FROM alert_events;
-- hostpin:split
CREATE TABLE hostpin_notification_deliveries_v4 (
  id TEXT PRIMARY KEY,
  event_id TEXT NOT NULL REFERENCES hostpin_alert_events_v4(id) ON DELETE CASCADE,
  channel_id INTEGER NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
  status TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL,
  last_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
-- hostpin:split
INSERT INTO hostpin_notification_deliveries_v4(
  id, event_id, channel_id, status, attempts, next_attempt_at, last_error,
  created_at, updated_at
)
SELECT id, event_id, channel_id, status, attempts, next_attempt_at, last_error,
  created_at, updated_at
FROM notification_deliveries;
-- hostpin:split
DROP TABLE notification_deliveries;
-- hostpin:split
DROP TABLE alert_events;
-- hostpin:split
ALTER TABLE hostpin_alert_events_v4 RENAME TO alert_events;
-- hostpin:split
ALTER TABLE hostpin_notification_deliveries_v4 RENAME TO notification_deliveries;
-- hostpin:split
CREATE INDEX idx_alert_events_time ON alert_events(occurred_at DESC);
