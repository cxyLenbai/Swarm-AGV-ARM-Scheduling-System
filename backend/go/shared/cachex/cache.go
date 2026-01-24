package cachex

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"swarm-agv-arm-scheduling-system/shared/config"
)

type Client struct {
	redis *redis.Client
}

func New(cfg config.Config) (*Client, error) {
	if cfg.RedisAddr == "" {
		return nil, errors.New("REDIS_ADDR is required")
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	return &Client{redis: rdb}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.redis == nil {
		return errors.New("redis client not initialized")
	}
	return c.redis.Ping(ctx).Err()
}

func (c *Client) Close() error {
	if c == nil || c.redis == nil {
		return nil
	}
	return c.redis.Close()
}

func (c *Client) SetJSON(ctx context.Context, key string, value any, ttl time.Duration) error {
	if c == nil || c.redis == nil {
		return errors.New("redis client not initialized")
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.redis.Set(ctx, key, b, ttl).Err()
}

func (c *Client) GetJSON(ctx context.Context, key string, dest any) (bool, error) {
	if c == nil || c.redis == nil {
		return false, errors.New("redis client not initialized")
	}
	raw, err := c.redis.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return false, err
	}
	return true, nil
}

func (c *Client) Delete(ctx context.Context, key string) error {
	if c == nil || c.redis == nil {
		return errors.New("redis client not initialized")
	}
	return c.redis.Del(ctx, key).Err()
}

func (c *Client) Client() *redis.Client {
	if c == nil {
		return nil
	}
	return c.redis
}
