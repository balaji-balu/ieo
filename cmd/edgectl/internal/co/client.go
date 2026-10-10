package co

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type Client struct {
	BaseURL string
	client  *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		client:  &http.Client{},
	}
}

type HealthResponse struct {
	Status string `json:"status"`
}

type DeployRequest struct {
	Site      string       `json:"site"`
	AppID     string       `json:"app_id"`
	Category  string		`json:"category"`
	Version		string		`json:"version"`
	AppName     string       `json:"app_name"`
	//ProfileID string       `json:"profile_id"`
	Sites     []SiteTarget `json:"sites"`
	DeployType string 	   `json:"deploy_type"`	
}

type SiteTarget struct {
	SiteID  string   `json:"site_id"`
	HostIDs []string `json:"host_ids"`
}

type DeployResponse struct {
	DeploymentIDs []string `json:"deployment_ids"`
	Status       string `json:"status"`
}

// AppInfo must match what CO API sends
type AppInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Vendor      string   `json:"vendor"`
	Version     string   `json:"version"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

func (c *Client) Deploy(site, category, app, version, deploytype string) (*DeployResponse, error) {
	body, _ := json.Marshal(DeployRequest{
		//Site:      site,
		//AppID:     "",
		AppName: app,
		Category: category,
		Version: version,
		//ProfileID: "",
		Sites: []SiteTarget{
			{SiteID: site, HostIDs: []string{}},
		},
		DeployType: deploytype,
	})
	resp, err := c.client.Post(c.BaseURL+"/api/v1/deployments", 
		"application/json", 
		bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("CO API error: %s", data)
	}

	var result DeployResponse
	_ = json.NewDecoder(resp.Body).Decode(&result)
	//fmt.Println("resp:", result)
	fmt.Println("DEBUG Response:", result)
	return &result, nil
}

func (c *Client) ListApps(category, appName, version string) ([]AppInfo, error) {
	q := url.Values{}
	if category != "" {
		q.Set("category", category)
	}
	if appName != "" {
		q.Set("app_name", appName)
	}
	if version != "" {
		q.Set("version", version)
	}

	url := fmt.Sprintf("%s/api/v1/apps?%s", c.BaseURL, q.Encode())
	resp, err := c.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to contact CO: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	fmt.Println("DEBUG Response:", string(data))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CO returned %d: %s", resp.StatusCode, data)
	}

	// The CO returns {"apps": [...]}, so we decode that shape
	var wrapper struct {
		Apps []AppInfo `json:"apps"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return wrapper.Apps, nil
}

func (c *Client) AddApp(category, appName, version, artifact string) error {
	app := map[string]string{
		"category": category,
		"app_name": appName,
		"version": version,
		"repo_url": artifact,
	}
	body, _ := json.Marshal(app)

	url := fmt.Sprintf("%s/api/v1/apps", c.BaseURL)

	fmt.Println("Sending to url:", url, app)
	resp, err := c.client.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to contact CO: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("CO error: %s", data)
	}
	return nil
}

func (c *Client) DeleteApp(category, appName, version, artifact string) error {
    app := map[string]string{
        "category": category,
        "app_name": appName,
        "version":  version,
        "repo_url": artifact,
    }

    body, _ := json.Marshal(app)

    url := fmt.Sprintf("%s/api/v1/apps", c.BaseURL)

    req, err := http.NewRequest("DELETE", url, bytes.NewBuffer(body))
    if err != nil {
        return err
    }
    req.Header.Set("Content-Type", "application/json")

    resp, err := c.client.Do(req)
    if err != nil {
        return fmt.Errorf("failed to contact CO: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("delete failed: %s", string(b))
    }

    return nil
}

// Health checks /healthz endpoint of CO service
func (c *Client) Health() (*HealthResponse, error) {
	resp, err := c.client.Get(fmt.Sprintf("%s/api/v1/healthz", c.BaseURL))
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

type ComponentStatus struct {
    Name         string `json:"name"`
    State        string `json:"state"`
    ErrorCode    string `json:"errorCode"`
    ErrorMessage string `json:"errorMessage"`
}

type DeploymentStatusResponse struct {
    ID           string            `json:"id"`
    DeploymentID string            `json:"deploymentID"`
    State        string            `json:"state"`
    ErrorCode    string            `json:"errorCode"`
    ErrorMessage string            `json:"errorMessage"`
    Components   []ComponentStatus `json:"components"`
}

func (c *Client) DeploymentStatus(depID string) (*DeploymentStatusResponse, error) {
    url := fmt.Sprintf("%s/api/v1/deployments/%s/status", c.BaseURL, depID)
    //fmt.Printf("📡 Calling CO: %s\n", url)

    resp, err := c.client.Get(url)
    if err != nil {
        return nil, fmt.Errorf("failed to reach CO service: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        body, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("CO returned error %d: %s", resp.StatusCode, string(body))
    }

    var result DeploymentStatusResponse
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, fmt.Errorf("failed to parse CO response: %w", err)
    }

    //fmt.Println(pretty(result))
    return &result, nil
}


