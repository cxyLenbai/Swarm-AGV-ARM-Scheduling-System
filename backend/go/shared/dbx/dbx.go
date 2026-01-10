package dbx

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"swarm-agv-arm-scheduling-system/shared/config"
)

func NewPool(cfg config.Config) (*pgxpool.Pool, error) {
	if cfg.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}

	poolCfg.MaxConns = int32(cfg.DBMaxConns)
	poolCfg.MinConns = int32(cfg.DBMinConns)
	poolCfg.MaxConnIdleTime = time.Duration(cfg.DBConnMaxIdleSec) * time.Second
	poolCfg.MaxConnLifetime = time.Duration(cfg.DBConnMaxLifeSec) * time.Second

	return pgxpool.NewWithConfig(context.Background(), poolCfg)
}

func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("db pool is nil")
	}
	var one int
	return pool.QueryRow(ctx, "SELECT 1").Scan(&one)
}
