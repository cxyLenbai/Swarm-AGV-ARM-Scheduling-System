package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"swarm-agv-arm-scheduling-system/shared/config"
	"swarm-agv-arm-scheduling-system/shared/metricsx"
)

type Client struct {
	baseURL  string
	timeout  time.Duration
	retryMax int
	http     *http.Client
	breaker  *circuitBreaker
}

type PredictRequest struct {
	TenantID string             `json:"tenant_id"`
	Horizon  int                `json:"horizon_seconds"`
	Zones    []PredictZoneInput `json:"zones"`
	Signals  map[string]float64 `json:"signals,omitempty"`
}

type PredictZoneInput struct {
	ZoneID          string  `json:"zone_id"`
	CongestionIndex float64 `json:"congestion_index"`
	AvgSpeed        float64 `json:"avg_speed"`
	QueueLength     float64 `json:"queue_length"`
}

type PredictResponse struct {
	Predictions []ZonePrediction `json:"predictions"`
}

type ZonePrediction struct {
	ZoneID          string  `json:"zone_id"`
	HorizonSeconds  int     `json:"horizon_seconds"`
	Risk            float64 `json:"risk"`
	Confidence      float64 `json:"confidence"`
	SuggestedAction string  `json:"suggested_action,omitempty"`
}

func New(cfg config.Config) (*Client, error) {
	if cfg.AIServiceURL == "" {
		return nil, errors.New("AI_SERVICE_URL is required")
	}
	timeout := time.Duration(cfg.AIPredictTimeout) * time.Millisecond
	return &Client{
		baseURL:  cfg.AIServiceURL,
		timeout:  timeout,
		retryMax: cfg.AIPredictRetry,
		http:     &http.Client{Timeout: timeout},
		breaker:  newCircuitBreaker(5, 30*time.Second),
	}, nil
}

func (c *Client) PredictHotspots(ctx context.Context, req PredictRequest) (PredictResponse, error) {
	if c == nil || c.http == nil {
		return PredictResponse{}, errors.New("ai client not initialized")
	}
	if c.breaker.Open() {
		return PredictResponse{}, errors.New("ai circuit open")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return PredictResponse{}, err
	}

	start := time.Now()
	var lastErr error
	for attempt := 0; attempt <= c.retryMax; attempt++ {
		reqHTTP, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/congestion/predict", bytes.NewReader(body))
		if err != nil {
			return PredictResponse{}, err
		}
		reqHTTP.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(reqHTTP)
		if err != nil {
			lastErr = err
			c.breaker.Fail()
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 500 {
			lastErr = errors.New("ai service error")
			c.breaker.Fail()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			metricsx.IncAIPredictFailure()
			return PredictResponse{}, errors.New("ai request failed")
		}
		var out PredictResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			c.breaker.Fail()
			metricsx.IncAIPredictFailure()
			return PredictResponse{}, err
		}
		c.breaker.Success()
		metricsx.IncAIPredictSuccess()
		metricsx.ObserveAIPredictLatency(time.Since(start))
		return out, nil
	}
	if lastErr == nil {
		lastErr = errors.New("ai request failed")
	}
	metricsx.IncAIPredictFailure()
	return PredictResponse{}, lastErr
}

type circuitBreaker struct {
	mu            sync.Mutex
	failures      int
	openUntil     time.Time
	threshold     int
	resetDuration time.Duration
}

func newCircuitBreaker(threshold int, reset time.Duration) *circuitBreaker {
	return &circuitBreaker{threshold: threshold, resetDuration: reset}
}

func (b *circuitBreaker) Open() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openUntil.IsZero() {
		return false
	}
	if time.Now().After(b.openUntil) {
		b.openUntil = time.Time{}
		b.failures = 0
		return false
	}
	return true
}

func (b *circuitBreaker) Fail() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.failures >= b.threshold {
		b.openUntil = time.Now().Add(b.resetDuration)
	}
}

func (b *circuitBreaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.openUntil = time.Time{}
}
