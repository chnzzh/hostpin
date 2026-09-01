package sqlstore

import (
	"context"
	"slices"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

const latencyResultColumns = `r.node_id, t.target_node_id, r.task_id, r.ts, r.collected_at, r.success, r.latency_ms, r.loss_percent, r.error`

func scanLatencyResult(row rowScanner) (model.LatencyResult, error) {
	var result model.LatencyResult
	var receivedAt, collectedAt int64
	if err := row.Scan(
		&result.ProbeNodeID, &result.TargetNodeID, &result.TaskID,
		&receivedAt, &collectedAt, &result.Success, &result.LatencyMS,
		&result.LossPercent, &result.Error,
	); err != nil {
		return model.LatencyResult{}, mapSQLError(err)
	}
	result.ReceivedAt = timeFromMillis(receivedAt)
	result.CollectedAt = timeFromMillis(collectedAt)
	return result, nil
}

func (s *SQLStore) LatestLatencyResults(ctx context.Context, since time.Time) ([]model.LatencyResult, error) {
	query := `SELECT ` + latencyResultColumns + `
		FROM probe_results r
		JOIN probe_tasks t ON t.id = r.task_id
		WHERE t.purpose = 'latency' AND r.ts >= ?
		AND r.ts = (SELECT MAX(r2.ts) FROM probe_results r2 WHERE r2.node_id = r.node_id AND r2.task_id = r.task_id)
		ORDER BY t.target_node_id, r.node_id`
	rows, err := s.db.QueryContext(ctx, s.q(query), millis(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]model.LatencyResult, 0)
	for rows.Next() {
		result, err := scanLatencyResult(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

func (s *SQLStore) LatencyHistory(ctx context.Context, probeNodeID, targetNodeID string, start, end time.Time, limit int) ([]model.LatencyResult, error) {
	if limit <= 0 || limit > 20000 {
		limit = 1000
	}
	query := `SELECT ` + latencyResultColumns + `
		FROM probe_results r
		JOIN probe_tasks t ON t.id = r.task_id
		WHERE t.purpose = 'latency' AND r.ts >= ? AND r.ts <= ?`
	args := []any{millis(start), millis(end)}
	if probeNodeID != "" {
		query += ` AND r.node_id = ?`
		args = append(args, probeNodeID)
	}
	if targetNodeID != "" {
		query += ` AND t.target_node_id = ?`
		args = append(args, targetNodeID)
	}
	query += ` ORDER BY r.ts DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]model.LatencyResult, 0)
	for rows.Next() {
		result, err := scanLatencyResult(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(results)
	return results, nil
}

func (s *SQLStore) LatencyWindowSummary(ctx context.Context, probeNodeID, targetNodeID string, start, end time.Time) (model.LatencyWindowSummary, error) {
	query := `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN r.success AND r.latency_ms >= 0 THEN 1 ELSE 0 END), 0),
		COALESCE(AVG(CASE WHEN r.success AND r.latency_ms >= 0 THEN r.latency_ms END), 0),
		COALESCE(AVG(r.loss_percent), 0)
		FROM probe_results r
		JOIN probe_tasks t ON t.id = r.task_id
		WHERE t.purpose = 'latency' AND r.ts >= ? AND r.ts <= ?`
	args := []any{millis(start), millis(end)}
	if probeNodeID != "" {
		query += ` AND r.node_id = ?`
		args = append(args, probeNodeID)
	}
	if targetNodeID != "" {
		query += ` AND t.target_node_id = ?`
		args = append(args, targetNodeID)
	}
	var summary model.LatencyWindowSummary
	err := s.db.QueryRowContext(ctx, s.q(query), args...).Scan(
		&summary.SampleCount,
		&summary.SuccessCount,
		&summary.AverageLatencyMS,
		&summary.AverageLossPercent,
	)
	if err != nil {
		return model.LatencyWindowSummary{}, mapSQLError(err)
	}
	if summary.SampleCount > 0 && summary.SuccessCount == 0 {
		summary.AverageLatencyMS = 999
	}
	return summary, nil
}
