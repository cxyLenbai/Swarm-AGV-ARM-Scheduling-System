package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Problem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type Config struct {
	Env              string
	ServiceName      string
	HTTPPort         int
	LogLevel         string
	ConfigPath       string
	RequestTimeoutMS int
	RequestTimeout   time.Duration
	OIDCIssuer       string
	OIDCAudience     string
	OIDCJWKSURL      string
	JWKSTTLSeconds   int
	JWTClockSkewSec  int
}

func Load(serviceNameDefault string, httpPortDefault int) (Config, []Problem) {
	envRaw := strings.TrimSpace(os.Getenv("ENV"))
	cfg := Config{
		Env:              envRaw,
		ServiceName:      serviceNameDefault,
		HTTPPort:         httpPortDefault,
		LogLevel:         "info",
		ConfigPath:       strings.TrimSpace(os.Getenv("CONFIG_PATH")),
		RequestTimeoutMS: 30000,
		OIDCIssuer:       strings.TrimSpace(os.Getenv("OIDC_ISSUER")),
		OIDCAudience:     strings.TrimSpace(os.Getenv("OIDC_AUDIENCE")),
		OIDCJWKSURL:      strings.TrimSpace(os.Getenv("OIDC_JWKS_URL")),
		JWKSTTLSeconds:   300,
		JWTClockSkewSec:  60,
	}

	problems := make([]Problem, 0, 4)
	envProvided := envRaw != ""

	if repoRoot, ok := findRepoRoot(); ok && cfg.Env != "" && cfg.ConfigPath == "" {
		cfg.ConfigPath = filepath.Join(repoRoot, "backend", "configs", cfg.Env+".json")
	}

	if fileData, fileProblems, ok := loadConfigFile(cfg.ConfigPath, strings.TrimSpace(os.Getenv("CONFIG_PATH")) != ""); ok {
		problems = append(problems, fileProblems...)
		if fileEnv, ok := readStringKey(fileData, "ENV"); ok && strings.TrimSpace(fileEnv) != "" {
			envProvided = true
		}
		applyConfigMap(&cfg, fileData, &problems)
	} else {
		problems = append(problems, fileProblems...)
	}

	applyEnv(&cfg, &problems)

	if cfg.Env == "" {
		cfg.Env = "dev"
	}
	if !envProvided {
		problems = append(problems, Problem{Field: "ENV", Message: "ENV is required"})
	}
	if cfg.HTTPPort <= 0 || cfg.HTTPPort > 65535 {
		problems = append(problems, Problem{Field: "HTTP_PORT", Message: "HTTP_PORT must be 1-65535"})
		cfg.HTTPPort = httpPortDefault
	}
	if cfg.RequestTimeoutMS <= 0 {
		problems = append(problems, Problem{Field: "REQUEST_TIMEOUT_MS", Message: "REQUEST_TIMEOUT_MS must be > 0"})
		cfg.RequestTimeoutMS = 30000
	}
	cfg.RequestTimeout = time.Duration(cfg.RequestTimeoutMS) * time.Millisecond
	if cfg.JWKSTTLSeconds <= 0 {
		problems = append(problems, Problem{Field: "JWKS_CACHE_TTL_SECONDS", Message: "JWKS_CACHE_TTL_SECONDS must be > 0"})
		cfg.JWKSTTLSeconds = 300
	}
	if cfg.JWTClockSkewSec < 0 {
		problems = append(problems, Problem{Field: "JWT_CLOCK_SKEW_SECONDS", Message: "JWT_CLOCK_SKEW_SECONDS must be >= 0"})
		cfg.JWTClockSkewSec = 60
	}

	return cfg, problems
}

func findRepoRoot() (string, bool) {
	start, err := os.Getwd()
	if err != nil {
		return "", false
	}
	dir := start
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, "backend", "configs")
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

func loadConfigFile(path string, explicit bool) (map[string]any, []Problem, bool) {
	if strings.TrimSpace(path) == "" {
		return nil, nil, false
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if explicit && !errors.Is(err, os.ErrNotExist) {
			return nil, []Problem{{Field: "CONFIG_PATH", Message: fmt.Sprintf("failed to read config file: %v", err)}}, false
		}
		if explicit && errors.Is(err, os.ErrNotExist) {
			return nil, []Problem{{Field: "CONFIG_PATH", Message: "config file not found"}}, false
		}
		return nil, nil, false
	}

	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, []Problem{{Field: "CONFIG_PATH", Message: fmt.Sprintf("invalid json: %v", err)}}, false
	}
	return raw, nil, true
}

func applyEnv(cfg *Config, problems *[]Problem) {
	if v := strings.TrimSpace(os.Getenv("SERVICE_NAME")); v != "" {
		cfg.ServiceName = v
	}

	portRaw := strings.TrimSpace(os.Getenv("HTTP_PORT"))
	if portRaw == "" {
		portRaw = strings.TrimSpace(os.Getenv("PORT"))
	}
	if portRaw != "" {
		if p, err := strconv.Atoi(portRaw); err != nil || p <= 0 || p > 65535 {
			*problems = append(*problems, Problem{Field: "HTTP_PORT", Message: "HTTP_PORT must be 1-65535"})
		} else {
			cfg.HTTPPort = p
		}
	}

	if v := strings.TrimSpace(os.Getenv("LOG_LEVEL")); v != "" {
		cfg.LogLevel = v
	}

	if v := strings.TrimSpace(os.Getenv("REQUEST_TIMEOUT_MS")); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil {
			*problems = append(*problems, Problem{Field: "REQUEST_TIMEOUT_MS", Message: "REQUEST_TIMEOUT_MS must be an integer"})
		} else {
			cfg.RequestTimeoutMS = ms
		}
	}

	if v := strings.TrimSpace(os.Getenv("OIDC_ISSUER")); v != "" {
		cfg.OIDCIssuer = v
	}
	if v := strings.TrimSpace(os.Getenv("OIDC_AUDIENCE")); v != "" {
		cfg.OIDCAudience = v
	}
	if v := strings.TrimSpace(os.Getenv("OIDC_JWKS_URL")); v != "" {
		cfg.OIDCJWKSURL = v
	}
	if v := strings.TrimSpace(os.Getenv("JWKS_CACHE_TTL_SECONDS")); v != "" {
		sec, err := strconv.Atoi(v)
		if err != nil {
			*problems = append(*problems, Problem{Field: "JWKS_CACHE_TTL_SECONDS", Message: "JWKS_CACHE_TTL_SECONDS must be an integer"})
		} else {
			cfg.JWKSTTLSeconds = sec
		}
	}
	if v := strings.TrimSpace(os.Getenv("JWT_CLOCK_SKEW_SECONDS")); v != "" {
		sec, err := strconv.Atoi(v)
		if err != nil {
			*problems = append(*problems, Problem{Field: "JWT_CLOCK_SKEW_SECONDS", Message: "JWT_CLOCK_SKEW_SECONDS must be an integer"})
		} else {
			cfg.JWTClockSkewSec = sec
		}
	}
}

func applyConfigMap(cfg *Config, raw map[string]any, problems *[]Problem) {
	for k, v := range raw {
		switch strings.ToUpper(strings.TrimSpace(k)) {
		case "ENV":
			if s, ok := v.(string); ok {
				cfg.Env = strings.TrimSpace(s)
			}
		case "SERVICE_NAME":
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				cfg.ServiceName = strings.TrimSpace(s)
			}
		case "HTTP_PORT":
			p, ok := asInt(v)
			if !ok || p <= 0 || p > 65535 {
				*problems = append(*problems, Problem{Field: "HTTP_PORT", Message: "HTTP_PORT must be 1-65535"})
			} else {
				cfg.HTTPPort = p
			}
		case "LOG_LEVEL":
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				cfg.LogLevel = strings.TrimSpace(s)
			}
		case "REQUEST_TIMEOUT_MS":
			ms, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "REQUEST_TIMEOUT_MS", Message: "REQUEST_TIMEOUT_MS must be an integer"})
			} else {
				cfg.RequestTimeoutMS = ms
			}
		case "OIDC_ISSUER":
			if s, ok := v.(string); ok {
				cfg.OIDCIssuer = strings.TrimSpace(s)
			}
		case "OIDC_AUDIENCE":
			if s, ok := v.(string); ok {
				cfg.OIDCAudience = strings.TrimSpace(s)
			}
		case "OIDC_JWKS_URL":
			if s, ok := v.(string); ok {
				cfg.OIDCJWKSURL = strings.TrimSpace(s)
			}
		case "JWKS_CACHE_TTL_SECONDS":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "JWKS_CACHE_TTL_SECONDS", Message: "JWKS_CACHE_TTL_SECONDS must be an integer"})
			} else {
				cfg.JWKSTTLSeconds = sec
			}
		case "JWT_CLOCK_SKEW_SECONDS":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "JWT_CLOCK_SKEW_SECONDS", Message: "JWT_CLOCK_SKEW_SECONDS must be an integer"})
			} else {
				cfg.JWTClockSkewSec = sec
			}
		}
	}
}

func readStringKey(raw map[string]any, key string) (string, bool) {
	for k, v := range raw {
		if strings.EqualFold(strings.TrimSpace(k), key) {
			s, ok := v.(string)
			return s, ok
		}
	}
	return "", false
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	case json.Number:
		i, err := t.Int64()
		return int(i), err == nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(t))
		return i, err == nil
	default:
		return 0, false
	}
}
