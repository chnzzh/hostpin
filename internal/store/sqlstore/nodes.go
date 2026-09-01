package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
)

const nodeColumns = `id, role, latency_enabled, name, hostname, node_group, region, country_code, latitude, longitude, location_manual, tags, public_remark, private_remark, hidden, weight, price, currency, billing_cycle_days, expires_at, auto_renewal, traffic_limit, traffic_limit_type, traffic_reset_day, traffic_rx_correction, traffic_tx_correction, traffic_correction_period_start, traffic_correction_updated_at, agent_version, os, arch, cpu_name, cpu_cores, virtualization, kernel_version, ipv4, ipv6, source_ip, created_at, updated_at, last_seen_at`

type rowScanner interface {
	Scan(...any) error
}

func scanNode(row rowScanner) (model.Node, error) {
	var node model.Node
	var tags string
	var latitude, longitude sql.NullFloat64
	var expiresAt, lastSeenAt, correctionPeriodStart, correctionUpdatedAt sql.NullInt64
	var createdAt, updatedAt int64
	err := row.Scan(
		&node.ID, &node.Role, &node.LatencyEnabled, &node.Name, &node.Hostname, &node.Group, &node.Region,
		&node.CountryCode, &latitude, &longitude, &node.LocationManual, &tags, &node.PublicRemark,
		&node.PrivateRemark, &node.Hidden, &node.Weight, &node.Price, &node.Currency,
		&node.BillingCycleDays, &expiresAt, &node.AutoRenewal, &node.TrafficLimit,
		&node.TrafficLimitType, &node.TrafficResetDay, &node.TrafficRXCorrection,
		&node.TrafficTXCorrection, &correctionPeriodStart, &correctionUpdatedAt, &node.AgentVersion,
		&node.OS, &node.Arch, &node.CPUName, &node.CPUCores, &node.Virtualization,
		&node.KernelVersion, &node.IPv4, &node.IPv6, &node.SourceIP, &createdAt, &updatedAt, &lastSeenAt,
	)
	if err != nil {
		return model.Node{}, mapSQLError(err)
	}
	if latitude.Valid {
		node.Latitude = &latitude.Float64
	}
	if longitude.Valid {
		node.Longitude = &longitude.Float64
	}
	if expiresAt.Valid {
		value := timeFromMillis(expiresAt.Int64)
		node.ExpiresAt = &value
	}
	if lastSeenAt.Valid {
		value := timeFromMillis(lastSeenAt.Int64)
		node.LastSeenAt = &value
	}
	if correctionPeriodStart.Valid {
		value := timeFromMillis(correctionPeriodStart.Int64)
		node.TrafficCorrectionPeriodStart = &value
	}
	if correctionUpdatedAt.Valid {
		value := timeFromMillis(correctionUpdatedAt.Int64)
		node.TrafficCorrectionUpdatedAt = &value
	}
	decodeJSON(tags, &node.Tags)
	if node.Tags == nil {
		node.Tags = []string{}
	}
	node.Role = model.NormalizeNodeRole(node.Role)
	node.CreatedAt = timeFromMillis(createdAt)
	node.UpdatedAt = timeFromMillis(updatedAt)
	return node, nil
}

func (s *SQLStore) EnrollNode(ctx context.Context, params store.EnrollParams) (store.EnrollmentRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return store.EnrollmentRecord{}, err
	}
	defer tx.Rollback()

	var existingNodeID, existingHash string
	err = tx.QueryRowContext(ctx, s.q(`SELECT node_id, token_hash FROM agent_credentials WHERE install_id = ?`), params.Request.InstallID).Scan(&existingNodeID, &existingHash)
	if err == nil {
		if !constantTimeEqual(existingHash, params.TokenHash) {
			return store.EnrollmentRecord{}, store.ErrInstallConflict
		}
		node, err := scanNode(tx.QueryRowContext(ctx, s.q(`SELECT `+nodeColumns+` FROM nodes WHERE id = ?`), existingNodeID))
		if err != nil {
			return store.EnrollmentRecord{}, err
		}
		if node.Role != model.NormalizeNodeRole(params.Request.Role) {
			return store.EnrollmentRecord{}, store.ErrInstallConflict
		}
		cfg, err := scanAgentConfig(tx.QueryRowContext(ctx, s.q(`SELECT config_json FROM agent_configs WHERE node_id = ?`), existingNodeID))
		if err != nil {
			return store.EnrollmentRecord{}, err
		}
		if err := tx.Commit(); err != nil {
			return store.EnrollmentRecord{}, err
		}
		return store.EnrollmentRecord{Node: node, Config: cfg, Created: false}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.EnrollmentRecord{}, err
	}
	if params.TemporaryPINID != "" {
		if err := claimTemporaryEnrollmentPIN(ctx, tx, s.q, params.TemporaryPINID, params.Request.InstallID, params.TokenID, params.Now); err != nil {
			return store.EnrollmentRecord{}, err
		}
	}

	metadata := params.Request.Metadata
	identity := params.Request.Identity
	if metadata.Currency == "" {
		metadata.Currency = "$"
	}
	if metadata.BillingCycleDays <= 0 {
		metadata.BillingCycleDays = 30
	}
	if metadata.TrafficLimitType == "" {
		metadata.TrafficLimitType = "sum"
	}
	if metadata.TrafficResetDay <= 0 || metadata.TrafficResetDay > 31 {
		metadata.TrafficResetDay = 1
	}
	name := strings.TrimSpace(metadata.Name)
	if name == "" {
		name = identity.Hostname
	}
	if name == "" {
		name = "Unnamed node"
	}
	role := model.NormalizeNodeRole(params.Request.Role)

	query := `INSERT INTO nodes(
			id, role, latency_enabled, name, hostname, node_group, region, country_code, latitude, longitude,
		location_manual, tags, public_remark, private_remark, hidden, weight, price, currency,
		billing_cycle_days, expires_at, auto_renewal, traffic_limit,
		traffic_limit_type, traffic_reset_day, agent_version, os, arch, cpu_name,
		cpu_cores, virtualization, kernel_version, ipv4, ipv6, source_ip,
		created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = tx.ExecContext(ctx, s.q(query),
		params.NodeID, role, s.boolArg(role == model.NodeRoleProbe), name, identity.Hostname, metadata.Group, metadata.Region,
		strings.ToUpper(metadata.CountryCode), metadata.Latitude, metadata.Longitude,
		s.boolArg(params.LocationManual), encodeJSON(metadata.Tags), metadata.PublicRemark, metadata.PrivateRemark,
		s.boolArg(metadata.Hidden), 0, metadata.Price, metadata.Currency,
		metadata.BillingCycleDays, nullableMillis(metadata.ExpiresAt), s.boolArg(metadata.AutoRenewal),
		metadata.TrafficLimit, metadata.TrafficLimitType, metadata.TrafficResetDay,
		identity.Version, identity.OS, identity.Arch, identity.CPUName, identity.CPUCores,
		identity.Virtualization, identity.KernelVersion, identity.IPv4, identity.IPv6,
		params.SourceIP, millis(params.Now), millis(params.Now),
	)
	if err != nil {
		return store.EnrollmentRecord{}, fmt.Errorf("insert node: %w", err)
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO agent_credentials(node_id, install_id, token_id, token_hash, created_at, rotated_at) VALUES(?, ?, ?, ?, ?, ?)`),
		params.NodeID, params.Request.InstallID, params.TokenID, params.TokenHash, millis(params.Now), millis(params.Now))
	if err != nil {
		return store.EnrollmentRecord{}, fmt.Errorf("insert agent credential: %w", err)
	}
	cfg := params.Request.Config
	if cfg.CollectIntervalSeconds <= 0 {
		cfg = model.DefaultAgentConfig()
	}
	if cfg.PersistIntervalSeconds < cfg.CollectIntervalSeconds {
		cfg.PersistIntervalSeconds = 60
	}
	if cfg.ProbeConcurrency <= 0 {
		cfg.ProbeConcurrency = 4
	}
	if cfg.ConfigVersion <= 0 {
		cfg.ConfigVersion = 1
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO agent_configs(node_id, config_json, config_version, updated_at) VALUES(?, ?, ?, ?)`),
		params.NodeID, encodeJSON(cfg), cfg.ConfigVersion, millis(params.Now))
	if err != nil {
		return store.EnrollmentRecord{}, fmt.Errorf("insert agent config: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return store.EnrollmentRecord{}, err
	}
	node, err := s.GetNode(ctx, params.NodeID)
	if err != nil {
		return store.EnrollmentRecord{}, err
	}
	return store.EnrollmentRecord{Node: node, Config: cfg, Created: true}, nil
}

func (s *SQLStore) AuthenticateAgent(ctx context.Context, tokenID, tokenHash string) (model.Node, model.AgentConfig, error) {
	var nodeID, storedHash string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT node_id, token_hash FROM agent_credentials WHERE token_id = ?`), tokenID).Scan(&nodeID, &storedHash)
	if err != nil {
		return model.Node{}, model.AgentConfig{}, store.ErrUnauthorized
	}
	if !constantTimeEqual(storedHash, tokenHash) {
		return model.Node{}, model.AgentConfig{}, store.ErrUnauthorized
	}
	node, err := s.GetNode(ctx, nodeID)
	if err != nil {
		return model.Node{}, model.AgentConfig{}, store.ErrUnauthorized
	}
	cfg, err := s.AgentConfig(ctx, nodeID)
	if err != nil {
		return model.Node{}, model.AgentConfig{}, err
	}
	return node, cfg, nil
}

func (s *SQLStore) ListNodes(ctx context.Context, includeHidden bool) ([]model.Node, error) {
	return s.listNodesByRole(ctx, model.NodeRoleMonitor, includeHidden)
}

func (s *SQLStore) ListLatencyNodes(ctx context.Context, includeHidden bool) ([]model.Node, error) {
	query := `SELECT ` + nodeColumns + ` FROM nodes WHERE (role = ? OR latency_enabled = ?)`
	args := []any{model.NodeRoleProbe, s.boolArg(true)}
	if !includeHidden {
		query += ` AND hidden = ?`
		args = append(args, s.boolArg(false))
	}
	query += ` ORDER BY weight DESC, node_group, name`
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := make([]model.Node, 0)
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

func (s *SQLStore) listNodesByRole(ctx context.Context, role model.NodeRole, includeHidden bool) ([]model.Node, error) {
	query := `SELECT ` + nodeColumns + ` FROM nodes WHERE role = ?`
	args := []any{role}
	if !includeHidden {
		query += ` AND hidden = ?`
		args = append(args, s.boolArg(false))
	}
	query += ` ORDER BY weight DESC, node_group, name`
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := make([]model.Node, 0)
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

func (s *SQLStore) GetNode(ctx context.Context, id string) (model.Node, error) {
	return scanNode(s.db.QueryRowContext(ctx, s.q(`SELECT `+nodeColumns+` FROM nodes WHERE id = ?`), id))
}

func (s *SQLStore) UpdateNode(ctx context.Context, node model.Node) error {
	query := `UPDATE nodes SET name=?, node_group=?, region=?, country_code=?, latitude=?, longitude=?, location_manual=?, tags=?, public_remark=?, private_remark=?, hidden=?, weight=?, price=?, currency=?, billing_cycle_days=?, expires_at=?, auto_renewal=?, traffic_limit=?, traffic_limit_type=?, traffic_reset_day=?, latency_enabled=?, updated_at=? WHERE id=?`
	latencyEnabled := node.CanMeasureLatency()
	result, err := s.db.ExecContext(ctx, s.q(query),
		node.Name, node.Group, node.Region, strings.ToUpper(node.CountryCode), node.Latitude,
		node.Longitude, s.boolArg(node.LocationManual), encodeJSON(node.Tags), node.PublicRemark, node.PrivateRemark,
		s.boolArg(node.Hidden), node.Weight, node.Price, node.Currency, node.BillingCycleDays,
		nullableMillis(node.ExpiresAt), s.boolArg(node.AutoRenewal), node.TrafficLimit,
		node.TrafficLimitType, node.TrafficResetDay, s.boolArg(latencyEnabled), millis(time.Now()), node.ID,
	)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *SQLStore) UpdateTrafficCorrection(ctx context.Context, nodeID string, rxCorrection, txCorrection int64, periodStart *time.Time, updatedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE nodes SET traffic_rx_correction=?, traffic_tx_correction=?, traffic_correction_period_start=?, traffic_correction_updated_at=?, updated_at=? WHERE id=?`),
		rxCorrection, txCorrection, nullableMillis(periodStart), millis(updatedAt), millis(updatedAt), nodeID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *SQLStore) DeleteNode(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM probe_tasks WHERE purpose = 'latency' AND target_node_id = ?`), id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.q(`DELETE FROM nodes WHERE id = ?`), id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return store.ErrNotFound
	}
	return tx.Commit()
}

func (s *SQLStore) UpdateAgentSeen(ctx context.Context, nodeID string, identity model.AgentIdentity, seenAt time.Time, sourceIP string) error {
	query := `UPDATE nodes SET hostname=?, agent_version=?, os=?, arch=?, cpu_name=?, cpu_cores=?, virtualization=?, kernel_version=?, ipv4=?, ipv6=?, source_ip=?, last_seen_at=?, updated_at=? WHERE id=?`
	result, err := s.db.ExecContext(ctx, s.q(query), identity.Hostname, identity.Version,
		identity.OS, identity.Arch, identity.CPUName, identity.CPUCores,
		identity.Virtualization, identity.KernelVersion, identity.IPv4, identity.IPv6,
		sourceIP, millis(seenAt), millis(seenAt), nodeID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func scanAgentConfig(row rowScanner) (model.AgentConfig, error) {
	var raw string
	if err := row.Scan(&raw); err != nil {
		return model.AgentConfig{}, mapSQLError(err)
	}
	cfg := model.DefaultAgentConfig()
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return model.AgentConfig{}, fmt.Errorf("decode agent config: %w", err)
	}
	return cfg, nil
}

func (s *SQLStore) AgentConfig(ctx context.Context, nodeID string) (model.AgentConfig, error) {
	return scanAgentConfig(s.db.QueryRowContext(ctx, s.q(`SELECT config_json FROM agent_configs WHERE node_id = ?`), nodeID))
}

func (s *SQLStore) SaveAgentConfig(ctx context.Context, nodeID string, cfg model.AgentConfig) error {
	for attempt := 0; attempt < 5; attempt++ {
		var current int64
		if err := s.db.QueryRowContext(ctx, s.q(`SELECT config_version FROM agent_configs WHERE node_id = ?`), nodeID).Scan(&current); err != nil {
			return mapSQLError(err)
		}
		cfg.ConfigVersion = current + 1
		result, err := s.db.ExecContext(ctx, s.q(`UPDATE agent_configs SET config_json=?, config_version=?, updated_at=? WHERE node_id=? AND config_version=?`),
			encodeJSON(cfg), cfg.ConfigVersion, millis(time.Now()), nodeID, current)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows == 1 {
			return nil
		}
	}
	return errors.New("Agent configuration changed concurrently; retry the update")
}

func (s *SQLStore) AppendAudit(ctx context.Context, actor, action, target, detail string, occurredAt time.Time) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO audit_log(actor, action, target, detail, occurred_at) VALUES(?, ?, ?, ?, ?)`),
		actor, action, target, detail, millis(occurredAt))
	return err
}
