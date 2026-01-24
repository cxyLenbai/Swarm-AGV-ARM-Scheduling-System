package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"swarm-agv-arm-scheduling-system/shared/httpx"
)

type RateLimitMiddleware struct {
	Limiter *IPRateLimiter
	Skip    func(*http.Request) bool
}

func (m RateLimitMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.Skip != nil && m.Skip(r) {
			next.ServeHTTP(w, r)
			return
		}
		if m.Limiter == nil {
			next.ServeHTTP(w, r)
			return
		}
		key := rateLimitClientIP(r)
		if key == "" {
			key = "unknown"
		}
		if !m.Limiter.Allow(key) {
			httpx.WriteError(w, r, http.StatusTooManyRequests, "FAILED_PRECONDITION", "rate limit exceeded", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type IPRateLimiter struct {
	mu      sync.Mutex
	rps     float64
	burst   float64
	ttl     time.Duration
	clients map[string]*clientTokens
}

type clientTokens struct {
	tokens   float64
	lastSeen time.Time
}

func NewIPRateLimiter(rps float64, burst int, ttl time.Duration) *IPRateLimiter {
	if rps <= 0 {
		rps = 5
	}
	if burst <= 0 {
		burst = 10
	}
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	return &IPRateLimiter{
		rps:     rps,
		burst:   float64(burst),
		ttl:     ttl,
		clients: make(map[string]*clientTokens),
	}
}

func (l *IPRateLimiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	l.cleanup(now)

	client, ok := l.clients[key]
	if !ok {
		l.clients[key] = &clientTokens{
			tokens:   l.burst - 1,
			lastSeen: now,
		}
		return true
	}

	elapsed := now.Sub(client.lastSeen).Seconds()
	client.tokens += elapsed * l.rps
	if client.tokens > l.burst {
		client.tokens = l.burst
	}
	client.lastSeen = now
	if client.tokens < 1 {
		return false
	}
	client.tokens -= 1
	return true
}

func (l *IPRateLimiter) cleanup(now time.Time) {
	if l.ttl <= 0 {
		return
	}
	for key, client := range l.clients {
		if now.Sub(client.lastSeen) > l.ttl {
			delete(l.clients, key)
		}
	}
}

func rateLimitClientIP(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); v != "" {
		parts := strings.Split(v, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
		return v
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
