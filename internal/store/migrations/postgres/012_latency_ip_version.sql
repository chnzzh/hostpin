ALTER TABLE probe_tasks ADD COLUMN ip_version INTEGER NOT NULL DEFAULT 0;
-- hostpin:split
UPDATE probe_tasks SET ip_version = CASE WHEN target LIKE '[%]:%' OR (type = 'icmp' AND target LIKE '%:%') THEN 6 ELSE 4 END WHERE purpose = 'latency' AND ip_version = 0;
-- hostpin:split
DROP INDEX IF EXISTS idx_probe_tasks_latency_target;
-- hostpin:split
CREATE UNIQUE INDEX IF NOT EXISTS idx_probe_tasks_latency_target_v2 ON probe_tasks(target_node_id, ip_version, type) WHERE purpose = 'latency';
