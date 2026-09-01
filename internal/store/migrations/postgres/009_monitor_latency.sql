ALTER TABLE nodes ADD COLUMN latency_enabled BOOLEAN NOT NULL DEFAULT FALSE;
-- hostpin:split
UPDATE nodes SET latency_enabled = TRUE WHERE role = 'probe';
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_nodes_latency_enabled ON nodes(latency_enabled, hidden, weight, name);
