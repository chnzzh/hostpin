CREATE INDEX IF NOT EXISTS idx_probe_results_task_ts ON probe_results(task_id, ts);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_alert_events_node ON alert_events(node_id);
