package authx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v2/jwk"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrUnknownKID   = errors.New("unknown kid")
)

type AuthContext struct {
	Subject string
	Email   string
	Name    string
	Roles   []string
	Claims  map[string]any
}

type contextKey struct{}

func WithAuth(ctx context.Context, auth AuthContext) context.Context {
	return context.WithValue(ctx, contextKey{}, auth)
}

func FromContext(ctx context.Context) (AuthContext, bool) {
	if v := ctx.Value(contextKey{}); v != nil {
		if a, ok := v.(AuthContext); ok {
			return a, true
		}
	}
	return AuthContext{}, false
}

type JWTVerifier struct {
	issuer    string
	audience  string
	jwks      *JWKSCache
	clockSkew time.Duration
	parser    *jwt.Parser
}

func NewJWTVerifier(issuer string, audience string, jwksURL string, ttlSeconds int, clockSkewSeconds int) (*JWTVerifier, error) {
	issuer = strings.TrimSpace(issuer)
	audience = strings.TrimSpace(audience)
	if issuer == "" || audience == "" {
		return nil, fmt.Errorf("%w: missing issuer or audience", ErrInvalidToken)
	}
	if jwksURL == "" {
		jwksURL = strings.TrimRight(issuer, "/") + "/.well-known/jwks.json"
	}
	if ttlSeconds <= 0 {
		ttlSeconds = 300
	}
	if clockSkewSeconds < 0 {
		clockSkewSeconds = 0
	}

	return &JWTVerifier{
		issuer:    issuer,
		audience:  audience,
		jwks:      NewJWKSCache(jwksURL, time.Duration(ttlSeconds)*time.Second, &http.Client{Timeout: 5 * time.Second}),
		clockSkew: time.Duration(clockSkewSeconds) * time.Second,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}),
			jwt.WithAudience(audience),
			jwt.WithIssuer(issuer),
			jwt.WithLeeway(time.Duration(clockSkewSeconds)*time.Second),
		),
	}, nil
}

func (v *JWTVerifier) Verify(ctx context.Context, rawToken string) (AuthContext, error) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return AuthContext{}, ErrInvalidToken
	}

	claims := jwt.MapClaims{}
	_, err := v.parser.ParseWithClaims(rawToken, claims, func(token *jwt.Token) (any, error) {
		kid, _ := token.Header["kid"].(string)
		kid = strings.TrimSpace(kid)
		if kid == "" {
			return nil, ErrUnknownKID
		}
		return v.jwks.GetKey(ctx, kid)
	})
	if err != nil {
		return AuthContext{}, ErrInvalidToken
	}

	if claims["exp"] == nil || claims["nbf"] == nil || claims["iss"] == nil || claims["aud"] == nil {
		return AuthContext{}, ErrInvalidToken
	}

	subject := strings.TrimSpace(fmt.Sprint(claims["sub"]))
	if subject == "" {
		return AuthContext{}, ErrInvalidToken
	}

	email := strings.TrimSpace(fmt.Sprint(claims["email"]))
	name := strings.TrimSpace(fmt.Sprint(claims["name"]))
	if name == "" {
		name = strings.TrimSpace(fmt.Sprint(claims["preferred_username"]))
	}

	return AuthContext{
		Subject: subject,
		Email:   email,
		Name:    name,
		Roles:   parseRoles(claims),
		Claims:  map[string]any(claims),
	}, nil
}

type JWKSCache struct {
	url        string
	ttl        time.Duration
	client     *http.Client
	mu         sync.RWMutex
	keysByKID  map[string]any
	expiresAt  time.Time
	lastUpdate time.Time
}

func NewJWKSCache(url string, ttl time.Duration, client *http.Client) *JWKSCache {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &JWKSCache{
		url:       url,
		ttl:       ttl,
		client:    client,
		keysByKID: map[string]any{},
	}
}

func (c *JWKSCache) GetKey(ctx context.Context, kid string) (any, error) {
	if kid == "" {
		return nil, ErrUnknownKID
	}

	now := time.Now()
	c.mu.RLock()
	key := c.keysByKID[kid]
	expiresAt := c.expiresAt
	c.mu.RUnlock()

	if key != nil && now.Before(expiresAt) {
		return key, nil
	}

	if err := c.refresh(ctx); err != nil {
		c.mu.RLock()
		key = c.keysByKID[kid]
		expiresAt = c.expiresAt
		c.mu.RUnlock()
		if key != nil && now.Before(expiresAt) {
			return key, nil
		}
		return nil, err
	}

	c.mu.RLock()
	key = c.keysByKID[kid]
	c.mu.RUnlock()
	if key == nil {
		return nil, ErrUnknownKID
	}
	return key, nil
}

func (c *JWKSCache) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("jwks fetch failed: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	set, err := jwk.Parse(body)
	if err != nil {
		return err
	}

	keys := make(map[string]any)
	iter := set.Iterate(ctx)
	for iter.Next(ctx) {
		pair := iter.Pair()
		key, ok := pair.Value.(jwk.Key)
		if !ok {
			continue
		}
		kid := strings.TrimSpace(key.KeyID())
		if kid == "" {
			continue
		}
		var raw any
		if err := key.Raw(&raw); err != nil {
			continue
		}
		keys[kid] = raw
	}
	if err := iter.Err(); err != nil {
		return err
	}
	if len(keys) == 0 {
		return errors.New("no usable jwks keys")
	}

	c.mu.Lock()
	c.keysByKID = keys
	c.expiresAt = time.Now().Add(c.ttl)
	c.lastUpdate = time.Now()
	c.mu.Unlock()
	return nil
}

func parseRoles(claims map[string]any) []string {
	var roles []string
	appendRole := func(role string) {
		role = strings.TrimSpace(role)
		if role == "" {
			return
		}
		for _, existing := range roles {
			if existing == role {
				return
			}
		}
		roles = append(roles, role)
	}

	for _, key := range []string{"roles", "role"} {
		if v, ok := claims[key]; ok {
			switch t := v.(type) {
			case []string:
				for _, role := range t {
					appendRole(role)
				}
			case []any:
				for _, role := range t {
					appendRole(fmt.Sprint(role))
				}
			case string:
				for _, role := range strings.Fields(t) {
					appendRole(role)
				}
			default:
				appendRole(fmt.Sprint(t))
			}
		}
	}

	if v, ok := claims["scp"]; ok {
		if s, ok := v.(string); ok {
			for _, scope := range strings.Fields(s) {
				appendRole(scope)
			}
		}
	}

	return roles
}
