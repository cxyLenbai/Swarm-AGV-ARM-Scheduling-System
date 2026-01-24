package rmf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

type TaskRequest struct {
	TaskID   string         `json:"task_id"`
	TaskType string         `json:"task_type"`
	Payload  map[string]any `json:"payload,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
}

type TaskResponse struct {
	TaskID  string `json:"task_id"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func NewClient(baseURL string, token string, timeout time.Duration) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("rmf api url required")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: timeout},
	}, nil
}

func (c *Client) CreateTask(ctx context.Context, req TaskRequest) (TaskResponse, error) {
	return c.doJSON(ctx, http.MethodPost, "/tasks", req)
}

func (c *Client) CancelTask(ctx context.Context, taskID string) (TaskResponse, error) {
	return c.doJSON(ctx, http.MethodPost, "/tasks/"+taskID+"/cancel", nil)
}

func (c *Client) doJSON(ctx context.Context, method string, path string, body any) (TaskResponse, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return TaskResponse{}, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return TaskResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return TaskResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return TaskResponse{}, errors.New("rmf request failed")
	}
	var out TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return TaskResponse{}, err
	}
	return out, nil
}
