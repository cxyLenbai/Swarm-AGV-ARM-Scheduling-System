CREATE TABLE IF NOT EXISTS robot_statuses (
    status_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(tenant_id) ON DELETE RESTRICT,
    robot_id UUID NOT NULL REFERENCES robots(robot_id) ON DELETE RESTRICT,
    robot_code TEXT NOT NULL,
    reported_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_robot_statuses_tenant_robot_time
    ON robot_statuses (tenant_id, robot_code, reported_at DESC);
