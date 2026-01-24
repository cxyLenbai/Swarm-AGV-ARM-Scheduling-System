CREATE EXTENSION IF NOT EXISTS postgis;

CREATE TABLE IF NOT EXISTS zones (
    zone_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(tenant_id) ON DELETE RESTRICT,
    name TEXT NOT NULL,
    description TEXT,
    geom geometry,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_zones_tenant
    ON zones (tenant_id);

CREATE TABLE IF NOT EXISTS zone_congestion_snapshots (
    snapshot_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(tenant_id) ON DELETE RESTRICT,
    zone_id UUID NOT NULL REFERENCES zones(zone_id) ON DELETE RESTRICT,
    congestion_index DOUBLE PRECISION NOT NULL,
    avg_speed DOUBLE PRECISION NOT NULL,
    queue_length DOUBLE PRECISION NOT NULL,
    risk DOUBLE PRECISION NOT NULL,
    confidence DOUBLE PRECISION NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, zone_id)
);

CREATE INDEX IF NOT EXISTS idx_zone_congestion_snapshots_updated
    ON zone_congestion_snapshots (tenant_id, updated_at);

CREATE TABLE IF NOT EXISTS congestion_alerts (
    alert_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(tenant_id) ON DELETE RESTRICT,
    zone_id UUID NOT NULL REFERENCES zones(zone_id) ON DELETE RESTRICT,
    level TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'open',
    congestion_index DOUBLE PRECISION NOT NULL,
    risk DOUBLE PRECISION NOT NULL,
    confidence DOUBLE PRECISION NOT NULL,
    detected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    message TEXT,
    details JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_congestion_alerts_tenant_detected
    ON congestion_alerts (tenant_id, detected_at);

CREATE INDEX IF NOT EXISTS idx_congestion_alerts_status
    ON congestion_alerts (tenant_id, status);

CREATE TABLE IF NOT EXISTS congestion_actions (
    action_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(tenant_id) ON DELETE RESTRICT,
    alert_id UUID NOT NULL REFERENCES congestion_alerts(alert_id) ON DELETE RESTRICT,
    action_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    notes TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_congestion_actions_alert
    ON congestion_actions (tenant_id, alert_id);
