//go:build integration

package integration

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"
)

func TestDependencies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		pool, err := pgxpool.New(ctx, dbURL)
		if err != nil {
			t.Fatalf("db connect failed: %v", err)
		}
		defer pool.Close()
		if err := pool.Ping(ctx); err != nil {
			t.Fatalf("db ping failed: %v", err)
		}
	} else {
		t.Skip("DATABASE_URL not set")
	}

	brokers := strings.Split(os.Getenv("KAFKA_BROKERS"), ",")
	if len(brokers) == 0 || strings.TrimSpace(brokers[0]) == "" {
		t.Skip("KAFKA_BROKERS not set")
	}
	conn, err := kafka.Dial("tcp", strings.TrimSpace(brokers[0]))
	if err != nil {
		t.Fatalf("kafka dial failed: %v", err)
	}
	_ = conn.Close()

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		t.Skip("REDIS_ADDR not set")
	}
	redisClient := redis.NewClient(&redis.Options{Addr: redisAddr})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis ping failed: %v", err)
	}
	_ = redisClient.Close()

	influxURL := os.Getenv("INFLUX_URL")
	if influxURL == "" {
		t.Skip("INFLUX_URL not set")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, influxURL+"/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("influx health failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("influx health status: %d", resp.StatusCode)
	}

	asynqRedis := os.Getenv("ASYNQ_REDIS_ADDR")
	if asynqRedis == "" {
		t.Skip("ASYNQ_REDIS_ADDR not set")
	}
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: asynqRedis})
	defer inspector.Close()
	_, err = inspector.GetQueueInfo("default")
	if err != nil {
		t.Fatalf("asynq inspector failed: %v", err)
	}

	if _, err := net.DialTimeout("tcp", strings.TrimSpace(brokers[0]), 2*time.Second); err != nil {
		t.Fatalf("kafka tcp check failed: %v", err)
	}
}
