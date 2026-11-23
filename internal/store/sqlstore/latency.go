package sqlstore

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
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
	type latencyTaskRow struct {
		id           int64
		targetNodeID string
	}
	taskRows, err := s.readDB.QueryContext(ctx, `SELECT id, target_node_id FROM probe_tasks WHERE purpose = 'latency' ORDER BY target_node_id, id`)
	if err != nil {
		return nil, err
	}
	tasks := make([]latencyTaskRow, 0)
	for taskRows.Next() {
		var task latencyTaskRow
		if err := taskRows.Scan(&task.id, &task.targetNodeID); err != nil {
			taskRows.Close()
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := taskRows.Err(); err != nil {
		taskRows.Close()
		return nil, err
	}
	if err := taskRows.Close(); err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return []model.LatencyResult{}, nil
	}

	nodeRows, err := s.readDB.QueryContext(ctx, s.q(`SELECT id FROM nodes WHERE role = ? OR latency_enabled = ? ORDER BY id`), model.NodeRoleProbe, s.boolArg(true))
	if err != nil {
		return nil, err
	}
	probeNodeIDs := make([]string, 0)
	for nodeRows.Next() {
		var id string
		if err := nodeRows.Scan(&id); err != nil {
			nodeRows.Close()
			return nil, err
		}
		probeNodeIDs = append(probeNodeIDs, id)
	}
	if err := nodeRows.Err(); err != nil {
		nodeRows.Close()
		return nil, err
	}
	if err := nodeRows.Close(); err != nil {
		return nil, err
	}
	if len(probeNodeIDs) == 0 {
		return []model.LatencyResult{}, nil
	}

	conn, err := s.readDB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	pointQuery := s.q(`SELECT node_id, task_id, ts, collected_at, success, latency_ms, loss_percent, error FROM probe_results WHERE node_id = ? AND task_id = ? AND ts >= ? ORDER BY ts DESC LIMIT 1`)
	stmt, err := conn.PrepareContext(ctx, pointQuery)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	sinceMS := millis(since)
	results := make([]model.LatencyResult, 0, len(tasks)*len(probeNodeIDs))
	for _, task := range tasks {
		for _, probeNodeID := range probeNodeIDs {
			var result model.LatencyResult
			var receivedAt, collectedAt int64
			err := stmt.QueryRowContext(ctx, probeNodeID, task.id, sinceMS).Scan(
				&result.ProbeNodeID, &result.TaskID, &receivedAt, &collectedAt,
				&result.Success, &result.LatencyMS, &result.LossPercent, &result.Error,
			)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			result.TargetNodeID = task.targetNodeID
			result.ReceivedAt = timeFromMillis(receivedAt)
			result.CollectedAt = timeFromMillis(collectedAt)
			results = append(results, result)
		}
	}
	slices.SortFunc(results, func(a, b model.LatencyResult) int {
		if c := cmp.Compare(a.TargetNodeID, b.TargetNodeID); c != 0 {
			return c
		}
		if c := cmp.Compare(a.ProbeNodeID, b.ProbeNodeID); c != 0 {
			return c
		}
		return cmp.Compare(a.TaskID, b.TaskID)
	})
	return results, nil
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
	rows, err := s.readDB.QueryContext(ctx, s.q(query), args...)
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
	err := s.readDB.QueryRowContext(ctx, s.q(query), args...).Scan(
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
