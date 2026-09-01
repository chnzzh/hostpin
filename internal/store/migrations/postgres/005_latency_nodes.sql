ALTER TABLE nodes ADD COLUMN role TEXT NOT NULL DEFAULT 'monitor';
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_nodes_role_visibility ON nodes(role, hidden, weight, name);
-- hostpin:split
ALTER TABLE probe_tasks ADD COLUMN purpose TEXT NOT NULL DEFAULT 'custom';
-- hostpin:split
ALTER TABLE probe_tasks ADD COLUMN run_on TEXT NOT NULL DEFAULT 'monitor';
-- hostpin:split
ALTER TABLE probe_tasks ADD COLUMN target_node_id TEXT NOT NULL DEFAULT '';
-- hostpin:split
ALTER TABLE probe_tasks ADD COLUMN public BOOLEAN NOT NULL DEFAULT FALSE;
-- hostpin:split
ALTER TABLE probe_tasks ADD COLUMN samples INTEGER NOT NULL DEFAULT 1;
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_probe_tasks_purpose ON probe_tasks(purpose, target_node_id, enabled);
-- hostpin:split
CREATE UNIQUE INDEX IF NOT EXISTS idx_probe_tasks_latency_target ON probe_tasks(target_node_id) WHERE purpose = 'latency';
-- hostpin:split
ALTER TABLE probe_results ADD COLUMN loss_percent DOUBLE PRECISION NOT NULL DEFAULT 0;
