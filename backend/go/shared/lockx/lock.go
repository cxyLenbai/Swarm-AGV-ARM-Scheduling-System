package lockx

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const releaseScript = `
if redis.call("get", KEYS[1]) == ARGV[1] then
	return redis.call("del", KEYS[1])
end
return 0
`

type Lock struct {
	Key   string
	Token string
	TTL   time.Duration
}

func Acquire(ctx context.Context, client *redis.Client, key string, ttl time.Duration) (*Lock, bool, error) {
	if client == nil {
		return nil, false, errors.New("redis client not initialized")
	}
	if ttl <= 0 {
		return nil, false, errors.New("ttl must be > 0")
	}
	token := uuid.NewString()
	ok, err := client.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return &Lock{Key: key, Token: token, TTL: ttl}, true, nil
}

func Release(ctx context.Context, client *redis.Client, lock *Lock) error {
	if client == nil {
		return errors.New("redis client not initialized")
	}
	if lock == nil {
		return errors.New("lock is nil")
	}
	return client.Eval(ctx, releaseScript, []string{lock.Key}, lock.Token).Err()
}
