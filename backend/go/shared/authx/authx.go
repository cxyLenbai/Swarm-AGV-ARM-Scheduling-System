package authx // 认证工具包

import ( // 依赖导入
	"context"  // 上下文处理
	"errors"   // 错误处理
	"fmt"      // 格式化工具
	"io"       // IO 工具
	"net/http" // HTTP 客户端
	"strings"  // 字符串工具
	"sync"     // 同步原语
	"time"     // 时间工具

	"github.com/golang-jwt/jwt/v5"      // JWT 解析
	"github.com/lestrrat-go/jwx/v2/jwk" // JWK 解析
)

var ( // 包内错误
	ErrInvalidToken = errors.New("invalid token") // 无效 token
	ErrUnknownKID   = errors.New("unknown kid")   // 未知 KID
)

type AuthContext struct { // 认证上下文
	Subject string         // Subject 声明
	Email   string         // 邮箱声明
	Name    string         // 显示名称
	Roles   []string       // 角色列表
	Claims  map[string]any // 原始 claims
}

type contextKey struct{} // 上下文键类型

func WithAuth(ctx context.Context, auth AuthContext) context.Context { // 将认证信息写入上下文
	return context.WithValue(ctx, contextKey{}, auth) // 设置值
}

func FromContext(ctx context.Context) (AuthContext, bool) { // 从上下文获取认证信息
	if v := ctx.Value(contextKey{}); v != nil { // 读取值
		if a, ok := v.(AuthContext); ok { // 类型断言
			return a, true // 获取成功
		}
	}
	return AuthContext{}, false // 未找到
}

type JWTVerifier struct { // JWT 验证器
	issuer    string        // 期望的 issuer
	audience  string        // 期望的 audience
	jwks      *JWKSCache    // JWKS 缓存
	clockSkew time.Duration // 时钟偏差
	parser    *jwt.Parser   // JWT 解析器
}

func NewJWTVerifier(issuer string, audience string, jwksURL string, ttlSeconds int, clockSkewSeconds int) (*JWTVerifier, error) { // 构造验证器
	issuer = strings.TrimSpace(issuer)     // 清理 issuer
	audience = strings.TrimSpace(audience) // 清理 audience
	if issuer == "" || audience == "" {    // 校验输入
		return nil, fmt.Errorf("%w: missing issuer or audience", ErrInvalidToken) // 返回错误
	}
	if jwksURL == "" { // 使用默认 JWKS URL
		jwksURL = strings.TrimRight(issuer, "/") + "/.well-known/jwks.json" // 拼接默认路径
	}
	if ttlSeconds <= 0 { // 默认 TTL
		ttlSeconds = 300 // 5 分钟
	}
	if clockSkewSeconds < 0 { // 纠正负数偏差
		clockSkewSeconds = 0 // 不允许负数
	}

	return &JWTVerifier{ // 构造实例
		issuer:    issuer,                                                                                               // 设置 issuer
		audience:  audience,                                                                                             // 设置 audience
		jwks:      NewJWKSCache(jwksURL, time.Duration(ttlSeconds)*time.Second, &http.Client{Timeout: 5 * time.Second}), // 初始化 JWKS 缓存
		clockSkew: time.Duration(clockSkewSeconds) * time.Second,                                                        // 设置偏差
		parser: jwt.NewParser( // 配置解析器
			jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}), // 允许的算法
			jwt.WithAudience(audience), // audience 校验
			jwt.WithIssuer(issuer),     // issuer 校验
			jwt.WithLeeway(time.Duration(clockSkewSeconds)*time.Second), // 时钟偏差
		),
	}, nil // 返回实例
}

func (v *JWTVerifier) Verify(ctx context.Context, rawToken string) (AuthContext, error) { // 验证 token
	rawToken = strings.TrimSpace(rawToken) // 清理 token
	if rawToken == "" {                    // 空 token
		return AuthContext{}, ErrInvalidToken // 无效
	}

	claims := jwt.MapClaims{}                                                                  // 创建 claims
	_, err := v.parser.ParseWithClaims(rawToken, claims, func(token *jwt.Token) (any, error) { // 解析 token
		kid, _ := token.Header["kid"].(string) // 读取 kid
		kid = strings.TrimSpace(kid)           // 清理 kid
		if kid == "" {                         // 缺少 kid
			return nil, ErrUnknownKID // 返回错误
		}
		return v.jwks.GetKey(ctx, kid) // 获取公钥
	})
	if err != nil { // 解析失败
		return AuthContext{}, ErrInvalidToken // 无效 token
	}

	if claims["exp"] == nil || claims["nbf"] == nil || claims["iss"] == nil || claims["aud"] == nil { // 关键声明缺失
		return AuthContext{}, ErrInvalidToken // 无效 token
	}

	subject := strings.TrimSpace(fmt.Sprint(claims["sub"])) // 提取 subject
	if subject == "" {                                      // 缺少 subject
		return AuthContext{}, ErrInvalidToken // 无效 token
	}

	email := strings.TrimSpace(fmt.Sprint(claims["email"])) // 提取邮箱
	name := strings.TrimSpace(fmt.Sprint(claims["name"]))   // 提取名称
	if name == "" {                                         // 使用备用字段
		name = strings.TrimSpace(fmt.Sprint(claims["preferred_username"])) // 备用名称
	}

	return AuthContext{ // 返回认证上下文
		Subject: subject,                // 设置 subject
		Email:   email,                  // 设置邮箱
		Name:    name,                   // 设置名称
		Roles:   parseRoles(claims),     // 解析角色
		Claims:  map[string]any(claims), // 原始 claims
	}, nil // 成功
}

type JWKSCache struct { // JWKS 缓存
	url        string         // JWKS URL
	ttl        time.Duration  // 缓存 TTL
	client     *http.Client   // HTTP 客户端
	mu         sync.RWMutex   // 读写锁
	keysByKID  map[string]any // KID 到 key
	expiresAt  time.Time      // 过期时间
	lastUpdate time.Time      // 最后更新时间
}

func NewJWKSCache(url string, ttl time.Duration, client *http.Client) *JWKSCache { // 构造缓存
	if client == nil { // 默认客户端
		client = &http.Client{Timeout: 5 * time.Second} // 设置超时
	}
	return &JWKSCache{ // 初始化缓存
		url:       url,              // 设置 URL
		ttl:       ttl,              // 设置 TTL
		client:    client,           // 设置客户端
		keysByKID: map[string]any{}, // 初始化缓存
	}
}

func (c *JWKSCache) GetKey(ctx context.Context, kid string) (any, error) { // 获取 key
	if kid == "" { // 缺少 kid
		return nil, ErrUnknownKID // 返回错误
	}

	now := time.Now()        // 当前时间
	c.mu.RLock()             // 加读锁
	key := c.keysByKID[kid]  // 读取 key
	expiresAt := c.expiresAt // 读取过期时间
	c.mu.RUnlock()           // 释放读锁

	if key != nil && now.Before(expiresAt) { // 命中且未过期
		return key, nil // 返回 key
	}

	if err := c.refresh(ctx); err != nil { // 刷新缓存
		c.mu.RLock()                             // 重新读取
		key = c.keysByKID[kid]                   // 读取 key
		expiresAt = c.expiresAt                  // 读取过期时间
		c.mu.RUnlock()                           // 释放读锁
		if key != nil && now.Before(expiresAt) { // 兜底命中
			return key, nil // 返回 key
		}
		return nil, err // 返回错误
	}

	c.mu.RLock()           // 读取刷新后缓存
	key = c.keysByKID[kid] // 读取 key
	c.mu.RUnlock()         // 释放读锁
	if key == nil {        // 仍然不存在
		return nil, ErrUnknownKID // 返回错误
	}
	return key, nil // 返回 key
}

func (c *JWKSCache) refresh(ctx context.Context) error { // 刷新缓存
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil) // 构造请求
	if err != nil {                                                         // 构造失败
		return err // 返回错误
	}
	req.Header.Set("Accept", "application/json") // 期望 JSON

	resp, err := c.client.Do(req) // 发起请求
	if err != nil {               // 请求失败
		return err // 返回错误
	}
	defer resp.Body.Close() // 关闭响应体

	if resp.StatusCode < 200 || resp.StatusCode >= 300 { // 非 2xx
		return fmt.Errorf("jwks fetch failed: status %d", resp.StatusCode) // 返回错误
	}

	body, err := io.ReadAll(resp.Body) // 读取响应
	if err != nil {                    // 读取失败
		return err // 返回错误
	}

	set, err := jwk.Parse(body) // 解析 JWKS
	if err != nil {             // 解析失败
		return err // 返回错误
	}

	keys := make(map[string]any) // 收集 key
	iter := set.Iterate(ctx)     // 遍历器
	for iter.Next(ctx) {         // 遍历
		pair := iter.Pair()             // 获取元素
		key, ok := pair.Value.(jwk.Key) // 类型断言
		if !ok {                        // 类型不匹配
			continue // 跳过
		}
		kid := strings.TrimSpace(key.KeyID()) // 读取 kid
		if kid == "" {                        // 空 kid
			continue // 跳过
		}
		var raw any                           // 原始 key
		if err := key.Raw(&raw); err != nil { // 提取失败
			continue // 跳过
		}
		keys[kid] = raw // 写入缓存
	}
	if len(keys) == 0 { // 未找到可用 key
		return errors.New("no usable jwks keys") // 返回错误
	}

	c.mu.Lock()                         // 加写锁
	c.keysByKID = keys                  // 替换缓存
	c.expiresAt = time.Now().Add(c.ttl) // 更新过期时间
	c.lastUpdate = time.Now()           // 更新最后时间
	c.mu.Unlock()                       // 释放写锁
	return nil                          // 成功
}

func parseRoles(claims map[string]any) []string { // 解析角色
	var roles []string                // 角色集合
	appendRole := func(role string) { // 追加角色
		role = strings.TrimSpace(role) // 清理角色
		if role == "" {                // 空角色
			return // 跳过
		}
		for _, existing := range roles { // 去重
			if existing == role { // 已存在
				return // 跳过
			}
		}
		roles = append(roles, role) // 追加
	}

	for _, key := range []string{"roles", "role"} { // 支持的字段
		if v, ok := claims[key]; ok { // 字段存在
			switch t := v.(type) { // 类型判断
			case []string: // 字符串列表
				for _, role := range t { // 遍历
					appendRole(role) // 追加
				}
			case []any: // 任意列表
				for _, role := range t { // 遍历
					appendRole(fmt.Sprint(role)) // 转换追加
				}
			case string: // 空格分隔
				for _, role := range strings.Fields(t) { // 拆分
					appendRole(role) // 追加
				}
			default: // 其他类型
				appendRole(fmt.Sprint(t)) // 兜底追加
			}
		}
	}

	if v, ok := claims["scp"]; ok { // 解析 scope
		if s, ok := v.(string); ok { // 字符串 scope
			for _, scope := range strings.Fields(s) { // 拆分 scope
				appendRole(scope) // 追加
			}
		}
	}

	return roles // 返回角色
}
