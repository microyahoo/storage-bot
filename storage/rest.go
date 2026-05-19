package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RESTBackend calls a generic REST API to query a non-Ceph storage system.
// The exact endpoints are configurable; the API is expected to return JSON.
type RESTBackend struct {
	name     string
	baseURL  string
	apiKey   string
	client   *http.Client
	endpoints RESTEndpoints
}

// RESTEndpoints configures which URL paths to call for each operation.
// Paths are appended to BaseURL. Leave empty to skip that operation.
type RESTEndpoints struct {
	// ClusterInfo endpoint, e.g. "/api/v1/cluster"
	ClusterInfo string `yaml:"cluster_info"`
	// DirUsage endpoint template, e.g. "/api/v1/usage?path=%s"
	// Use %s as a placeholder for the path argument.
	DirUsage string `yaml:"dir_usage"`
	// HealthCheck endpoint, e.g. "/api/v1/health"
	HealthCheck string `yaml:"health_check"`
}

func NewRESTBackend(name, baseURL, apiKey string, endpoints RESTEndpoints) *RESTBackend {
	return &RESTBackend{
		name:      name,
		baseURL:   baseURL,
		apiKey:    apiKey,
		endpoints: endpoints,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (r *RESTBackend) Type() string { return "rest" }

func (r *RESTBackend) ClusterInfo(ctx context.Context) (string, error) {
	if r.endpoints.ClusterInfo == "" {
		return fmt.Sprintf("[%s] cluster_info endpoint not configured", r.name), nil
	}
	return r.get(ctx, r.endpoints.ClusterInfo)
}

func (r *RESTBackend) DirUsage(ctx context.Context, path string) (string, error) {
	if r.endpoints.DirUsage == "" {
		return fmt.Sprintf("[%s] dir_usage endpoint not configured", r.name), nil
	}
	endpoint := fmt.Sprintf(r.endpoints.DirUsage, path)
	return r.get(ctx, endpoint)
}

func (r *RESTBackend) HealthCheck(ctx context.Context) (string, error) {
	if r.endpoints.HealthCheck == "" {
		return fmt.Sprintf("[%s] health_check endpoint not configured", r.name), nil
	}
	return r.get(ctx, r.endpoints.HealthCheck)
}

func (r *RESTBackend) get(ctx context.Context, path string) (string, error) {
	url := r.baseURL + path
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("GET %s returned %d: %s", url, resp.StatusCode, string(body))
	}

	// Pretty-print JSON if possible
	var pretty bytes.Buffer
	if json.Indent(&pretty, body, "", "  ") == nil {
		return pretty.String(), nil
	}
	return string(body), nil
}
