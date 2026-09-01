CREATE TABLE IF NOT EXISTS api_keys (
  id TEXT PRIMARY KEY,
  admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  token_id TEXT NOT NULL UNIQUE,
  token_hash TEXT NOT NULL,
  scopes_json TEXT NOT NULL DEFAULT '["admin"]',
  last_used_at BIGINT,
  expires_at BIGINT,
  created_at BIGINT NOT NULL
);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_api_keys_token ON api_keys(token_id);
-- hostpin:split
CREATE TABLE IF NOT EXISTS share_links (
  id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  node_ids TEXT NOT NULL DEFAULT '[]',
  expires_at BIGINT NOT NULL,
  created_at BIGINT NOT NULL,
  revoked_at BIGINT
);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_share_links_expiry ON share_links(expires_at);
