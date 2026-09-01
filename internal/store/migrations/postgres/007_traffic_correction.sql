ALTER TABLE nodes ADD COLUMN traffic_rx_correction BIGINT NOT NULL DEFAULT 0;
-- hostpin:split
ALTER TABLE nodes ADD COLUMN traffic_tx_correction BIGINT NOT NULL DEFAULT 0;
-- hostpin:split
ALTER TABLE nodes ADD COLUMN traffic_correction_period_start BIGINT;
-- hostpin:split
ALTER TABLE nodes ADD COLUMN traffic_correction_updated_at BIGINT;
