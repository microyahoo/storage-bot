package storage

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// YanrongBackend talks to the Yanrong cloud filesystem REST API.
//
// Auth flow (mirrors examples/yrfs.py):
//  1. password → md5 → md5 again → interleave the two hex digests char-by-char
//     into a 64-char "token cipher" string.
//  2. POST /api/auth/tokens with {"username","password":<cipher>} → token in
//     data.token of the JSON response.
//  3. Subsequent calls send the token via the "x-auth-token" header.
//
// The token is cached and refreshed lazily on the first request and on any
// 401 response.
type YanrongBackend struct {
	name     string
	baseURL  string
	username string
	password string
	client   *http.Client

	mu    sync.Mutex
	token string
}

// NewYanrongBackend creates a Yanrong backend. baseURL should be the host root,
// e.g. "https://192.168.73.25". TLS verification is disabled because Yanrong
// installs typically use self-signed certs.
func NewYanrongBackend(name, baseURL, username, password string) *YanrongBackend {
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	return &YanrongBackend{
		name:     name,
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		client:   &http.Client{Timeout: 30 * time.Second, Transport: tr},
	}
}

func (y *YanrongBackend) Type() string { return "yanrong" }

// ClusterInfo returns the cluster license / version info. Yanrong exposes this
// at /api/v3/license; if the endpoint is rejected, fall back to a quotas list
// so the user still sees something useful.
func (y *YanrongBackend) ClusterInfo(ctx context.Context) (string, error) {
	return y.authedGet(ctx, "/api/v3/license", nil)
}

// HealthCheck pings the cluster status endpoint.
func (y *YanrongBackend) HealthCheck(ctx context.Context) (string, error) {
	return y.authedGet(ctx, "/api/v3/cluster/status", nil)
}

// DirUsage queries the quota for a path (matches the get_quota example).
func (y *YanrongBackend) DirUsage(ctx context.Context, path string) (string, error) {
	if path == "" {
		path = "/"
	}
	q := url.Values{
		"page":      {"1"},
		"page_size": {"10"},
		"lang":      {"zh"},
		"key":       {path},
	}
	return y.authedGet(ctx, "/api/v3/quotas", q)
}

// authedGet runs a GET with the cached token, refreshing once on 401.
func (y *YanrongBackend) authedGet(ctx context.Context, path string, query url.Values) (string, error) {
	body, status, err := y.doGet(ctx, path, query)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized {
		// Token expired or first call. Re-login and retry once.
		y.mu.Lock()
		y.token = ""
		y.mu.Unlock()
		body, status, err = y.doGet(ctx, path, query)
		if err != nil {
			return "", err
		}
	}
	if status >= 400 {
		return "", fmt.Errorf("GET %s returned %d: %s", path, status, string(body))
	}
	return prettyJSON(body), nil
}

func (y *YanrongBackend) doGet(ctx context.Context, path string, query url.Values) ([]byte, int, error) {
	token, err := y.ensureToken(ctx)
	if err != nil {
		return nil, 0, err
	}

	u := y.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("x-auth-token", token)
	req.Header.Set("Accept", "application/json")

	resp, err := y.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("GET %s: %w", u, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	return body, resp.StatusCode, nil
}

func (y *YanrongBackend) ensureToken(ctx context.Context) (string, error) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.token != "" {
		return y.token, nil
	}

	cipher := yanrongPasswordCipher(y.password)
	payload, err := json.Marshal(map[string]string{
		"username": y.username,
		"password": cipher,
	})
	if err != nil {
		return "", fmt.Errorf("marshal login: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", y.baseURL+"/api/auth/tokens", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := y.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("yanrong login: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read login response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("yanrong login %d: %s", resp.StatusCode, string(body))
	}

	var parsed struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse login response: %w (body=%s)", err, string(body))
	}
	if parsed.Data.Token == "" {
		return "", fmt.Errorf("yanrong login returned empty token: %s", string(body))
	}
	y.token = parsed.Data.Token
	return y.token, nil
}

// yanrongPasswordCipher reproduces the calculate_md5_twice transform from
// examples/yrfs.py: md5(pw) → md5(md5(pw)) → interleave the two hex strings
// character by character into a 64-char string.
func yanrongPasswordCipher(password string) string {
	first := md5.Sum([]byte(password))
	firstHex := hex.EncodeToString(first[:])
	second := md5.Sum([]byte(firstHex))
	secondHex := hex.EncodeToString(second[:])

	var b strings.Builder
	b.Grow(len(firstHex) * 2)
	for i := 0; i < len(firstHex); i++ {
		b.WriteByte(firstHex[i])
		b.WriteByte(secondHex[i])
	}
	return b.String()
}

func prettyJSON(body []byte) string {
	var out bytes.Buffer
	if json.Indent(&out, body, "", "  ") == nil {
		return out.String()
	}
	return string(body)
}
