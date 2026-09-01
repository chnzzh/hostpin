ALTER TABLE alert_events DROP CONSTRAINT IF EXISTS alert_events_node_id_fkey;
-- hostpin:split
ALTER TABLE alert_events ADD COLUMN IF NOT EXISTS node_json TEXT NOT NULL DEFAULT '{}';
-- hostpin:split
ALTER TABLE alert_events ADD COLUMN IF NOT EXISTS link TEXT NOT NULL DEFAULT '';
