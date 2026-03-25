package milvus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const (
	gcPausePath  = "/management/datacoord/garbage_collection/pause"
	gcResumePath = "/management/datacoord/garbage_collection/resume"
)

// gcPauseResponse is the JSON body returned by the Milvus management GC pause endpoint.
type gcPauseResponse struct {
	Msg    string `json:"msg"`
	Ticket string `json:"ticket"`
}

// PauseGC asks the Milvus datacoord to pause garbage collection for pauseSeconds seconds.
// The GC lease expires automatically after that duration, so callers that need a longer
// pause must periodically re-call PauseGC before the lease expires.
// pauseSeconds must be > 0; values above 3600 are clamped by the server.
//
// The returned ticket must be passed to ResumeGC to resume collection.
func (c *Client) PauseGC(ctx context.Context, pauseSeconds int32) (string, error) {
	endpoint, err := url.Parse(c.managementURL + gcPausePath)
	if err != nil {
		return "", fmt.Errorf("building gc pause URL: %w", err)
	}
	q := endpoint.Query()
	q.Set("pause_seconds", fmt.Sprintf("%d", pauseSeconds))
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", fmt.Errorf("creating gc pause request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gc pause request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gc pause returned HTTP %d: %s", resp.StatusCode, body)
	}

	var result gcPauseResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decoding gc pause response: %w", err)
	}
	if result.Msg != "OK" {
		return "", fmt.Errorf("gc pause non-OK response: %s", result.Msg)
	}
	return result.Ticket, nil
}

// ResumeGC asks the Milvus datacoord to resume garbage collection immediately,
// without waiting for the pause lease to expire.
// ticket is the opaque string returned by PauseGC; the server uses it to
// identify which pause lease to cancel.
func (c *Client) ResumeGC(ctx context.Context, ticket string) error {
	endpoint, err := url.Parse(c.managementURL + gcResumePath)
	if err != nil {
		return fmt.Errorf("building gc resume URL: %w", err)
	}
	q := endpoint.Query()
	q.Set("ticket", ticket)
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("creating gc resume request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("gc resume request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gc resume returned HTTP %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decoding gc resume response: %w", err)
	}
	if result.Msg != "OK" {
		return fmt.Errorf("gc resume non-OK response: %s", result.Msg)
	}
	return nil
}
