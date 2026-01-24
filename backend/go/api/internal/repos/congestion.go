package repos

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"swarm-agv-arm-scheduling-system/api/internal/models"
)

type CongestionRepo struct {
	pool *pgxpool.Pool
}

func NewCongestionRepo(pool *pgxpool.Pool) *CongestionRepo {
	return &CongestionRepo{pool: pool}
}

func (r *CongestionRepo) EnsureZone(ctx context.Context, tenantID uuid.UUID, zoneID uuid.UUID, name string) error {
	if name == "" {
		name = zoneID.String()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO zones (zone_id, tenant_id, name)
		VALUES ($1, $2, $3)
		ON CONFLICT (zone_id) DO NOTHING
	`, zoneID, tenantID, name)
	return err
}

func (r *CongestionRepo) UpsertZoneSnapshot(ctx context.Context, snapshot models.ZoneCongestionSnapshot) (models.ZoneCongestionSnapshot, error) {
	if snapshot.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = time.Now().UTC()
	}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO zone_congestion_snapshots (
			tenant_id, zone_id, congestion_index, avg_speed, queue_length, risk, confidence, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8
		)
		ON CONFLICT (tenant_id, zone_id)
		DO UPDATE SET
			congestion_index = EXCLUDED.congestion_index,
			avg_speed = EXCLUDED.avg_speed,
			queue_length = EXCLUDED.queue_length,
			risk = EXCLUDED.risk,
			confidence = EXCLUDED.confidence,
			updated_at = EXCLUDED.updated_at
		RETURNING snapshot_id, tenant_id, zone_id, congestion_index, avg_speed, queue_length, risk, confidence, updated_at
	`, snapshot.TenantID, snapshot.ZoneID, snapshot.CongestionIndex, snapshot.AvgSpeed, snapshot.QueueLength, snapshot.Risk, snapshot.Confidence, snapshot.UpdatedAt).
		Scan(&snapshot.SnapshotID, &snapshot.TenantID, &snapshot.ZoneID, &snapshot.CongestionIndex, &snapshot.AvgSpeed, &snapshot.QueueLength, &snapshot.Risk, &snapshot.Confidence, &snapshot.UpdatedAt)
	return snapshot, err
}

func (r *CongestionRepo) CreateAlert(ctx context.Context, alert models.CongestionAlert) (models.CongestionAlert, error) {
	if alert.DetectedAt.IsZero() {
		alert.DetectedAt = time.Now().UTC()
	}
	if alert.Status == "" {
		alert.Status = "open"
	}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO congestion_alerts (
			tenant_id, zone_id, level, status, congestion_index, risk, confidence, detected_at, message, details
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10
		)
		RETURNING alert_id, tenant_id, zone_id, level, status, congestion_index, risk, confidence, detected_at, message, details
	`, alert.TenantID, alert.ZoneID, alert.Level, alert.Status, alert.CongestionIndex, alert.Risk, alert.Confidence, alert.DetectedAt, alert.Message, alert.Details).
		Scan(&alert.AlertID, &alert.TenantID, &alert.ZoneID, &alert.Level, &alert.Status, &alert.CongestionIndex, &alert.Risk, &alert.Confidence, &alert.DetectedAt, &alert.Message, &alert.Details)
	return alert, err
}

func (r *CongestionRepo) ListHotspots(ctx context.Context, tenantID uuid.UUID, horizon time.Duration, limit int) ([]models.ZoneCongestionSnapshot, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if horizon > 0 {
		rows, err = r.pool.Query(ctx, `
			SELECT snapshot_id, tenant_id, zone_id, congestion_index, avg_speed, queue_length, risk, confidence, updated_at
			FROM zone_congestion_snapshots
			WHERE tenant_id = $1 AND updated_at >= $2
			ORDER BY congestion_index DESC, updated_at DESC
			LIMIT $3
		`, tenantID, time.Now().UTC().Add(-horizon), limit)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT snapshot_id, tenant_id, zone_id, congestion_index, avg_speed, queue_length, risk, confidence, updated_at
			FROM zone_congestion_snapshots
			WHERE tenant_id = $1
			ORDER BY congestion_index DESC, updated_at DESC
			LIMIT $2
		`, tenantID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	snapshots := make([]models.ZoneCongestionSnapshot, 0, limit)
	for rows.Next() {
		var s models.ZoneCongestionSnapshot
		if err := rows.Scan(&s.SnapshotID, &s.TenantID, &s.ZoneID, &s.CongestionIndex, &s.AvgSpeed, &s.QueueLength, &s.Risk, &s.Confidence, &s.UpdatedAt); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, s)
	}
	return snapshots, rows.Err()
}

func (r *CongestionRepo) ListAlerts(ctx context.Context, tenantID uuid.UUID, status string, limit int) ([]models.CongestionAlert, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if status != "" {
		rows, err = r.pool.Query(ctx, `
			SELECT alert_id, tenant_id, zone_id, level, status, congestion_index, risk, confidence, detected_at, message, details
			FROM congestion_alerts
			WHERE tenant_id = $1 AND status = $2
			ORDER BY detected_at DESC
			LIMIT $3
		`, tenantID, status, limit)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT alert_id, tenant_id, zone_id, level, status, congestion_index, risk, confidence, detected_at, message, details
			FROM congestion_alerts
			WHERE tenant_id = $1
			ORDER BY detected_at DESC
			LIMIT $2
		`, tenantID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	alerts := make([]models.CongestionAlert, 0, limit)
	for rows.Next() {
		var alert models.CongestionAlert
		if err := rows.Scan(&alert.AlertID, &alert.TenantID, &alert.ZoneID, &alert.Level, &alert.Status, &alert.CongestionIndex, &alert.Risk, &alert.Confidence, &alert.DetectedAt, &alert.Message, &alert.Details); err != nil {
			return nil, err
		}
		alerts = append(alerts, alert)
	}
	return alerts, rows.Err()
}

func (r *CongestionRepo) UpdateAlertStatus(ctx context.Context, tenantID uuid.UUID, alertID uuid.UUID, status string, notes string) error {
	var details []byte
	if notes != "" {
		payload, _ := json.Marshal(map[string]any{"notes": notes})
		details = payload
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE congestion_alerts
		SET status = $3, details = COALESCE($4, details)
		WHERE tenant_id = $1 AND alert_id = $2
	`, tenantID, alertID, status, details)
	return err
}
