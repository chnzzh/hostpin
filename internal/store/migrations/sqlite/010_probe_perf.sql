DROP INDEX IF EXISTS idx_probe_results_lookup;
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_probe_results_ts ON probe_results(ts);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_probe_results_summary ON probe_results(node_id, task_id, ts, success, latency_ms, loss_percent);
