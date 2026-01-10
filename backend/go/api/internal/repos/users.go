package repos

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"swarm-agv-arm-scheduling-system/api/internal/models"
)

type UsersRepo struct {
	pool *pgxpool.Pool
}

func NewUsersRepo(pool *pgxpool.Pool) *UsersRepo {
	return &UsersRepo{pool: pool}
}

func (r *UsersRepo) UpsertUserFromOIDC(ctx context.Context, tenantID uuid.UUID, subject string, email string, displayName string, role string) (models.User, error) {
	var user models.User
	now := time.Now().UTC()
	err := r.pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, subject, email, display_name, role, created_at, last_login_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)
		ON CONFLICT (tenant_id, subject) DO UPDATE SET
			email = EXCLUDED.email,
			display_name = EXCLUDED.display_name,
			role = COALESCE(EXCLUDED.role, users.role),
			last_login_at = EXCLUDED.last_login_at
		RETURNING user_id, tenant_id, subject, email, display_name, role, created_at, last_login_at
	`, tenantID, subject, nullIfEmpty(email), nullIfEmpty(displayName), nullIfEmpty(role), now).
		Scan(&user.UserID, &user.TenantID, &user.Subject, &user.Email, &user.DisplayName, &user.Role, &user.CreatedAt, &user.LastLoginAt)
	return user, err
}

func (r *UsersRepo) GetUserByID(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) (models.User, error) {
	var user models.User
	err := r.pool.QueryRow(ctx, `
		SELECT user_id, tenant_id, subject, email, display_name, role, created_at, last_login_at
		FROM users
		WHERE tenant_id = $1 AND user_id = $2
	`, tenantID, userID).
		Scan(&user.UserID, &user.TenantID, &user.Subject, &user.Email, &user.DisplayName, &user.Role, &user.CreatedAt, &user.LastLoginAt)
	return user, err
}

func nullIfEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
