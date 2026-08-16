package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a minimal GitHub REST client used by gitmirror.
// Authentication is intentionally token based for the first bootstrap phase.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(token string) *Client {
	return &Client{
		baseURL: "https://api.github.com",
		token:   token,
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *Client) Get(ctx context.Context, path string) ([]byte, int, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, fmt.Errorf("github api returned %d: %s", resp.StatusCode, string(body))
	}
	return body, resp.StatusCode, nil
}
