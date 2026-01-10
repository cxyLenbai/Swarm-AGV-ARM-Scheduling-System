package repos

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"swarm-agv-arm-scheduling-system/api/internal/models"
)

type TenantsRepo struct {
	pool *pgxpool.Pool
}

func NewTenantsRepo(pool *pgxpool.Pool) *TenantsRepo {
	return &TenantsRepo{pool: pool}
}

func (r *TenantsRepo) CreateTenant(ctx context.Context, slug string, name string) (models.Tenant, error) {
	var tenant models.Tenant
	err := r.pool.QueryRow(ctx, `
		INSERT INTO tenants (slug, name)
		VALUES ($1, $2)
		RETURNING tenant_id, slug, name, created_at
	`, slug, name).Scan(&tenant.TenantID, &tenant.Slug, &tenant.Name, &tenant.CreatedAt)
	return tenant, err
}

func (r *TenantsRepo) GetTenantByID(ctx context.Context, tenantID uuid.UUID) (models.Tenant, error) {
	var tenant models.Tenant
	err := r.pool.QueryRow(ctx, `
		SELECT tenant_id, slug, name, created_at
		FROM tenants
		WHERE tenant_id = $1
	`, tenantID).Scan(&tenant.TenantID, &tenant.Slug, &tenant.Name, &tenant.CreatedAt)
	return tenant, err
}

func (r *TenantsRepo) GetTenantBySlug(ctx context.Context, slug string) (models.Tenant, error) {
	var tenant models.Tenant
	err := r.pool.QueryRow(ctx, `
		SELECT tenant_id, slug, name, created_at
		FROM tenants
		WHERE slug = $1
	`, slug).Scan(&tenant.TenantID, &tenant.Slug, &tenant.Name, &tenant.CreatedAt)
	return tenant, err
}
