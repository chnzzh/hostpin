package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/google/uuid"
)

func scanAlertRule(row rowScanner) (model.AlertRule, error) {
	var rule model.AlertRule
	var scope string
	var createdAt, updatedAt int64
	err := row.Scan(&rule.ID, &rule.Name, &rule.Metric, &rule.Operator, &rule.Threshold,
		&rule.RecoveryThreshold, &rule.DurationSeconds, &rule.CooldownSeconds,
		&rule.Severity, &scope, &rule.Enabled, &createdAt, &updatedAt)
	if err != nil {
		return model.AlertRule{}, mapSQLError(err)
	}
	_ = json.Unmarshal([]byte(scope), &rule.Scope)
	rule.CreatedAt, rule.UpdatedAt = timeFromMillis(createdAt), timeFromMillis(updatedAt)
	return rule, nil
}

func (s *SQLStore) ListAlertRules(ctx context.Context) ([]model.AlertRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, metric, operator, threshold, recovery_threshold, duration_seconds, cooldown_seconds, severity, scope_json, enabled, created_at, updated_at FROM alert_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := make([]model.AlertRule, 0)
	for rows.Next() {
		rule, err := scanAlertRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (s *SQLStore) SaveAlertRule(ctx context.Context, rule model.AlertRule) (model.AlertRule, error) {
	now := time.Now().UTC()
	if rule.ID == 0 {
		query := `INSERT INTO alert_rules(name, metric, operator, threshold, recovery_threshold, duration_seconds, cooldown_seconds, severity, scope_json, enabled, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`
		err := s.db.QueryRowContext(ctx, s.q(query), rule.Name, rule.Metric, rule.Operator,
			rule.Threshold, rule.RecoveryThreshold, rule.DurationSeconds, rule.CooldownSeconds,
			rule.Severity, encodeJSON(rule.Scope), s.boolArg(rule.Enabled), millis(now), millis(now)).Scan(&rule.ID)
		if err != nil {
			return model.AlertRule{}, err
		}
		rule.CreatedAt, rule.UpdatedAt = now, now
		return rule, nil
	}
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE alert_rules SET name=?, metric=?, operator=?, threshold=?, recovery_threshold=?, duration_seconds=?, cooldown_seconds=?, severity=?, scope_json=?, enabled=?, updated_at=? WHERE id=?`),
		rule.Name, rule.Metric, rule.Operator, rule.Threshold, rule.RecoveryThreshold,
		rule.DurationSeconds, rule.CooldownSeconds, rule.Severity, encodeJSON(rule.Scope),
		s.boolArg(rule.Enabled), millis(now), rule.ID)
	if err != nil {
		return model.AlertRule{}, err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return model.AlertRule{}, store.ErrNotFound
	}
	rule.UpdatedAt = now
	return rule, nil
}

func (s *SQLStore) DeleteAlertRule(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, s.q(`DELETE FROM alert_rules WHERE id = ?`), id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *SQLStore) SaveAlertEvent(ctx context.Context, event model.AlertEvent, ruleID *int64) error {
	var resolved any
	if event.Status == model.AlertResolved {
		resolved = millis(event.OccurredAt)
	}
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO alert_events(id, rule_id, node_id, node_json, event_type, status, severity, metric, value, threshold, message, link, occurred_at, resolved_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		event.ID, ruleID, event.Node.ID, encodeJSON(event.Node), event.Type, event.Status, event.Severity,
		event.Metric, event.Value, event.Threshold, event.Message, event.Link, millis(event.OccurredAt), resolved)
	return err
}

func (s *SQLStore) ListAlertEvents(ctx context.Context, limit int) ([]model.AlertEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id, node_id, node_json, event_type, status, severity, metric, value, threshold, message, link, occurred_at FROM alert_events ORDER BY occurred_at DESC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type rawEvent struct {
		event    model.AlertEvent
		nodeID   string
		nodeJSON string
	}
	raw := make([]rawEvent, 0)
	for rows.Next() {
		var item rawEvent
		var occurredAt int64
		if err := rows.Scan(&item.event.ID, &item.nodeID, &item.nodeJSON, &item.event.Type, &item.event.Status,
			&item.event.Severity, &item.event.Metric, &item.event.Value, &item.event.Threshold,
			&item.event.Message, &item.event.Link, &occurredAt); err != nil {
			return nil, err
		}
		item.event.OccurredAt = timeFromMillis(occurredAt)
		raw = append(raw, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	events := make([]model.AlertEvent, 0, len(raw))
	for _, item := range raw {
		decodeJSON(item.nodeJSON, &item.event.Node)
		if item.event.Node.ID == "" {
			if node, err := s.GetNode(ctx, item.nodeID); err == nil {
				item.event.Node = node.Public()
			} else {
				item.event.Node = model.PublicNode{ID: item.nodeID, Name: item.nodeID}
			}
		}
		events = append(events, item.event)
	}
	return events, nil
}

func scanNotificationChannel(row rowScanner) (model.NotificationChannel, error) {
	var channel model.NotificationChannel
	var createdAt, updatedAt int64
	err := row.Scan(&channel.ID, &channel.Name, &channel.Type, &channel.EncryptedConfig,
		&channel.Enabled, &createdAt, &updatedAt)
	if err != nil {
		return model.NotificationChannel{}, mapSQLError(err)
	}
	channel.CreatedAt, channel.UpdatedAt = timeFromMillis(createdAt), timeFromMillis(updatedAt)
	return channel, nil
}

func (s *SQLStore) ListNotificationChannels(ctx context.Context) ([]model.NotificationChannel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, config_enc, enabled, created_at, updated_at FROM notification_channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	channels := make([]model.NotificationChannel, 0)
	for rows.Next() {
		channel, err := scanNotificationChannel(rows)
		if err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	return channels, rows.Err()
}

func (s *SQLStore) SaveNotificationChannel(ctx context.Context, channel model.NotificationChannel, encryptedConfig string) (model.NotificationChannel, error) {
	now := time.Now().UTC()
	if channel.ID == 0 {
		query := `INSERT INTO notification_channels(name, type, config_enc, enabled, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?) RETURNING id`
		err := s.db.QueryRowContext(ctx, s.q(query), channel.Name, channel.Type, encryptedConfig,
			s.boolArg(channel.Enabled), millis(now), millis(now)).Scan(&channel.ID)
		if err != nil {
			return model.NotificationChannel{}, err
		}
		channel.CreatedAt, channel.UpdatedAt = now, now
		return channel, nil
	}
	if encryptedConfig == "" {
		result, err := s.db.ExecContext(ctx, s.q(`UPDATE notification_channels SET name=?, type=?, enabled=?, updated_at=? WHERE id=?`),
			channel.Name, channel.Type, s.boolArg(channel.Enabled), millis(now), channel.ID)
		if err != nil {
			return model.NotificationChannel{}, err
		}
		if count, _ := result.RowsAffected(); count == 0 {
			return model.NotificationChannel{}, store.ErrNotFound
		}
	} else {
		result, err := s.db.ExecContext(ctx, s.q(`UPDATE notification_channels SET name=?, type=?, config_enc=?, enabled=?, updated_at=? WHERE id=?`),
			channel.Name, channel.Type, encryptedConfig, s.boolArg(channel.Enabled), millis(now), channel.ID)
		if err != nil {
			return model.NotificationChannel{}, err
		}
		if count, _ := result.RowsAffected(); count == 0 {
			return model.NotificationChannel{}, store.ErrNotFound
		}
	}
	channel.UpdatedAt = now
	return channel, nil
}

func (s *SQLStore) DeleteNotificationChannel(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, s.q(`DELETE FROM notification_channels WHERE id = ?`), id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *SQLStore) EnqueueNotificationDeliveries(ctx context.Context, eventID string, now time.Time) error {
	channels, err := s.ListNotificationChannels(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, channel := range channels {
		if !channel.Enabled {
			continue
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, s.q(`INSERT INTO notification_deliveries(id, event_id, channel_id, status, attempts, next_attempt_at, last_error, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			id.String(), eventID, channel.ID, "pending", 0, millis(now), "", millis(now), millis(now))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLStore) DueNotificationDeliveries(ctx context.Context, now time.Time, limit int) ([]model.NotificationDelivery, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	query := `SELECT d.id, d.status, d.attempts, d.next_attempt_at, d.last_error, d.created_at, d.updated_at,
		e.id, e.node_id, e.node_json, e.event_type, e.status, e.severity, e.metric, e.value, e.threshold, e.message, e.link, e.occurred_at,
		c.id, c.name, c.type, c.config_enc, c.enabled, c.created_at, c.updated_at
		FROM notification_deliveries d
		JOIN alert_events e ON e.id=d.event_id
		JOIN notification_channels c ON c.id=d.channel_id
		WHERE d.status IN ('pending','retry') AND d.next_attempt_at <= ? AND c.enabled = ` + s.enabledLiteral() + `
		ORDER BY d.next_attempt_at LIMIT ?`
	rows, err := s.db.QueryContext(ctx, s.q(query), millis(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type rawDelivery struct {
		delivery model.NotificationDelivery
		nodeID   string
		nodeJSON string
	}
	raw := make([]rawDelivery, 0)
	for rows.Next() {
		var item rawDelivery
		var nextAt, deliveryCreated, deliveryUpdated, eventAt, channelCreated, channelUpdated int64
		if err := rows.Scan(&item.delivery.ID, &item.delivery.Status, &item.delivery.Attempts, &nextAt,
			&item.delivery.LastError, &deliveryCreated, &deliveryUpdated, &item.delivery.Event.ID,
			&item.nodeID, &item.nodeJSON, &item.delivery.Event.Type, &item.delivery.Event.Status, &item.delivery.Event.Severity,
			&item.delivery.Event.Metric, &item.delivery.Event.Value, &item.delivery.Event.Threshold,
			&item.delivery.Event.Message, &item.delivery.Event.Link, &eventAt, &item.delivery.Channel.ID, &item.delivery.Channel.Name,
			&item.delivery.Channel.Type, &item.delivery.Channel.EncryptedConfig, &item.delivery.Channel.Enabled,
			&channelCreated, &channelUpdated); err != nil {
			return nil, err
		}
		item.delivery.NextAttemptAt = timeFromMillis(nextAt)
		item.delivery.CreatedAt, item.delivery.UpdatedAt = timeFromMillis(deliveryCreated), timeFromMillis(deliveryUpdated)
		item.delivery.Event.OccurredAt = timeFromMillis(eventAt)
		item.delivery.Channel.CreatedAt, item.delivery.Channel.UpdatedAt = timeFromMillis(channelCreated), timeFromMillis(channelUpdated)
		raw = append(raw, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	deliveries := make([]model.NotificationDelivery, 0, len(raw))
	for _, item := range raw {
		decodeJSON(item.nodeJSON, &item.delivery.Event.Node)
		if item.delivery.Event.Node.ID == "" {
			if node, err := s.GetNode(ctx, item.nodeID); err == nil {
				item.delivery.Event.Node = node.Public()
			} else {
				item.delivery.Event.Node = model.PublicNode{ID: item.nodeID, Name: item.nodeID}
			}
		}
		deliveries = append(deliveries, item.delivery)
	}
	return deliveries, nil
}

func (s *SQLStore) UpdateNotificationDelivery(ctx context.Context, id, status string, attempts int, next time.Time, lastError string) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE notification_deliveries SET status=?, attempts=?, next_attempt_at=?, last_error=?, updated_at=? WHERE id=?`),
		status, attempts, millis(next), lastError, millis(time.Now().UTC()), id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *SQLStore) ListAudit(ctx context.Context, limit int) ([]model.AuditEntry, error) {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id, actor, action, target, detail, occurred_at FROM audit_log ORDER BY occurred_at DESC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]model.AuditEntry, 0)
	for rows.Next() {
		var entry model.AuditEntry
		var occurredAt int64
		if err := rows.Scan(&entry.ID, &entry.Actor, &entry.Action, &entry.Target, &entry.Detail, &occurredAt); err != nil {
			return nil, err
		}
		entry.OccurredAt = timeFromMillis(occurredAt)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

var _ = sql.ErrNoRows
