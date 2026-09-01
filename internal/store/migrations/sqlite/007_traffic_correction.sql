ALTER TABLE nodes ADD COLUMN traffic_rx_correction INTEGER NOT NULL DEFAULT 0;
-- hostpin:split
ALTER TABLE nodes ADD COLUMN traffic_tx_correction INTEGER NOT NULL DEFAULT 0;
-- hostpin:split
ALTER TABLE nodes ADD COLUMN traffic_correction_period_start INTEGER;
-- hostpin:split
ALTER TABLE nodes ADD COLUMN traffic_correction_updated_at INTEGER;
