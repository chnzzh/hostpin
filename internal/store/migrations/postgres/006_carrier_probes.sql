CREATE UNIQUE INDEX IF NOT EXISTS idx_probe_tasks_carrier_purpose
ON probe_tasks(purpose)
WHERE purpose IN ('carrier.telecom', 'carrier.unicom', 'carrier.mobile');
