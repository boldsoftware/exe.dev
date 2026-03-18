package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"exe.dev/exe-ops/apitype"
)

// Store provides data access operations.
type Store struct {
	db *sql.DB
}

// NewStore creates a new Store.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// UpsertServer creates or updates a server record and returns its ID.
func (s *Store) UpsertServer(ctx context.Context, r *apitype.Report, parts apitype.HostnameParts) (int64, error) {
	tagsJSON, err := json.Marshal(r.Tags)
	if err != nil {
		return 0, fmt.Errorf("marshal tags: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO servers (name, hostname, role, region, env, instance, tags, agent_version, arch, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			tags = excluded.tags,
			agent_version = excluded.agent_version,
			arch = excluded.arch,
			last_seen = excluded.last_seen
	`, r.Name, r.Name, parts.Role, parts.Region, parts.Env, parts.Instance, string(tagsJSON), r.AgentVersion, r.Arch, r.Timestamp.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("upsert server: %w", err)
	}

	// Always query for the ID — LastInsertId is unreliable after ON CONFLICT DO UPDATE.
	var id int64
	err = s.db.QueryRowContext(ctx, "SELECT id FROM servers WHERE name = ?", r.Name).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("get server id: %w", err)
	}

	return id, nil
}

// InsertReport inserts a new report row.
func (s *Store) InsertReport(ctx context.Context, serverID int64, r *apitype.Report) error {
	componentsJSON, err := json.Marshal(r.Components)
	if err != nil {
		return fmt.Errorf("marshal components: %w", err)
	}
	updatesJSON, err := json.Marshal(r.Updates)
	if err != nil {
		return fmt.Errorf("marshal updates: %w", err)
	}
	failedUnitsJSON, err := json.Marshal(r.FailedUnits)
	if err != nil {
		return fmt.Errorf("marshal failed_units: %w", err)
	}

	var zfsPoolsSQL *string
	if len(r.ZFSPools) > 0 {
		b, err := json.Marshal(r.ZFSPools)
		if err != nil {
			return fmt.Errorf("marshal zfs_pools: %w", err)
		}
		s := string(b)
		zfsPoolsSQL = &s
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO reports (
			server_id, timestamp, cpu_percent,
			mem_total, mem_used, mem_free, mem_swap, mem_swap_total,
			disk_total, disk_used, disk_free,
			net_send, net_recv,
			zfs_used, zfs_free,
			backup_zfs_used, backup_zfs_free,
			uptime_secs,
			load_avg_1, load_avg_5, load_avg_15,
			zfs_pool_health, zfs_arc_size, zfs_arc_hit_rate,
			net_rx_errors, net_rx_dropped, net_tx_errors, net_tx_dropped,
			conntrack_count, conntrack_max,
			fd_allocated, fd_max,
			components, updates, failed_units,
			zfs_pools
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		serverID, r.Timestamp.UTC().Format(time.RFC3339), r.CPU,
		r.MemTotal, r.MemUsed, r.MemFree, r.MemSwap, r.MemSwapTotal,
		r.DiskTotal, r.DiskUsed, r.DiskFree,
		r.NetSend, r.NetRecv,
		r.ZFSUsed, r.ZFSFree,
		r.BackupZFSUsed, r.BackupZFSFree,
		r.UptimeSecs,
		r.LoadAvg1, r.LoadAvg5, r.LoadAvg15,
		r.ZFSPoolHealth, r.ZFSArcSize, r.ZFSArcHitRate,
		r.NetRxErrors, r.NetRxDropped, r.NetTxErrors, r.NetTxDropped,
		r.ConntrackCount, r.ConntrackMax,
		r.FDAllocated, r.FDMax,
		string(componentsJSON), string(updatesJSON), string(failedUnitsJSON),
		zfsPoolsSQL,
	)
	if err != nil {
		return fmt.Errorf("insert report: %w", err)
	}
	return nil
}

// ListServers returns all servers with their latest metrics.
func (s *Store) ListServers(ctx context.Context) ([]apitype.ServerSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			s.name, s.hostname, s.role, s.region, s.env, s.instance, s.tags, s.last_seen,
			s.agent_version, s.arch, s.upgrade_pending,
			COALESCE(r.cpu_percent, 0), COALESCE(r.mem_total, 0), COALESCE(r.mem_used, 0),
			COALESCE(r.mem_swap, 0), COALESCE(r.mem_swap_total, 0),
			COALESCE(r.disk_total, 0), COALESCE(r.disk_used, 0),
			COALESCE(r.net_send, 0), COALESCE(r.net_recv, 0),
			COALESCE(r.components, '[]'),
			COALESCE(ec.instances, 0), COALESCE(ec.capacity, 0)
		FROM servers s
		LEFT JOIN reports r ON r.id = (
			SELECT id FROM reports WHERE server_id = s.id ORDER BY timestamp DESC LIMIT 1
		)
		LEFT JOIN exelet_capacity ec ON ec.server_name = s.name AND ec.timestamp = (
			SELECT MAX(timestamp) FROM exelet_capacity WHERE server_name = s.name
		)
		ORDER BY s.name
	`)
	if err != nil {
		return nil, fmt.Errorf("query servers: %w", err)
	}
	defer rows.Close()

	var servers []apitype.ServerSummary
	for rows.Next() {
		var ss apitype.ServerSummary
		var tagsJSON, componentsJSON string
		var upgradePending int
		err := rows.Scan(
			&ss.Name, &ss.Hostname, &ss.Role, &ss.Region, &ss.Env, &ss.Instance, &tagsJSON, &ss.LastSeen,
			&ss.AgentVersion, &ss.Arch, &upgradePending,
			&ss.CPU, &ss.MemTotal, &ss.MemUsed,
			&ss.MemSwap, &ss.MemSwapTotal,
			&ss.DiskTotal, &ss.DiskUsed,
			&ss.NetSend, &ss.NetRecv,
			&componentsJSON,
			&ss.Instances, &ss.Capacity,
		)
		ss.UpgradeAvailable = upgradePending != 0
		if err != nil {
			return nil, fmt.Errorf("scan server: %w", err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &ss.Tags); err != nil {
			ss.Tags = []string{}
		}
		if err := json.Unmarshal([]byte(componentsJSON), &ss.Components); err != nil {
			ss.Components = []apitype.Component{}
		}
		servers = append(servers, ss)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate servers: %w", err)
	}

	if servers == nil {
		servers = []apitype.ServerSummary{}
	}
	return servers, nil
}

// ListFleet returns all servers with extended fields for fleet-wide views.
func (s *Store) ListFleet(ctx context.Context) ([]apitype.FleetServer, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			s.name, s.hostname, s.role, s.region, s.env, s.last_seen,
			s.agent_version,
			COALESCE(r.cpu_percent, 0), COALESCE(r.mem_total, 0), COALESCE(r.mem_used, 0),
			COALESCE(r.disk_total, 0), COALESCE(r.disk_used, 0),
			r.conntrack_count, r.conntrack_max,
			COALESCE(r.fd_allocated, 0), COALESCE(r.fd_max, 0),
			COALESCE(r.components, '[]'), COALESCE(r.updates, '[]'),
			COALESCE(r.failed_units, '[]'), r.zfs_pools,
			r.zfs_pool_health, r.zfs_arc_size, r.zfs_arc_hit_rate,
			MAX(COALESCE(r.net_rx_errors, 0) - s.net_rx_errors_baseline, 0),
			MAX(COALESCE(r.net_tx_errors, 0) - s.net_tx_errors_baseline, 0),
			MAX(COALESCE(r.net_rx_dropped, 0) - s.net_rx_dropped_baseline, 0),
			MAX(COALESCE(r.net_tx_dropped, 0) - s.net_tx_dropped_baseline, 0)
		FROM servers s
		LEFT JOIN reports r ON r.id = (
			SELECT id FROM reports WHERE server_id = s.id ORDER BY timestamp DESC LIMIT 1
		)
		ORDER BY s.name
	`)
	if err != nil {
		return nil, fmt.Errorf("query fleet: %w", err)
	}
	defer rows.Close()

	var servers []apitype.FleetServer
	for rows.Next() {
		var fs apitype.FleetServer
		var componentsJSON, updatesJSON, failedUnitsJSON string
		var zfsPoolsJSON sql.NullString
		err := rows.Scan(
			&fs.Name, &fs.Hostname, &fs.Role, &fs.Region, &fs.Env, &fs.LastSeen,
			&fs.AgentVersion,
			&fs.CPU, &fs.MemTotal, &fs.MemUsed,
			&fs.DiskTotal, &fs.DiskUsed,
			&fs.ConntrackCount, &fs.ConntrackMax,
			&fs.FDAllocated, &fs.FDMax,
			&componentsJSON, &updatesJSON, &failedUnitsJSON, &zfsPoolsJSON,
			&fs.ZFSPoolHealth, &fs.ZFSArcSize, &fs.ZFSArcHitRate,
			&fs.NetRxErrors, &fs.NetTxErrors,
			&fs.NetRxDropped, &fs.NetTxDropped,
		)
		if err != nil {
			return nil, fmt.Errorf("scan fleet server: %w", err)
		}
		if err := json.Unmarshal([]byte(componentsJSON), &fs.Components); err != nil {
			fs.Components = []apitype.Component{}
		}
		if err := json.Unmarshal([]byte(updatesJSON), &fs.Updates); err != nil {
			fs.Updates = []string{}
		}
		if err := json.Unmarshal([]byte(failedUnitsJSON), &fs.FailedUnits); err != nil {
			fs.FailedUnits = []string{}
		}
		if zfsPoolsJSON.Valid {
			if err := json.Unmarshal([]byte(zfsPoolsJSON.String), &fs.ZFSPools); err != nil {
				fs.ZFSPools = nil
			}
		}
		servers = append(servers, fs)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fleet: %w", err)
	}

	if servers == nil {
		servers = []apitype.FleetServer{}
	}
	return servers, nil
}

// GetServer returns a single server with its latest report and history.
func (s *Store) GetServer(ctx context.Context, name string) (*apitype.ServerDetail, error) {
	var sd apitype.ServerDetail
	var tagsJSON, componentsJSON, updatesJSON, failedUnitsJSON string
	var zfsPoolsJSON sql.NullString

	var upgradePending int
	err := s.db.QueryRowContext(ctx, `
		SELECT
			s.name, s.hostname, s.role, s.region, s.env, s.instance, s.tags,
			s.first_seen, s.last_seen,
			s.agent_version, s.arch, s.upgrade_pending,
			COALESCE(r.cpu_percent, 0), COALESCE(r.mem_total, 0), COALESCE(r.mem_used, 0),
			COALESCE(r.mem_free, 0), COALESCE(r.mem_swap, 0), COALESCE(r.mem_swap_total, 0),
			COALESCE(r.disk_total, 0), COALESCE(r.disk_used, 0), COALESCE(r.disk_free, 0),
			COALESCE(r.net_send, 0), COALESCE(r.net_recv, 0),
			r.zfs_used, r.zfs_free,
			r.backup_zfs_used, r.backup_zfs_free,
			COALESCE(r.uptime_secs, 0),
			COALESCE(r.load_avg_1, 0), COALESCE(r.load_avg_5, 0), COALESCE(r.load_avg_15, 0),
			r.zfs_pool_health, r.zfs_arc_size, r.zfs_arc_hit_rate,
			MAX(COALESCE(r.net_rx_errors, 0) - s.net_rx_errors_baseline, 0),
			MAX(COALESCE(r.net_rx_dropped, 0) - s.net_rx_dropped_baseline, 0),
			MAX(COALESCE(r.net_tx_errors, 0) - s.net_tx_errors_baseline, 0),
			MAX(COALESCE(r.net_tx_dropped, 0) - s.net_tx_dropped_baseline, 0),
			r.conntrack_count, r.conntrack_max,
			COALESCE(r.fd_allocated, 0), COALESCE(r.fd_max, 0),
			COALESCE(r.components, '[]'), COALESCE(r.updates, '[]'),
			COALESCE(r.failed_units, '[]'),
			r.zfs_pools
		FROM servers s
		LEFT JOIN reports r ON r.id = (
			SELECT id FROM reports WHERE server_id = s.id ORDER BY timestamp DESC LIMIT 1
		)
		WHERE s.name = ?
	`, name).Scan(
		&sd.Name, &sd.Hostname, &sd.Role, &sd.Region, &sd.Env, &sd.Instance, &tagsJSON,
		&sd.FirstSeen, &sd.LastSeen,
		&sd.AgentVersion, &sd.Arch, &upgradePending,
		&sd.CPU, &sd.MemTotal, &sd.MemUsed,
		&sd.MemFree, &sd.MemSwap, &sd.MemSwapTotal,
		&sd.DiskTotal, &sd.DiskUsed, &sd.DiskFree,
		&sd.NetSend, &sd.NetRecv,
		&sd.ZFSUsed, &sd.ZFSFree,
		&sd.BackupZFSUsed, &sd.BackupZFSFree,
		&sd.UptimeSecs,
		&sd.LoadAvg1, &sd.LoadAvg5, &sd.LoadAvg15,
		&sd.ZFSPoolHealth, &sd.ZFSArcSize, &sd.ZFSArcHitRate,
		&sd.NetRxErrors, &sd.NetRxDropped,
		&sd.NetTxErrors, &sd.NetTxDropped,
		&sd.ConntrackCount, &sd.ConntrackMax,
		&sd.FDAllocated, &sd.FDMax,
		&componentsJSON, &updatesJSON, &failedUnitsJSON,
		&zfsPoolsJSON,
	)
	sd.UpgradeAvailable = upgradePending != 0
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query server %q: %w", name, err)
	}

	if err := json.Unmarshal([]byte(tagsJSON), &sd.Tags); err != nil {
		sd.Tags = []string{}
	}
	if err := json.Unmarshal([]byte(componentsJSON), &sd.Components); err != nil {
		sd.Components = []apitype.Component{}
	}
	if err := json.Unmarshal([]byte(updatesJSON), &sd.Updates); err != nil {
		sd.Updates = []string{}
	}
	if err := json.Unmarshal([]byte(failedUnitsJSON), &sd.FailedUnits); err != nil {
		sd.FailedUnits = []string{}
	}
	if zfsPoolsJSON.Valid {
		if err := json.Unmarshal([]byte(zfsPoolsJSON.String), &sd.ZFSPools); err != nil {
			sd.ZFSPools = nil
		}
	}

	// Fetch history.
	rows, err := s.db.QueryContext(ctx, `
		SELECT timestamp, cpu_percent, mem_used, disk_used, net_send, net_recv, uptime_secs
		FROM reports r
		JOIN servers s ON s.id = r.server_id
		WHERE s.name = ?
		ORDER BY r.timestamp DESC
		LIMIT 1000
	`, name)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()

	sd.History = []apitype.ReportRow{}
	for rows.Next() {
		var rr apitype.ReportRow
		if err := rows.Scan(&rr.Timestamp, &rr.CPU, &rr.MemUsed, &rr.DiskUsed, &rr.NetSend, &rr.NetRecv, &rr.UptimeSecs); err != nil {
			return nil, fmt.Errorf("scan history: %w", err)
		}
		sd.History = append(sd.History, rr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate history: %w", err)
	}

	// Fetch exelet capacity history.
	capRows, err := s.GetExeletCapacityHistory(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("query exelet capacity: %w", err)
	}
	sd.ExeletCapacity = capRows

	return &sd, nil
}

// DeleteServer deletes a server and all its reports.
func (s *Store) DeleteServer(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRowContext(ctx, "SELECT id FROM servers WHERE name = ?", name).Scan(&id)
	if err != nil {
		return fmt.Errorf("find server %q: %w", name, err)
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM reports WHERE server_id = ?", id); err != nil {
		return fmt.Errorf("delete reports: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM servers WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete server: %w", err)
	}

	return tx.Commit()
}

// SetUpgradePending marks a server for upgrade.
func (s *Store) SetUpgradePending(ctx context.Context, name string, pending bool) error {
	val := 0
	if pending {
		val = 1
	}
	res, err := s.db.ExecContext(ctx, "UPDATE servers SET upgrade_pending = ? WHERE name = ?", val, name)
	if err != nil {
		return fmt.Errorf("set upgrade pending: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("server %q not found", name)
	}
	return nil
}

// IsUpgradePending checks if a server has a pending upgrade.
func (s *Store) IsUpgradePending(ctx context.Context, name string) (bool, error) {
	var pending int
	err := s.db.QueryRowContext(ctx, "SELECT upgrade_pending FROM servers WHERE name = ?", name).Scan(&pending)
	if err != nil {
		return false, fmt.Errorf("check upgrade pending: %w", err)
	}
	return pending != 0, nil
}

// ResetNetCounters sets the net counter baselines to the current values,
// effectively zeroing out the displayed counters.
func (s *Store) ResetNetCounters(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE servers SET
			net_rx_errors_baseline = COALESCE((
				SELECT r.net_rx_errors FROM reports r WHERE r.server_id = servers.id ORDER BY r.timestamp DESC LIMIT 1
			), 0),
			net_rx_dropped_baseline = COALESCE((
				SELECT r.net_rx_dropped FROM reports r WHERE r.server_id = servers.id ORDER BY r.timestamp DESC LIMIT 1
			), 0),
			net_tx_errors_baseline = COALESCE((
				SELECT r.net_tx_errors FROM reports r WHERE r.server_id = servers.id ORDER BY r.timestamp DESC LIMIT 1
			), 0),
			net_tx_dropped_baseline = COALESCE((
				SELECT r.net_tx_dropped FROM reports r WHERE r.server_id = servers.id ORDER BY r.timestamp DESC LIMIT 1
			), 0)
		WHERE name = ?
	`, name)
	if err != nil {
		return fmt.Errorf("reset net counters: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("server %q not found", name)
	}
	return nil
}

// PurgeOldReports deletes reports older than the given retention duration.
func (s *Store) PurgeOldReports(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-retention).Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, "DELETE FROM reports WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge reports: %w", err)
	}
	return res.RowsAffected()
}

// ListCustomAlerts returns all custom alert rules.
func (s *Store) ListCustomAlerts(ctx context.Context) ([]apitype.CustomAlertRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, metric, operator, threshold, severity, enabled, created_at FROM custom_alerts ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list custom alerts: %w", err)
	}
	defer rows.Close()

	var rules []apitype.CustomAlertRule
	for rows.Next() {
		var r apitype.CustomAlertRule
		var enabled int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Metric, &r.Operator, &r.Threshold, &r.Severity, &enabled, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan custom alert: %w", err)
		}
		r.Enabled = enabled != 0
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

// CreateCustomAlert inserts a new custom alert rule and returns its ID.
func (s *Store) CreateCustomAlert(ctx context.Context, r *apitype.CustomAlertRule) (int64, error) {
	var enabled int64
	if r.Enabled {
		enabled = 1
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO custom_alerts (name, metric, operator, threshold, severity, enabled) VALUES (?, ?, ?, ?, ?, ?)`,
		r.Name, r.Metric, r.Operator, r.Threshold, r.Severity, enabled)
	if err != nil {
		return 0, fmt.Errorf("create custom alert: %w", err)
	}
	return res.LastInsertId()
}

// UpdateCustomAlert updates an existing custom alert rule.
func (s *Store) UpdateCustomAlert(ctx context.Context, r *apitype.CustomAlertRule) error {
	var enabled int64
	if r.Enabled {
		enabled = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE custom_alerts SET name = ?, metric = ?, operator = ?, threshold = ?, severity = ?, enabled = ? WHERE id = ?`,
		r.Name, r.Metric, r.Operator, r.Threshold, r.Severity, enabled, r.ID)
	if err != nil {
		return fmt.Errorf("update custom alert: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("custom alert %d not found", r.ID)
	}
	return nil
}

// InsertExeletCapacity inserts a batch of exelet capacity snapshots.
func (s *Store) InsertExeletCapacity(ctx context.Context, env string, ts time.Time, entries []ExeletCapacityEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO exelet_capacity (server_name, env, timestamp, instances, capacity) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	tsStr := ts.UTC().Format(time.RFC3339)
	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, e.ServerName, env, tsStr, e.Instances, e.Capacity); err != nil {
			return fmt.Errorf("insert exelet capacity for %q: %w", e.ServerName, err)
		}
	}
	return tx.Commit()
}

// ExeletCapacityEntry is one exelet's capacity at a point in time (for insertion).
type ExeletCapacityEntry struct {
	ServerName string
	Instances  int
	Capacity   int
}

// GetExeletCapacityHistory returns capacity history for a server (last 1000 rows).
func (s *Store) GetExeletCapacityHistory(ctx context.Context, serverName string) ([]apitype.ExeletCapacityRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT timestamp, instances, capacity
		FROM exelet_capacity
		WHERE server_name = ?
		ORDER BY timestamp DESC
		LIMIT 1000
	`, serverName)
	if err != nil {
		return nil, fmt.Errorf("query exelet capacity: %w", err)
	}
	defer rows.Close()

	var result []apitype.ExeletCapacityRow
	for rows.Next() {
		var r apitype.ExeletCapacityRow
		if err := rows.Scan(&r.Timestamp, &r.Instances, &r.Capacity); err != nil {
			return nil, fmt.Errorf("scan exelet capacity: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate exelet capacity: %w", err)
	}
	if result == nil {
		result = []apitype.ExeletCapacityRow{}
	}
	return result, nil
}

// GetExeletCapacitySummary returns the aggregated latest capacity across all servers,
// optionally filtered by env and/or region.
func (s *Store) GetExeletCapacitySummary(ctx context.Context, env, region string) (*apitype.ExeletCapacitySummary, error) {
	query := `
		SELECT COALESCE(SUM(ec.instances), 0), COALESCE(SUM(ec.capacity), 0)
		FROM exelet_capacity ec
		INNER JOIN (
			SELECT server_name, MAX(timestamp) AS max_ts
			FROM exelet_capacity
			GROUP BY server_name
		) latest ON ec.server_name = latest.server_name AND ec.timestamp = latest.max_ts`

	var conditions []string
	var args []any

	if env != "" {
		conditions = append(conditions, "ec.env = ?")
		args = append(args, env)
	}
	if region != "" {
		query += ` INNER JOIN servers s ON ec.server_name = s.name`
		conditions = append(conditions, "s.region LIKE ?")
		args = append(args, region+"%")
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	var summary apitype.ExeletCapacitySummary
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&summary.TotalInstances, &summary.TotalCapacity)
	if err != nil {
		return nil, fmt.Errorf("query exelet capacity summary: %w", err)
	}
	return &summary, nil
}

// PurgeOldExeletCapacity deletes exelet capacity records older than the given retention.
func (s *Store) PurgeOldExeletCapacity(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-retention).Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, "DELETE FROM exelet_capacity WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge exelet capacity: %w", err)
	}
	return res.RowsAffected()
}

// DeleteCustomAlert deletes a custom alert rule by ID.
func (s *Store) DeleteCustomAlert(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM custom_alerts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete custom alert: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("custom alert %d not found", id)
	}
	return nil
}
