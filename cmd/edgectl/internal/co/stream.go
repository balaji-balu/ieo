package co

import (
	"context"
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
)

type DeployEvent struct {
	//Status   string `json:"status"`
	Progress int    `json:"progress"`
	//Message  string `json:"message,omitempty"`

	DeploymentID string `json:"deployment_id"`
	//SiteID       string `json:"site_id"`
	Timestamp string `json:"timestamp"`
	SiteID    string `json:"site_id"`
	Message   string `json:"message,omitempty"`
	Status    string `json:"status"` // pending, in-progress, completed, failed

}

// StreamStatus streams deployment events until completed, failed, or cancelled.
func (c *Client) StreamStatus(ctx context.Context, id string, onEvent func(ev DeployEvent)) error {
	url := fmt.Sprintf("%s/api/v1/deployments/%s/stream", c.BaseURL, id)
	fmt.Printf("📡 Streaming status for %s\n→ %s\n", id, url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request failed: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			fmt.Printf("🛑 Stream for %s stopped by context\n", id)
			return nil
		default:
		}

		line := scanner.Text()
		if len(line) < 6 || line[:5] != "data:" {
			continue
		}

		var ev DeployEvent
		if err := json.Unmarshal([]byte(line[5:]), &ev); err == nil {
			onEvent(ev)
			if ev.Status == "installed" || ev.Status == "failed" {
				fmt.Printf("✅ Stream for %s finished (%s)\n", id, ev.Status)
				return nil
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("stream error: %w", err)
	}
	return nil
}

// StopStream cancels an active stream by canceling its context.
func (c *Client) StopStream(cancelFunc context.CancelFunc) {
	cancelFunc()
}