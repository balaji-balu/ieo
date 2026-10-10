package lo

import (
	"fmt"
	"encoding/json"
	"net/http"
)

type Client struct {
	BaseURL string
	client  *http.Client
}

type HealthResponse struct {
	Status string `json:"status"`
}

func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		client:  &http.Client{},
	}
}

// Health checks /healthz endpoint of CO service
func (c *Client) Health() (*HealthResponse, error) {
	resp, err := c.client.Get(fmt.Sprintf("%s/healthz", c.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("failed to reach CO service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("service unhealthy (status=%d)", resp.StatusCode)
	}

	var h HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, fmt.Errorf("invalid response: %w", err)
	}
	return &h, nil
}

func (c *Client) Hosts() (error) {
	resp, err := c.client.Get(fmt.Sprintf("%s/hosts", c.BaseURL))
	if err != nil {
		return fmt.Errorf("failed to reach CO service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Failed to get Hosts (status=%d)", resp.StatusCode)
	}	

	var h interface{}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return fmt.Errorf("invalid response: %w", err)
	}
	fmt.Println(pretty(h))	
	return nil
}

func (c *Client) Actual() (error) {
	resp, err := c.client.Get(fmt.Sprintf("%s/actual", c.BaseURL))
	if err != nil {
		return fmt.Errorf("failed to reach CO service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Failed to get Hosts (status=%d)", resp.StatusCode)
	}	

	var h interface{}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return fmt.Errorf("invalid response: %w", err)
	}
	fmt.Println(pretty(h))	
	return nil
}

func pretty(v interface{}) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}