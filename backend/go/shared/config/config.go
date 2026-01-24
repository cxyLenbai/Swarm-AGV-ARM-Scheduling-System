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
	Env                   string
	ServiceName           string
	HTTPPort              int
	LogLevel              string
	ConfigPath            string
	RequestTimeoutMS      int
	RequestTimeout        time.Duration
	OIDCIssuer            string
	OIDCAudience          string
	OIDCJWKSURL           string
	JWKSTTLSeconds        int
	JWTClockSkewSec       int
	DatabaseURL           string
	DBMaxConns            int
	DBMinConns            int
	DBConnMaxIdleSec      int
	DBConnMaxLifeSec      int
	AuditEnabled          bool
	KafkaBrokers          []string
	KafkaClientID         string
	KafkaGroupID          string
	KafkaRetryMax         int
	KafkaWriteMS          int
	RedisAddr             string
	RedisPassword         string
	RedisDB               int
	AsynqRedisAddr        string
	AsynqRedisPass        string
	AsynqRedisDB          int
	AsynqQueue            string
	AsynqConcurrency      int
	AsynqEnabled          bool
	OutboxScanSec         int
	OutboxBatchSize       int
	OutboxMaxAttempts     int
	InfluxURL             string
	InfluxToken           string
	InfluxOrg             string
	InfluxBucket          string
	InfluxTimeoutMS       int
	AIServiceURL          string
	AIPredictTimeout      int
	AIPredictRetry        int
	AIEnabled             bool
	RMFAPIURL             string
	RMFAPIToken           string
	RMFEnabled            bool
	CongestionL1          float64
	CongestionL2          float64
	CongestionL3          float64
	CongestionCooldownSec int
	OtelEnabled           bool
	OtelEndpoint          string
	OtelInsecure          bool
	OtelSampleRatio       float64
}

func Load(serviceNameDefault string, httpPortDefault int) (Config, []Problem) {
	envRaw := strings.TrimSpace(os.Getenv("ENV"))
	cfg := Config{
		Env:                   envRaw,
		ServiceName:           serviceNameDefault,
		HTTPPort:              httpPortDefault,
		LogLevel:              "info",
		ConfigPath:            strings.TrimSpace(os.Getenv("CONFIG_PATH")),
		RequestTimeoutMS:      30000,
		OIDCIssuer:            strings.TrimSpace(os.Getenv("OIDC_ISSUER")),
		OIDCAudience:          strings.TrimSpace(os.Getenv("OIDC_AUDIENCE")),
		OIDCJWKSURL:           strings.TrimSpace(os.Getenv("OIDC_JWKS_URL")),
		JWKSTTLSeconds:        300,
		JWTClockSkewSec:       60,
		DatabaseURL:           strings.TrimSpace(os.Getenv("DATABASE_URL")),
		DBMaxConns:            10,
		DBMinConns:            1,
		DBConnMaxIdleSec:      300,
		DBConnMaxLifeSec:      1800,
		AuditEnabled:          false,
		KafkaBrokers:          nil,
		KafkaClientID:         "",
		KafkaGroupID:          "",
		KafkaRetryMax:         5,
		KafkaWriteMS:          5000,
		RedisAddr:             "",
		RedisPassword:         "",
		RedisDB:               0,
		AsynqRedisAddr:        "",
		AsynqRedisPass:        "",
		AsynqRedisDB:          0,
		AsynqQueue:            "default",
		AsynqConcurrency:      10,
		AsynqEnabled:          false,
		OutboxScanSec:         5,
		OutboxBatchSize:       50,
		OutboxMaxAttempts:     20,
		InfluxURL:             "",
		InfluxToken:           "",
		InfluxOrg:             "",
		InfluxBucket:          "",
		InfluxTimeoutMS:       5000,
		AIServiceURL:          "",
		AIPredictTimeout:      3000,
		AIPredictRetry:        2,
		AIEnabled:             false,
		RMFAPIURL:             "",
		RMFAPIToken:           "",
		RMFEnabled:            false,
		CongestionL1:          0.5,
		CongestionL2:          0.7,
		CongestionL3:          0.85,
		CongestionCooldownSec: 120,
		OtelEnabled:           false,
		OtelEndpoint:          "",
		OtelInsecure:          true,
		OtelSampleRatio:       1.0,
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

	// If issuer is set and no explicit JWKS URL is provided, default to issuer/.well-known/jwks.json.
	if cfg.OIDCIssuer != "" && strings.TrimSpace(cfg.OIDCJWKSURL) == "" {
		cfg.OIDCJWKSURL = strings.TrimRight(cfg.OIDCIssuer, "/") + "/.well-known/jwks.json"
	}

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
	if cfg.DBMaxConns <= 0 {
		problems = append(problems, Problem{Field: "DB_MAX_CONNS", Message: "DB_MAX_CONNS must be > 0"})
		cfg.DBMaxConns = 10
	}
	if cfg.DBMinConns < 0 {
		problems = append(problems, Problem{Field: "DB_MIN_CONNS", Message: "DB_MIN_CONNS must be >= 0"})
		cfg.DBMinConns = 1
	}
	if cfg.DBMinConns > cfg.DBMaxConns {
		problems = append(problems, Problem{Field: "DB_MIN_CONNS", Message: "DB_MIN_CONNS must be <= DB_MAX_CONNS"})
		cfg.DBMinConns = cfg.DBMaxConns
	}
	if cfg.DBConnMaxIdleSec <= 0 {
		problems = append(problems, Problem{Field: "DB_CONN_MAX_IDLE_SECONDS", Message: "DB_CONN_MAX_IDLE_SECONDS must be > 0"})
		cfg.DBConnMaxIdleSec = 300
	}
	if cfg.DBConnMaxLifeSec <= 0 {
		problems = append(problems, Problem{Field: "DB_CONN_MAX_LIFETIME_SECONDS", Message: "DB_CONN_MAX_LIFETIME_SECONDS must be > 0"})
		cfg.DBConnMaxLifeSec = 1800
	}
	if cfg.KafkaRetryMax < 0 {
		problems = append(problems, Problem{Field: "KAFKA_RETRY_MAX", Message: "KAFKA_RETRY_MAX must be >= 0"})
		cfg.KafkaRetryMax = 5
	}
	if cfg.KafkaWriteMS <= 0 {
		problems = append(problems, Problem{Field: "KAFKA_WRITE_TIMEOUT_MS", Message: "KAFKA_WRITE_TIMEOUT_MS must be > 0"})
		cfg.KafkaWriteMS = 5000
	}
	if cfg.RedisDB < 0 {
		problems = append(problems, Problem{Field: "REDIS_DB", Message: "REDIS_DB must be >= 0"})
		cfg.RedisDB = 0
	}
	if cfg.AsynqRedisDB < 0 {
		problems = append(problems, Problem{Field: "ASYNQ_REDIS_DB", Message: "ASYNQ_REDIS_DB must be >= 0"})
		cfg.AsynqRedisDB = 0
	}
	if cfg.AsynqConcurrency <= 0 {
		problems = append(problems, Problem{Field: "ASYNQ_CONCURRENCY", Message: "ASYNQ_CONCURRENCY must be > 0"})
		cfg.AsynqConcurrency = 10
	}
	if cfg.OutboxScanSec <= 0 {
		problems = append(problems, Problem{Field: "OUTBOX_SCAN_INTERVAL_SECONDS", Message: "OUTBOX_SCAN_INTERVAL_SECONDS must be > 0"})
		cfg.OutboxScanSec = 5
	}
	if cfg.OutboxBatchSize <= 0 {
		problems = append(problems, Problem{Field: "OUTBOX_BATCH_SIZE", Message: "OUTBOX_BATCH_SIZE must be > 0"})
		cfg.OutboxBatchSize = 50
	}
	if cfg.OutboxMaxAttempts <= 0 {
		problems = append(problems, Problem{Field: "OUTBOX_MAX_ATTEMPTS", Message: "OUTBOX_MAX_ATTEMPTS must be > 0"})
		cfg.OutboxMaxAttempts = 20
	}
	if cfg.InfluxTimeoutMS <= 0 {
		problems = append(problems, Problem{Field: "INFLUX_TIMEOUT_MS", Message: "INFLUX_TIMEOUT_MS must be > 0"})
		cfg.InfluxTimeoutMS = 5000
	}
	if cfg.AIPredictTimeout <= 0 {
		problems = append(problems, Problem{Field: "AI_PREDICT_TIMEOUT_MS", Message: "AI_PREDICT_TIMEOUT_MS must be > 0"})
		cfg.AIPredictTimeout = 3000
	}
	if cfg.AIPredictRetry < 0 {
		problems = append(problems, Problem{Field: "AI_PREDICT_RETRY_MAX", Message: "AI_PREDICT_RETRY_MAX must be >= 0"})
		cfg.AIPredictRetry = 2
	}
	if cfg.CongestionCooldownSec <= 0 {
		problems = append(problems, Problem{Field: "CONGESTION_COOLDOWN_SECONDS", Message: "CONGESTION_COOLDOWN_SECONDS must be > 0"})
		cfg.CongestionCooldownSec = 120
	}
	if cfg.OtelSampleRatio < 0 || cfg.OtelSampleRatio > 1 {
		problems = append(problems, Problem{Field: "OTEL_SAMPLE_RATIO", Message: "OTEL_SAMPLE_RATIO must be 0-1"})
		cfg.OtelSampleRatio = 1.0
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
	if v := strings.TrimSpace(os.Getenv("DATABASE_URL")); v != "" {
		cfg.DatabaseURL = v
	}
	if v := strings.TrimSpace(os.Getenv("DB_MAX_CONNS")); v != "" {
		sec, err := strconv.Atoi(v)
		if err != nil {
			*problems = append(*problems, Problem{Field: "DB_MAX_CONNS", Message: "DB_MAX_CONNS must be an integer"})
		} else {
			cfg.DBMaxConns = sec
		}
	}
	if v := strings.TrimSpace(os.Getenv("DB_MIN_CONNS")); v != "" {
		sec, err := strconv.Atoi(v)
		if err != nil {
			*problems = append(*problems, Problem{Field: "DB_MIN_CONNS", Message: "DB_MIN_CONNS must be an integer"})
		} else {
			cfg.DBMinConns = sec
		}
	}
	if v := strings.TrimSpace(os.Getenv("DB_CONN_MAX_IDLE_SECONDS")); v != "" {
		sec, err := strconv.Atoi(v)
		if err != nil {
			*problems = append(*problems, Problem{Field: "DB_CONN_MAX_IDLE_SECONDS", Message: "DB_CONN_MAX_IDLE_SECONDS must be an integer"})
		} else {
			cfg.DBConnMaxIdleSec = sec
		}
	}
	if v := strings.TrimSpace(os.Getenv("DB_CONN_MAX_LIFETIME_SECONDS")); v != "" {
		sec, err := strconv.Atoi(v)
		if err != nil {
			*problems = append(*problems, Problem{Field: "DB_CONN_MAX_LIFETIME_SECONDS", Message: "DB_CONN_MAX_LIFETIME_SECONDS must be an integer"})
		} else {
			cfg.DBConnMaxLifeSec = sec
		}
	}
	if v := strings.TrimSpace(os.Getenv("AUDIT_ENABLED")); v != "" {
		if b, ok := asBool(v); ok {
			cfg.AuditEnabled = b
		} else {
			*problems = append(*problems, Problem{Field: "AUDIT_ENABLED", Message: "AUDIT_ENABLED must be a boolean"})
		}
	}
	if v := strings.TrimSpace(os.Getenv("KAFKA_BROKERS")); v != "" {
		cfg.KafkaBrokers = parseCSV(v)
	}
	if v := strings.TrimSpace(os.Getenv("KAFKA_CLIENT_ID")); v != "" {
		cfg.KafkaClientID = v
	}
	if v := strings.TrimSpace(os.Getenv("KAFKA_CONSUMER_GROUP")); v != "" {
		cfg.KafkaGroupID = v
	}
	if v := strings.TrimSpace(os.Getenv("KAFKA_RETRY_MAX")); v != "" {
		if n, err := strconv.Atoi(v); err != nil {
			*problems = append(*problems, Problem{Field: "KAFKA_RETRY_MAX", Message: "KAFKA_RETRY_MAX must be an integer"})
		} else {
			cfg.KafkaRetryMax = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("KAFKA_WRITE_TIMEOUT_MS")); v != "" {
		if n, err := strconv.Atoi(v); err != nil {
			*problems = append(*problems, Problem{Field: "KAFKA_WRITE_TIMEOUT_MS", Message: "KAFKA_WRITE_TIMEOUT_MS must be an integer"})
		} else {
			cfg.KafkaWriteMS = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("REDIS_ADDR")); v != "" {
		cfg.RedisAddr = v
	}
	if v := strings.TrimSpace(os.Getenv("REDIS_PASSWORD")); v != "" {
		cfg.RedisPassword = v
	}
	if v := strings.TrimSpace(os.Getenv("REDIS_DB")); v != "" {
		if n, err := strconv.Atoi(v); err != nil {
			*problems = append(*problems, Problem{Field: "REDIS_DB", Message: "REDIS_DB must be an integer"})
		} else {
			cfg.RedisDB = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("ASYNQ_REDIS_ADDR")); v != "" {
		cfg.AsynqRedisAddr = v
	}
	if v := strings.TrimSpace(os.Getenv("ASYNQ_REDIS_PASSWORD")); v != "" {
		cfg.AsynqRedisPass = v
	}
	if v := strings.TrimSpace(os.Getenv("ASYNQ_REDIS_DB")); v != "" {
		if n, err := strconv.Atoi(v); err != nil {
			*problems = append(*problems, Problem{Field: "ASYNQ_REDIS_DB", Message: "ASYNQ_REDIS_DB must be an integer"})
		} else {
			cfg.AsynqRedisDB = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("ASYNQ_QUEUE")); v != "" {
		cfg.AsynqQueue = v
	}
	if v := strings.TrimSpace(os.Getenv("ASYNQ_CONCURRENCY")); v != "" {
		if n, err := strconv.Atoi(v); err != nil {
			*problems = append(*problems, Problem{Field: "ASYNQ_CONCURRENCY", Message: "ASYNQ_CONCURRENCY must be an integer"})
		} else {
			cfg.AsynqConcurrency = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("ASYNQ_ENABLED")); v != "" {
		if b, ok := asBool(v); ok {
			cfg.AsynqEnabled = b
		} else {
			*problems = append(*problems, Problem{Field: "ASYNQ_ENABLED", Message: "ASYNQ_ENABLED must be a boolean"})
		}
	}
	if v := strings.TrimSpace(os.Getenv("OUTBOX_SCAN_INTERVAL_SECONDS")); v != "" {
		if n, err := strconv.Atoi(v); err != nil {
			*problems = append(*problems, Problem{Field: "OUTBOX_SCAN_INTERVAL_SECONDS", Message: "OUTBOX_SCAN_INTERVAL_SECONDS must be an integer"})
		} else {
			cfg.OutboxScanSec = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("OUTBOX_BATCH_SIZE")); v != "" {
		if n, err := strconv.Atoi(v); err != nil {
			*problems = append(*problems, Problem{Field: "OUTBOX_BATCH_SIZE", Message: "OUTBOX_BATCH_SIZE must be an integer"})
		} else {
			cfg.OutboxBatchSize = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("OUTBOX_MAX_ATTEMPTS")); v != "" {
		if n, err := strconv.Atoi(v); err != nil {
			*problems = append(*problems, Problem{Field: "OUTBOX_MAX_ATTEMPTS", Message: "OUTBOX_MAX_ATTEMPTS must be an integer"})
		} else {
			cfg.OutboxMaxAttempts = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("INFLUX_URL")); v != "" {
		cfg.InfluxURL = v
	}
	if v := strings.TrimSpace(os.Getenv("INFLUX_TOKEN")); v != "" {
		cfg.InfluxToken = v
	}
	if v := strings.TrimSpace(os.Getenv("INFLUX_ORG")); v != "" {
		cfg.InfluxOrg = v
	}
	if v := strings.TrimSpace(os.Getenv("INFLUX_BUCKET")); v != "" {
		cfg.InfluxBucket = v
	}
	if v := strings.TrimSpace(os.Getenv("INFLUX_TIMEOUT_MS")); v != "" {
		if n, err := strconv.Atoi(v); err != nil {
			*problems = append(*problems, Problem{Field: "INFLUX_TIMEOUT_MS", Message: "INFLUX_TIMEOUT_MS must be an integer"})
		} else {
			cfg.InfluxTimeoutMS = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("AI_SERVICE_URL")); v != "" {
		cfg.AIServiceURL = v
	}
	if v := strings.TrimSpace(os.Getenv("AI_PREDICT_TIMEOUT_MS")); v != "" {
		if n, err := strconv.Atoi(v); err != nil {
			*problems = append(*problems, Problem{Field: "AI_PREDICT_TIMEOUT_MS", Message: "AI_PREDICT_TIMEOUT_MS must be an integer"})
		} else {
			cfg.AIPredictTimeout = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("AI_PREDICT_RETRY_MAX")); v != "" {
		if n, err := strconv.Atoi(v); err != nil {
			*problems = append(*problems, Problem{Field: "AI_PREDICT_RETRY_MAX", Message: "AI_PREDICT_RETRY_MAX must be an integer"})
		} else {
			cfg.AIPredictRetry = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("AI_ENABLED")); v != "" {
		if b, ok := asBool(v); ok {
			cfg.AIEnabled = b
		} else {
			*problems = append(*problems, Problem{Field: "AI_ENABLED", Message: "AI_ENABLED must be a boolean"})
		}
	}
	if v := strings.TrimSpace(os.Getenv("RMF_API_URL")); v != "" {
		cfg.RMFAPIURL = v
	}
	if v := strings.TrimSpace(os.Getenv("RMF_API_TOKEN")); v != "" {
		cfg.RMFAPIToken = v
	}
	if v := strings.TrimSpace(os.Getenv("RMF_ENABLED")); v != "" {
		if b, ok := asBool(v); ok {
			cfg.RMFEnabled = b
		} else {
			*problems = append(*problems, Problem{Field: "RMF_ENABLED", Message: "RMF_ENABLED must be a boolean"})
		}
	}
	if v := strings.TrimSpace(os.Getenv("CONGESTION_L1_THRESHOLD")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err != nil {
			*problems = append(*problems, Problem{Field: "CONGESTION_L1_THRESHOLD", Message: "CONGESTION_L1_THRESHOLD must be a number"})
		} else {
			cfg.CongestionL1 = f
		}
	}
	if v := strings.TrimSpace(os.Getenv("CONGESTION_L2_THRESHOLD")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err != nil {
			*problems = append(*problems, Problem{Field: "CONGESTION_L2_THRESHOLD", Message: "CONGESTION_L2_THRESHOLD must be a number"})
		} else {
			cfg.CongestionL2 = f
		}
	}
	if v := strings.TrimSpace(os.Getenv("CONGESTION_L3_THRESHOLD")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err != nil {
			*problems = append(*problems, Problem{Field: "CONGESTION_L3_THRESHOLD", Message: "CONGESTION_L3_THRESHOLD must be a number"})
		} else {
			cfg.CongestionL3 = f
		}
	}
	if v := strings.TrimSpace(os.Getenv("CONGESTION_COOLDOWN_SECONDS")); v != "" {
		if n, err := strconv.Atoi(v); err != nil {
			*problems = append(*problems, Problem{Field: "CONGESTION_COOLDOWN_SECONDS", Message: "CONGESTION_COOLDOWN_SECONDS must be an integer"})
		} else {
			cfg.CongestionCooldownSec = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("OTEL_ENABLED")); v != "" {
		if b, ok := asBool(v); ok {
			cfg.OtelEnabled = b
		} else {
			*problems = append(*problems, Problem{Field: "OTEL_ENABLED", Message: "OTEL_ENABLED must be a boolean"})
		}
	}
	if v := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")); v != "" {
		cfg.OtelEndpoint = v
	}
	if v := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); v != "" {
		if b, ok := asBool(v); ok {
			cfg.OtelInsecure = b
		} else {
			*problems = append(*problems, Problem{Field: "OTEL_EXPORTER_OTLP_INSECURE", Message: "OTEL_EXPORTER_OTLP_INSECURE must be a boolean"})
		}
	}
	if v := strings.TrimSpace(os.Getenv("OTEL_SAMPLE_RATIO")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err != nil {
			*problems = append(*problems, Problem{Field: "OTEL_SAMPLE_RATIO", Message: "OTEL_SAMPLE_RATIO must be a number"})
		} else {
			cfg.OtelSampleRatio = f
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
		case "DATABASE_URL":
			if s, ok := v.(string); ok {
				cfg.DatabaseURL = strings.TrimSpace(s)
			}
		case "DB_MAX_CONNS":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "DB_MAX_CONNS", Message: "DB_MAX_CONNS must be an integer"})
			} else {
				cfg.DBMaxConns = sec
			}
		case "DB_MIN_CONNS":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "DB_MIN_CONNS", Message: "DB_MIN_CONNS must be an integer"})
			} else {
				cfg.DBMinConns = sec
			}
		case "DB_CONN_MAX_IDLE_SECONDS":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "DB_CONN_MAX_IDLE_SECONDS", Message: "DB_CONN_MAX_IDLE_SECONDS must be an integer"})
			} else {
				cfg.DBConnMaxIdleSec = sec
			}
		case "DB_CONN_MAX_LIFETIME_SECONDS":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "DB_CONN_MAX_LIFETIME_SECONDS", Message: "DB_CONN_MAX_LIFETIME_SECONDS must be an integer"})
			} else {
				cfg.DBConnMaxLifeSec = sec
			}
		case "AUDIT_ENABLED":
			if s, ok := v.(string); ok {
				if b, ok := asBool(s); ok {
					cfg.AuditEnabled = b
				} else {
					*problems = append(*problems, Problem{Field: "AUDIT_ENABLED", Message: "AUDIT_ENABLED must be a boolean"})
				}
			} else if b, ok := v.(bool); ok {
				cfg.AuditEnabled = b
			} else {
				*problems = append(*problems, Problem{Field: "AUDIT_ENABLED", Message: "AUDIT_ENABLED must be a boolean"})
			}
		case "KAFKA_BROKERS":
			if s, ok := v.(string); ok {
				cfg.KafkaBrokers = parseCSV(s)
			} else if arr, ok := v.([]any); ok {
				cfg.KafkaBrokers = parseAnyCSV(arr)
			}
		case "KAFKA_CLIENT_ID":
			if s, ok := v.(string); ok {
				cfg.KafkaClientID = strings.TrimSpace(s)
			}
		case "KAFKA_CONSUMER_GROUP":
			if s, ok := v.(string); ok {
				cfg.KafkaGroupID = strings.TrimSpace(s)
			}
		case "KAFKA_RETRY_MAX":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "KAFKA_RETRY_MAX", Message: "KAFKA_RETRY_MAX must be an integer"})
			} else {
				cfg.KafkaRetryMax = sec
			}
		case "KAFKA_WRITE_TIMEOUT_MS":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "KAFKA_WRITE_TIMEOUT_MS", Message: "KAFKA_WRITE_TIMEOUT_MS must be an integer"})
			} else {
				cfg.KafkaWriteMS = sec
			}
		case "REDIS_ADDR":
			if s, ok := v.(string); ok {
				cfg.RedisAddr = strings.TrimSpace(s)
			}
		case "REDIS_PASSWORD":
			if s, ok := v.(string); ok {
				cfg.RedisPassword = s
			}
		case "REDIS_DB":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "REDIS_DB", Message: "REDIS_DB must be an integer"})
			} else {
				cfg.RedisDB = sec
			}
		case "ASYNQ_REDIS_ADDR":
			if s, ok := v.(string); ok {
				cfg.AsynqRedisAddr = strings.TrimSpace(s)
			}
		case "ASYNQ_REDIS_PASSWORD":
			if s, ok := v.(string); ok {
				cfg.AsynqRedisPass = s
			}
		case "ASYNQ_REDIS_DB":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "ASYNQ_REDIS_DB", Message: "ASYNQ_REDIS_DB must be an integer"})
			} else {
				cfg.AsynqRedisDB = sec
			}
		case "ASYNQ_QUEUE":
			if s, ok := v.(string); ok {
				cfg.AsynqQueue = strings.TrimSpace(s)
			}
		case "ASYNQ_CONCURRENCY":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "ASYNQ_CONCURRENCY", Message: "ASYNQ_CONCURRENCY must be an integer"})
			} else {
				cfg.AsynqConcurrency = sec
			}
		case "ASYNQ_ENABLED":
			if s, ok := v.(string); ok {
				if b, ok := asBool(s); ok {
					cfg.AsynqEnabled = b
				} else {
					*problems = append(*problems, Problem{Field: "ASYNQ_ENABLED", Message: "ASYNQ_ENABLED must be a boolean"})
				}
			} else if b, ok := v.(bool); ok {
				cfg.AsynqEnabled = b
			} else {
				*problems = append(*problems, Problem{Field: "ASYNQ_ENABLED", Message: "ASYNQ_ENABLED must be a boolean"})
			}
		case "OUTBOX_SCAN_INTERVAL_SECONDS":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "OUTBOX_SCAN_INTERVAL_SECONDS", Message: "OUTBOX_SCAN_INTERVAL_SECONDS must be an integer"})
			} else {
				cfg.OutboxScanSec = sec
			}
		case "OUTBOX_BATCH_SIZE":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "OUTBOX_BATCH_SIZE", Message: "OUTBOX_BATCH_SIZE must be an integer"})
			} else {
				cfg.OutboxBatchSize = sec
			}
		case "OUTBOX_MAX_ATTEMPTS":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "OUTBOX_MAX_ATTEMPTS", Message: "OUTBOX_MAX_ATTEMPTS must be an integer"})
			} else {
				cfg.OutboxMaxAttempts = sec
			}
		case "INFLUX_URL":
			if s, ok := v.(string); ok {
				cfg.InfluxURL = strings.TrimSpace(s)
			}
		case "INFLUX_TOKEN":
			if s, ok := v.(string); ok {
				cfg.InfluxToken = s
			}
		case "INFLUX_ORG":
			if s, ok := v.(string); ok {
				cfg.InfluxOrg = strings.TrimSpace(s)
			}
		case "INFLUX_BUCKET":
			if s, ok := v.(string); ok {
				cfg.InfluxBucket = strings.TrimSpace(s)
			}
		case "INFLUX_TIMEOUT_MS":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "INFLUX_TIMEOUT_MS", Message: "INFLUX_TIMEOUT_MS must be an integer"})
			} else {
				cfg.InfluxTimeoutMS = sec
			}
		case "AI_SERVICE_URL":
			if s, ok := v.(string); ok {
				cfg.AIServiceURL = strings.TrimSpace(s)
			}
		case "AI_PREDICT_TIMEOUT_MS":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "AI_PREDICT_TIMEOUT_MS", Message: "AI_PREDICT_TIMEOUT_MS must be an integer"})
			} else {
				cfg.AIPredictTimeout = sec
			}
		case "AI_PREDICT_RETRY_MAX":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "AI_PREDICT_RETRY_MAX", Message: "AI_PREDICT_RETRY_MAX must be an integer"})
			} else {
				cfg.AIPredictRetry = sec
			}
		case "AI_ENABLED":
			if s, ok := v.(string); ok {
				if b, ok := asBool(s); ok {
					cfg.AIEnabled = b
				} else {
					*problems = append(*problems, Problem{Field: "AI_ENABLED", Message: "AI_ENABLED must be a boolean"})
				}
			} else if b, ok := v.(bool); ok {
				cfg.AIEnabled = b
			} else {
				*problems = append(*problems, Problem{Field: "AI_ENABLED", Message: "AI_ENABLED must be a boolean"})
			}
		case "RMF_API_URL":
			if s, ok := v.(string); ok {
				cfg.RMFAPIURL = strings.TrimSpace(s)
			}
		case "RMF_API_TOKEN":
			if s, ok := v.(string); ok {
				cfg.RMFAPIToken = s
			}
		case "RMF_ENABLED":
			if s, ok := v.(string); ok {
				if b, ok := asBool(s); ok {
					cfg.RMFEnabled = b
				} else {
					*problems = append(*problems, Problem{Field: "RMF_ENABLED", Message: "RMF_ENABLED must be a boolean"})
				}
			} else if b, ok := v.(bool); ok {
				cfg.RMFEnabled = b
			} else {
				*problems = append(*problems, Problem{Field: "RMF_ENABLED", Message: "RMF_ENABLED must be a boolean"})
			}
		case "CONGESTION_L1_THRESHOLD":
			if f, ok := asFloat(v); ok {
				cfg.CongestionL1 = f
			} else {
				*problems = append(*problems, Problem{Field: "CONGESTION_L1_THRESHOLD", Message: "CONGESTION_L1_THRESHOLD must be a number"})
			}
		case "CONGESTION_L2_THRESHOLD":
			if f, ok := asFloat(v); ok {
				cfg.CongestionL2 = f
			} else {
				*problems = append(*problems, Problem{Field: "CONGESTION_L2_THRESHOLD", Message: "CONGESTION_L2_THRESHOLD must be a number"})
			}
		case "CONGESTION_L3_THRESHOLD":
			if f, ok := asFloat(v); ok {
				cfg.CongestionL3 = f
			} else {
				*problems = append(*problems, Problem{Field: "CONGESTION_L3_THRESHOLD", Message: "CONGESTION_L3_THRESHOLD must be a number"})
			}
		case "CONGESTION_COOLDOWN_SECONDS":
			sec, ok := asInt(v)
			if !ok {
				*problems = append(*problems, Problem{Field: "CONGESTION_COOLDOWN_SECONDS", Message: "CONGESTION_COOLDOWN_SECONDS must be an integer"})
			} else {
				cfg.CongestionCooldownSec = sec
			}
		case "OTEL_ENABLED":
			if s, ok := v.(string); ok {
				if b, ok := asBool(s); ok {
					cfg.OtelEnabled = b
				} else {
					*problems = append(*problems, Problem{Field: "OTEL_ENABLED", Message: "OTEL_ENABLED must be a boolean"})
				}
			} else if b, ok := v.(bool); ok {
				cfg.OtelEnabled = b
			} else {
				*problems = append(*problems, Problem{Field: "OTEL_ENABLED", Message: "OTEL_ENABLED must be a boolean"})
			}
		case "OTEL_EXPORTER_OTLP_ENDPOINT":
			if s, ok := v.(string); ok {
				cfg.OtelEndpoint = strings.TrimSpace(s)
			}
		case "OTEL_EXPORTER_OTLP_INSECURE":
			if s, ok := v.(string); ok {
				if b, ok := asBool(s); ok {
					cfg.OtelInsecure = b
				} else {
					*problems = append(*problems, Problem{Field: "OTEL_EXPORTER_OTLP_INSECURE", Message: "OTEL_EXPORTER_OTLP_INSECURE must be a boolean"})
				}
			} else if b, ok := v.(bool); ok {
				cfg.OtelInsecure = b
			} else {
				*problems = append(*problems, Problem{Field: "OTEL_EXPORTER_OTLP_INSECURE", Message: "OTEL_EXPORTER_OTLP_INSECURE must be a boolean"})
			}
		case "OTEL_SAMPLE_RATIO":
			if f, ok := asFloat(v); ok {
				cfg.OtelSampleRatio = f
			} else {
				*problems = append(*problems, Problem{Field: "OTEL_SAMPLE_RATIO", Message: "OTEL_SAMPLE_RATIO must be a number"})
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

func asBool(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "y":
		return true, true
	case "false", "0", "no", "n":
		return false, true
	default:
		return false, false
	}
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func parseCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseAnyCSV(raw []any) []string {
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}
