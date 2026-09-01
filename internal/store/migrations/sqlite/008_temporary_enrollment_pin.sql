CREATE TABLE IF NOT EXISTS temporary_enrollment_pins (
  id TEXT PRIMARY KEY,
  pin_hash TEXT NOT NULL,
  claimed_install_id TEXT NOT NULL DEFAULT '',
  claimed_token_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at INTEGER,
  revoked_at INTEGER
);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_temporary_enrollment_pins_created ON temporary_enrollment_pins(created_at DESC);
-- hostpin:split
CREATE INDEX IF NOT EXISTS idx_temporary_enrollment_pins_active ON temporary_enrollment_pins(expires_at, revoked_at);
