ALTER TABLE nodes ADD COLUMN latency_enabled INTEGER NOT NULL DEFAULT 0;
-- hostpin:split
UPDATE nodes SET latency_enabled = 1 WHERE role = 'probe';
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_nodes_latency_enabled ON nodes(latency_enabled, hidden, weight, name);
