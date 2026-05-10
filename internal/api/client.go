package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/constants"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/db"
)

// Client is an HTTP client for the API server.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new API client.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: constants.HTTPClientTimeout,
		},
	}
}

// CreateScan creates a new scan record and returns the scan ID.
func (c *Client) CreateScan(ctx context.Context, scope, trigger string) (int64, error) {
	body, err := json.Marshal(map[string]string{
		"scope":   scope,
		"trigger": trigger,
	})
	if err != nil {
		return 0, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := c.do(ctx, http.MethodPost, "/api/scans", body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return 0, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var result struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}

	return result.ID, nil
}

// AddResults adds results to a scan.
func (c *Client) AddResults(ctx context.Context, scanID int64, results []db.Result) error {
	body, err := json.Marshal(results)
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}

	resp, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/scans/%d/results", scanID), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// CompleteScan marks a scan as completed.
func (c *Client) CompleteScan(ctx context.Context, scanID int64, resultCount int) error {
	body, err := json.Marshal(map[string]any{
		"status":       "completed",
		"result_count": resultCount,
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	resp, err := c.do(ctx, http.MethodPatch, fmt.Sprintf("/api/scans/%d", scanID), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return nil
}

// FailScan marks a scan as failed.
func (c *Client) FailScan(ctx context.Context, scanID int64, errMsg string) error {
	body, err := json.Marshal(map[string]string{
		"status": "failed",
		"error":  errMsg,
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	resp, err := c.do(ctx, http.MethodPatch, fmt.Sprintf("/api/scans/%d", scanID), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.httpClient.Do(req)
}
