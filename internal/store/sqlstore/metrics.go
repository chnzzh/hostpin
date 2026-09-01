package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
)

const metricColumns = `node_id, ts, sequence, collected_at, boot_id, clock_offset_ms, cpu, load1, load5, load15, memory_total, memory_used, swap_total, swap_used, disk_total, disk_used, net_rx_bps, net_tx_bps, net_rx_bytes, net_tx_bytes, monthly_rx_bytes, monthly_tx_bytes, tcp_connections, udp_connections, processes, temperature, uptime_seconds, details_json, message`

type metricDetails struct {
	Disks    []model.DiskMetric    `json:"disks,omitempty"`
	Networks []model.NetworkMetric `json:"networks,omitempty"`
	GPUs     []model.GPUMetric     `json:"gpus,omitempty"`
}

const maxSignedMetricCounter uint64 = 1<<63 - 1

func metricCounter(value uint64) int64 {
	if value > maxSignedMetricCounter {
		return int64(maxSignedMetricCounter)
	}
	return int64(value)
}

func metricArgs(sample model.MetricSample) []any {
	details := encodeJSON(metricDetails{Disks: sample.Disks, Networks: sample.Networks, GPUs: sample.GPUs})
	return []any{
		sample.NodeID, millis(sample.ReceivedAt), sample.Sequence, millis(sample.CollectedAt),
		sample.BootID, sample.ClockOffsetMS, sample.CPU, sample.Load1, sample.Load5,
		sample.Load15, metricCounter(sample.MemoryTotal), metricCounter(sample.MemoryUsed),
		metricCounter(sample.SwapTotal), metricCounter(sample.SwapUsed), metricCounter(sample.DiskTotal),
		metricCounter(sample.DiskUsed), sample.NetRxBPS, sample.NetTxBPS,
		metricCounter(sample.NetRxBytes), metricCounter(sample.NetTxBytes), metricCounter(sample.MonthlyRxBytes),
		metricCounter(sample.MonthlyTxBytes), sample.TCPConnections, sample.UDPConnections,
		sample.Processes, sample.Temperature, metricCounter(sample.UptimeSeconds), details, sample.Message,
	}
}

func (s *SQLStore) SaveMetric(ctx context.Context, sample model.MetricSample) error {
	if sample.ReceivedAt.IsZero() {
		sample.ReceivedAt = time.Now().UTC()
	}
	if sample.CollectedAt.IsZero() {
		sample.CollectedAt = sample.ReceivedAt
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", 29), ",")
	query := `INSERT INTO metrics_raw(` + metricColumns + `) VALUES(` + placeholders + `)
		ON CONFLICT(node_id, ts) DO UPDATE SET
		sequence=excluded.sequence, collected_at=excluded.collected_at, boot_id=excluded.boot_id,
		clock_offset_ms=excluded.clock_offset_ms, cpu=excluded.cpu, load1=excluded.load1,
		load5=excluded.load5, load15=excluded.load15, memory_total=excluded.memory_total,
		memory_used=excluded.memory_used, swap_total=excluded.swap_total, swap_used=excluded.swap_used,
		disk_total=excluded.disk_total, disk_used=excluded.disk_used, net_rx_bps=excluded.net_rx_bps,
		net_tx_bps=excluded.net_tx_bps, net_rx_bytes=excluded.net_rx_bytes,
		net_tx_bytes=excluded.net_tx_bytes, monthly_rx_bytes=excluded.monthly_rx_bytes,
		monthly_tx_bytes=excluded.monthly_tx_bytes, tcp_connections=excluded.tcp_connections,
		udp_connections=excluded.udp_connections, processes=excluded.processes,
		temperature=excluded.temperature, uptime_seconds=excluded.uptime_seconds,
		details_json=excluded.details_json, message=excluded.message`
	_, err := s.db.ExecContext(ctx, s.q(query), metricArgs(sample)...)
	return err
}

func scanMetric(row rowScanner) (model.MetricSample, error) {
	var sample model.MetricSample
	var ts, collectedAt int64
	var sequence int64
	var memoryTotal, memoryUsed, swapTotal, swapUsed, diskTotal, diskUsed int64
	var netRxBytes, netTxBytes, monthlyRxBytes, monthlyTxBytes, uptimeSeconds int64
	var detailsRaw string
	err := row.Scan(
		&sample.NodeID, &ts, &sequence, &collectedAt, &sample.BootID, &sample.ClockOffsetMS,
		&sample.CPU, &sample.Load1, &sample.Load5, &sample.Load15, &memoryTotal,
		&memoryUsed, &swapTotal, &swapUsed, &diskTotal, &diskUsed, &sample.NetRxBPS,
		&sample.NetTxBPS, &netRxBytes, &netTxBytes, &monthlyRxBytes, &monthlyTxBytes,
		&sample.TCPConnections, &sample.UDPConnections, &sample.Processes,
		&sample.Temperature, &uptimeSeconds, &detailsRaw, &sample.Message,
	)
	if err != nil {
		return model.MetricSample{}, mapSQLError(err)
	}
	sample.Sequence = uint64(max(sequence, 0))
	sample.ReceivedAt = timeFromMillis(ts)
	sample.CollectedAt = timeFromMillis(collectedAt)
	sample.MemoryTotal = uint64(max(memoryTotal, 0))
	sample.MemoryUsed = uint64(max(memoryUsed, 0))
	sample.SwapTotal = uint64(max(swapTotal, 0))
	sample.SwapUsed = uint64(max(swapUsed, 0))
	sample.DiskTotal = uint64(max(diskTotal, 0))
	sample.DiskUsed = uint64(max(diskUsed, 0))
	sample.NetRxBytes = uint64(max(netRxBytes, 0))
	sample.NetTxBytes = uint64(max(netTxBytes, 0))
	sample.MonthlyRxBytes = uint64(max(monthlyRxBytes, 0))
	sample.MonthlyTxBytes = uint64(max(monthlyTxBytes, 0))
	sample.UptimeSeconds = uint64(max(uptimeSeconds, 0))
	var details metricDetails
	_ = json.Unmarshal([]byte(detailsRaw), &details)
	sample.Disks, sample.Networks, sample.GPUs = details.Disks, details.Networks, details.GPUs
	return sample, nil
}

func (s *SQLStore) LatestMetric(ctx context.Context, nodeID string) (model.MetricSample, error) {
	query := `SELECT ` + metricColumns + ` FROM metrics_raw WHERE node_id = ? ORDER BY ts DESC LIMIT 1`
	return scanMetric(s.db.QueryRowContext(ctx, s.q(query), nodeID))
}

func (s *SQLStore) LatestMetrics(ctx context.Context, nodeIDs []string) (map[string]model.MetricSample, error) {
	query := `SELECT ` + strings.ReplaceAll(metricColumns, "node_id", "m.node_id") + ` FROM metrics_raw m JOIN (SELECT node_id, MAX(ts) AS max_ts FROM metrics_raw`
	args := make([]any, 0, len(nodeIDs))
	if len(nodeIDs) > 0 {
		query += ` WHERE node_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(nodeIDs)), ",") + `)`
		for _, id := range nodeIDs {
			args = append(args, id)
		}
	}
	query += ` GROUP BY node_id) latest ON latest.node_id = m.node_id AND latest.max_ts = m.ts`
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]model.MetricSample)
	for rows.Next() {
		sample, err := scanMetric(rows)
		if err != nil {
			return nil, err
		}
		result[sample.NodeID] = sample
	}
	return result, rows.Err()
}

func (s *SQLStore) RecentMetrics(ctx context.Context, nodeID string, since time.Time) ([]model.MetricSample, error) {
	query := `SELECT ` + metricColumns + ` FROM metrics_raw WHERE node_id = ? AND ts >= ? ORDER BY ts ASC`
	return s.readMetrics(ctx, query, nodeID, millis(since))
}

func (s *SQLStore) History(ctx context.Context, query store.HistoryQuery) ([]model.MetricSample, error) {
	if query.End.IsZero() {
		query.End = time.Now().UTC()
	}
	if query.Start.IsZero() || !query.Start.Before(query.End) {
		query.Start = query.End.Add(-time.Hour)
	}
	table := "metrics_raw"
	window := query.End.Sub(query.Start)
	if window > 90*24*time.Hour {
		table = "metrics_1h"
	} else if window > 7*24*time.Hour {
		table = "metrics_5m"
	}
	sqlQuery := `SELECT ` + metricColumns + ` FROM ` + table + ` WHERE node_id = ? AND ts >= ? AND ts <= ? ORDER BY ts ASC LIMIT 20000`
	rows, err := s.readMetrics(ctx, sqlQuery, query.NodeID, millis(query.Start), millis(query.End))
	if err != nil {
		return nil, err
	}
	if query.MaxPoints <= 0 {
		query.MaxPoints = 500
	}
	return downsample(rows, query.MaxPoints), nil
}

func (s *SQLStore) readMetrics(ctx context.Context, query string, args ...any) ([]model.MetricSample, error) {
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.MetricSample, 0)
	for rows.Next() {
		sample, err := scanMetric(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, sample)
	}
	return result, rows.Err()
}

func downsample(input []model.MetricSample, maxPoints int) []model.MetricSample {
	if maxPoints <= 0 || len(input) <= maxPoints {
		return input
	}
	if maxPoints == 1 {
		return []model.MetricSample{input[len(input)-1]}
	}
	output := make([]model.MetricSample, 0, maxPoints)
	step := float64(len(input)-1) / float64(maxPoints-1)
	last := -1
	for i := 0; i < maxPoints; i++ {
		index := int(float64(i) * step)
		if index == last {
			continue
		}
		output = append(output, input[index])
		last = index
	}
	if output[len(output)-1].ReceivedAt != input[len(input)-1].ReceivedAt {
		output[len(output)-1] = input[len(input)-1]
	}
	return output
}

func (s *SQLStore) Rollup(ctx context.Context, now time.Time) error {
	if err := s.EnsureMetricPartitions(ctx, now); err != nil {
		return err
	}
	if err := s.rollupTable(ctx, "metrics_raw", "metrics_5m", 5*time.Minute, now.Add(-48*time.Hour)); err != nil {
		return err
	}
	return s.rollupTable(ctx, "metrics_5m", "metrics_1h", time.Hour, now.Add(-14*24*time.Hour))
}

func (s *SQLStore) rollupTable(ctx context.Context, source, target string, bucket time.Duration, since time.Time) error {
	bucketMS := bucket.Milliseconds()
	query := `INSERT INTO ` + target + `(` + metricColumns + `)
	SELECT node_id, bucket_ts, MAX(sequence), MAX(collected_at), MAX(boot_id), CAST(AVG(clock_offset_ms) AS BIGINT),
	AVG(cpu), AVG(load1), AVG(load5), AVG(load15), MAX(memory_total), CAST(AVG(memory_used) AS BIGINT),
	MAX(swap_total), CAST(AVG(swap_used) AS BIGINT), MAX(disk_total), CAST(AVG(disk_used) AS BIGINT),
	AVG(net_rx_bps), AVG(net_tx_bps), MAX(net_rx_bytes), MAX(net_tx_bytes), MAX(monthly_rx_bytes),
	MAX(monthly_tx_bytes), CAST(AVG(tcp_connections) AS INTEGER), CAST(AVG(udp_connections) AS INTEGER),
	CAST(AVG(processes) AS INTEGER), AVG(temperature), MAX(uptime_seconds), '{}', ''
	FROM (SELECT *, (ts / ?)*? AS bucket_ts FROM ` + source + ` WHERE ts >= ?) AS bucketed
	GROUP BY node_id, bucket_ts
	ON CONFLICT(node_id, ts) DO UPDATE SET
	sequence=excluded.sequence, collected_at=excluded.collected_at, boot_id=excluded.boot_id,
	clock_offset_ms=excluded.clock_offset_ms, cpu=excluded.cpu, load1=excluded.load1,
	load5=excluded.load5, load15=excluded.load15, memory_total=excluded.memory_total,
	memory_used=excluded.memory_used, swap_total=excluded.swap_total, swap_used=excluded.swap_used,
	disk_total=excluded.disk_total, disk_used=excluded.disk_used, net_rx_bps=excluded.net_rx_bps,
	net_tx_bps=excluded.net_tx_bps, net_rx_bytes=excluded.net_rx_bytes,
	net_tx_bytes=excluded.net_tx_bytes, monthly_rx_bytes=excluded.monthly_rx_bytes,
	monthly_tx_bytes=excluded.monthly_tx_bytes, tcp_connections=excluded.tcp_connections,
	udp_connections=excluded.udp_connections, processes=excluded.processes,
	temperature=excluded.temperature, uptime_seconds=excluded.uptime_seconds`
	_, err := s.db.ExecContext(ctx, s.q(query), bucketMS, bucketMS, millis(since))
	if err != nil {
		return fmt.Errorf("roll up %s to %s: %w", source, target, err)
	}
	return nil
}

func (s *SQLStore) ApplyRetention(ctx context.Context, settings model.SiteSettings, now time.Time) error {
	cutoffs := map[string]time.Time{
		"metrics_raw": now.Add(-time.Duration(settings.RawRetentionHours) * time.Hour),
		"metrics_5m":  now.Add(-time.Duration(settings.FiveMinuteRetentionHours) * time.Hour),
		"metrics_1h":  now.Add(-time.Duration(settings.HourlyRetentionHours) * time.Hour),
	}
	tables := make([]string, 0, len(cutoffs))
	for table := range cutoffs {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		if _, err := s.db.ExecContext(ctx, s.q(`DELETE FROM `+table+` WHERE ts < ?`), millis(cutoffs[table])); err != nil {
			return fmt.Errorf("apply retention to %s: %w", table, err)
		}
	}
	probeCutoff := now.Add(-time.Duration(settings.FiveMinuteRetentionHours) * time.Hour)
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM probe_results WHERE ts < ?`), millis(probeCutoff))
	return err
}

// Verify the scanner behavior eagerly in tests without exposing database/sql.
var _ = errors.Is
var _ = sql.ErrNoRows
