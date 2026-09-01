package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
)

func scanProbeTask(row rowScanner) (model.ProbeTask, error) {
	var task model.ProbeTask
	var rawNodeIDs string
	var createdAt, updatedAt int64
	err := row.Scan(&task.ID, &task.Name, &task.Type, &task.Target, &task.IntervalSeconds,
		&task.TimeoutSeconds, &task.ExpectedStatus, &task.ExpectedValue, &rawNodeIDs,
		&task.Purpose, &task.RunOn, &task.TargetNodeID, &task.Public, &task.Samples,
		&task.Enabled, &createdAt, &updatedAt)
	if err != nil {
		return model.ProbeTask{}, mapSQLError(err)
	}
	_ = json.Unmarshal([]byte(rawNodeIDs), &task.NodeIDs)
	if task.NodeIDs == nil {
		task.NodeIDs = []string{}
	}
	if task.Purpose == "" {
		task.Purpose = "custom"
	}
	task.RunOn = model.NormalizeNodeRole(task.RunOn)
	if task.Samples <= 0 {
		task.Samples = 1
	}
	task.CreatedAt = timeFromMillis(createdAt)
	task.UpdatedAt = timeFromMillis(updatedAt)
	return task, nil
}

func (s *SQLStore) ListProbeTasks(ctx context.Context, nodeID string) ([]model.ProbeTask, error) {
	return s.listProbeTasks(ctx, nodeID, true)
}

func (s *SQLStore) ListAllProbeTasks(ctx context.Context) ([]model.ProbeTask, error) {
	return s.listProbeTasks(ctx, "", false)
}

func (s *SQLStore) listProbeTasks(ctx context.Context, nodeID string, enabledOnly bool) ([]model.ProbeTask, error) {
	var node model.Node
	if nodeID != "" {
		var err error
		node, err = s.GetNode(ctx, nodeID)
		if err != nil {
			return nil, err
		}
	}
	query := `SELECT id, name, type, target, interval_seconds, timeout_seconds, expected_status, expected_value, node_ids, purpose, run_on, target_node_id, public, samples, enabled, created_at, updated_at FROM probe_tasks`
	if enabledOnly {
		query += ` WHERE enabled = ` + s.enabledLiteral()
	}
	query += ` ORDER BY id`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := make([]model.ProbeTask, 0)
	for rows.Next() {
		task, err := scanProbeTask(rows)
		if err != nil {
			return nil, err
		}
		roleMatches := nodeID == "" || task.RunOn == node.Role
		if nodeID != "" && task.Purpose == model.ProbePurposeLatency && node.CanMeasureLatency() {
			roleMatches = true
		}
		if roleMatches && (nodeID == "" || len(task.NodeIDs) == 0 || slices.Contains(task.NodeIDs, nodeID)) {
			tasks = append(tasks, task)
		}
	}
	return tasks, rows.Err()
}

func (s *SQLStore) enabledLiteral() string {
	if s.driver == "postgres" {
		return "TRUE"
	}
	return "1"
}

func (s *SQLStore) SaveProbeTask(ctx context.Context, task model.ProbeTask) (model.ProbeTask, error) {
	now := time.Now().UTC()
	if task.IntervalSeconds < 5 {
		task.IntervalSeconds = 60
	}
	if task.TimeoutSeconds <= 0 || task.TimeoutSeconds > task.IntervalSeconds {
		task.TimeoutSeconds = 5
	}
	if task.Purpose == "" {
		task.Purpose = "custom"
	}
	task.RunOn = model.NormalizeNodeRole(task.RunOn)
	if task.Samples <= 0 {
		task.Samples = 1
	}
	if task.ID == 0 {
		query := `INSERT INTO probe_tasks(name, type, target, interval_seconds, timeout_seconds, expected_status, expected_value, node_ids, purpose, run_on, target_node_id, public, samples, enabled, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`
		err := s.db.QueryRowContext(ctx, s.q(query), task.Name, task.Type, task.Target,
			task.IntervalSeconds, task.TimeoutSeconds, task.ExpectedStatus, task.ExpectedValue,
			encodeJSON(task.NodeIDs), task.Purpose, task.RunOn, task.TargetNodeID,
			s.boolArg(task.Public), task.Samples, s.boolArg(task.Enabled), millis(now), millis(now)).Scan(&task.ID)
		if err != nil {
			return model.ProbeTask{}, err
		}
		task.CreatedAt = now
		task.UpdatedAt = now
		return task, nil
	}
	query := `UPDATE probe_tasks SET name=?, type=?, target=?, interval_seconds=?, timeout_seconds=?, expected_status=?, expected_value=?, node_ids=?, purpose=?, run_on=?, target_node_id=?, public=?, samples=?, enabled=?, updated_at=? WHERE id=?`
	result, err := s.db.ExecContext(ctx, s.q(query), task.Name, task.Type, task.Target,
		task.IntervalSeconds, task.TimeoutSeconds, task.ExpectedStatus, task.ExpectedValue,
		encodeJSON(task.NodeIDs), task.Purpose, task.RunOn, task.TargetNodeID,
		s.boolArg(task.Public), task.Samples, s.boolArg(task.Enabled), millis(now), task.ID)
	if err != nil {
		return model.ProbeTask{}, err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return model.ProbeTask{}, store.ErrNotFound
	}
	task.UpdatedAt = now
	return task, nil
}

func (s *SQLStore) DeleteProbeTask(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, s.q(`DELETE FROM probe_tasks WHERE id = ?`), id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *SQLStore) SaveProbeResult(ctx context.Context, result model.ProbeResult) error {
	if result.ReceivedAt.IsZero() {
		result.ReceivedAt = time.Now().UTC()
	}
	if result.CollectedAt.IsZero() {
		result.CollectedAt = result.ReceivedAt
	}
	query := `INSERT INTO probe_results(node_id, task_id, ts, collected_at, success, latency_ms, loss_percent, status_code, value, error) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(node_id, task_id, ts) DO UPDATE SET collected_at=excluded.collected_at, success=excluded.success, latency_ms=excluded.latency_ms, loss_percent=excluded.loss_percent, status_code=excluded.status_code, value=excluded.value, error=excluded.error`
	_, err := s.db.ExecContext(ctx, s.q(query), result.NodeID, result.TaskID,
		millis(result.ReceivedAt), millis(result.CollectedAt), s.boolArg(result.Success),
		result.LatencyMS, result.LossPercent, result.StatusCode, result.Value, result.Error)
	return err
}

func (s *SQLStore) ProbeHistory(ctx context.Context, nodeID string, taskID int64, start, end time.Time, limit int) ([]model.ProbeResult, error) {
	if limit <= 0 || limit > 20000 {
		limit = 4000
	}
	query := `SELECT node_id, task_id, ts, collected_at, success, latency_ms, loss_percent, status_code, value, error FROM probe_results WHERE ts >= ? AND ts <= ?`
	args := []any{millis(start), millis(end)}
	if nodeID != "" {
		query += ` AND node_id = ?`
		args = append(args, nodeID)
	}
	if taskID > 0 {
		query += ` AND task_id = ?`
		args = append(args, taskID)
	}
	query += ` ORDER BY ts DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]model.ProbeResult, 0)
	for rows.Next() {
		var result model.ProbeResult
		var ts, collectedAt int64
		if err := rows.Scan(&result.NodeID, &result.TaskID, &ts, &collectedAt,
			&result.Success, &result.LatencyMS, &result.LossPercent, &result.StatusCode, &result.Value, &result.Error); err != nil {
			return nil, err
		}
		result.ReceivedAt = timeFromMillis(ts)
		result.CollectedAt = timeFromMillis(collectedAt)
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(results)
	return results, nil
}

var _ = sql.ErrNoRows
var _ = errors.Is
var _ = fmt.Sprintf
