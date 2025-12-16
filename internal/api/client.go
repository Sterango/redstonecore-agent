package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type Client struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
}

func NewClient(baseURL, apiToken string) *Client {
	return &Client{
		baseURL:  baseURL,
		apiToken: apiToken,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) SetAPIToken(token string) {
	c.apiToken = token
}

// RegisterRequest is sent to register a new instance
type RegisterRequest struct {
	LicenseKey string                 `json:"license_key"`
	Name       string                 `json:"name"`
	Hostname   string                 `json:"hostname,omitempty"`
	IPAddress  string                 `json:"ip_address,omitempty"`
	Version    string                 `json:"version,omitempty"`
	SystemInfo map[string]interface{} `json:"system_info,omitempty"`
}

// RegisterResponse is returned after successful registration
type RegisterResponse struct {
	Success  bool `json:"success"`
	Instance struct {
		UUID     string `json:"uuid"`
		Name     string `json:"name"`
		APIToken string `json:"api_token"`
	} `json:"instance"`
	License struct {
		UUID       string `json:"uuid"`
		Plan       string `json:"plan"`
		MaxServers int    `json:"max_servers"`
	} `json:"license"`
}

// Register registers the instance with the cloud
func (c *Client) Register(req *RegisterRequest) (*RegisterResponse, error) {
	resp, err := c.post("/api/v1/instance/register", req, false)
	if err != nil {
		return nil, err
	}

	var result RegisterResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse register response: %w", err)
	}

	return &result, nil
}

// HeartbeatRequest is sent periodically to report status
type HeartbeatRequest struct {
	Status     string                 `json:"status"`
	SystemInfo map[string]interface{} `json:"system_info,omitempty"`
	Servers    []ServerStatus         `json:"servers,omitempty"`
}

type ServerStatus struct {
	UUID        string `json:"uuid"`
	Status      string `json:"status"`
	PlayerCount int    `json:"player_count,omitempty"`
}

// HeartbeatResponse contains pending commands
type HeartbeatResponse struct {
	Success   bool      `json:"success"`
	Timestamp string    `json:"timestamp"`
	Commands  []Command `json:"commands"`
}

type Command struct {
	UUID       string                 `json:"uuid"`
	Command    string                 `json:"command"`
	ServerUUID string                 `json:"server_uuid,omitempty"`
	Payload    map[string]interface{} `json:"payload,omitempty"`
}

// Heartbeat sends a heartbeat and receives pending commands
func (c *Client) Heartbeat(req *HeartbeatRequest) (*HeartbeatResponse, error) {
	resp, err := c.post("/api/v1/instance/heartbeat", req, true)
	if err != nil {
		return nil, err
	}

	var result HeartbeatResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse heartbeat response: %w", err)
	}

	return &result, nil
}

// SyncServersRequest syncs server list to cloud
type SyncServersRequest struct {
	Servers []SyncServer `json:"servers"`
}

type SyncServer struct {
	UUID             string `json:"uuid,omitempty"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	MinecraftVersion string `json:"minecraft_version,omitempty"`
	Port             int    `json:"port"`
	MaxPlayers       int    `json:"max_players"`
	AllocatedRAM     int    `json:"allocated_ram_mb"`
	Status           string `json:"status"`
	PlayerCount      int    `json:"player_count,omitempty"`
}

type SyncServersResponse struct {
	Success bool `json:"success"`
	Servers []struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	} `json:"servers"`
}

// SyncServers syncs the server list to the cloud
func (c *Client) SyncServers(req *SyncServersRequest) (*SyncServersResponse, error) {
	resp, err := c.post("/api/v1/instance/servers/sync", req, true)
	if err != nil {
		return nil, err
	}

	var result SyncServersResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse sync response: %w", err)
	}

	return &result, nil
}

// CommandResultRequest reports command execution result
type CommandResultRequest struct {
	CommandUUID string `json:"command_uuid"`
	Success     bool   `json:"success"`
	Result      string `json:"result,omitempty"`
}

// ReportCommandResult reports the result of a command execution
func (c *Client) ReportCommandResult(req *CommandResultRequest) error {
	_, err := c.post("/api/v1/instance/command/result", req, true)
	return err
}

// ConsoleRequest sends console output to cloud
type ConsoleRequest struct {
	ServerUUID string   `json:"server_uuid"`
	Lines      []string `json:"lines"`
}

// SendConsoleOutput sends console log lines to the cloud
func (c *Client) SendConsoleOutput(req *ConsoleRequest) error {
	_, err := c.post("/api/v1/instance/console", req, true)
	return err
}

// PlayerEventRequest reports player join/leave events
type PlayerEventRequest struct {
	ServerUUID string `json:"server_uuid"`
	Event      string `json:"event"` // "join" or "leave"
	PlayerUUID string `json:"player_uuid"`
	PlayerName string `json:"player_name"`
}

// ReportPlayerEvent reports a player join or leave event
func (c *Client) ReportPlayerEvent(req *PlayerEventRequest) error {
	_, err := c.post("/api/v1/instance/players", req, true)
	return err
}

// AnalyticsRequest reports server analytics snapshot
type AnalyticsRequest struct {
	ServerUUID   string  `json:"server_uuid"`
	PlayerCount  int     `json:"player_count"`
	TPS          float64 `json:"tps,omitempty"`
	MemoryUsedMB int     `json:"memory_used_mb,omitempty"`
	MemoryMaxMB  int     `json:"memory_max_mb,omitempty"`
	CPUPercent   float64 `json:"cpu_percent,omitempty"`
}

// ReportAnalytics reports server analytics
func (c *Client) ReportAnalytics(req *AnalyticsRequest) error {
	_, err := c.post("/api/v1/instance/analytics", req, true)
	return err
}

// PropertiesRequest syncs server.properties to cloud
type PropertiesRequest struct {
	ServerUUID string            `json:"server_uuid"`
	Properties map[string]string `json:"properties"`
}

// SyncProperties syncs server.properties to cloud
func (c *Client) SyncProperties(req *PropertiesRequest) error {
	_, err := c.post("/api/v1/instance/properties", req, true)
	return err
}

// ModpackProgressRequest reports modpack installation progress
type ModpackProgressRequest struct {
	ServerUUID  string `json:"server_uuid"`
	Stage       string `json:"stage"` // preparing, downloading_modpack, extracting, downloading_mods, installing_loader, finalizing, complete, error
	Message     string `json:"message"`
	Progress    int    `json:"progress"`
	Total       int    `json:"total"`
	CurrentFile string `json:"current_file,omitempty"`
}

// ReportModpackProgress reports modpack installation progress
func (c *Client) ReportModpackProgress(req *ModpackProgressRequest) error {
	_, err := c.post("/api/v1/instance/modpack/progress", req, true)
	return err
}

// DownloadPlugin downloads a plugin file from the cloud
func (c *Client) DownloadPlugin(pluginUUID, destPath string) error {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/instance/plugins/"+pluginUUID+"/download", nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("User-Agent", "RedstoneCore-Agent/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download failed (%d): %s", resp.StatusCode, string(body))
	}

	// Create destination file
	out, err := CreateFile(destPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// CreateFile is a helper to create a file with parent directories
func CreateFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	return os.Create(path)
}

func (c *Client) post(path string, body interface{}, auth bool) ([]byte, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "RedstoneCore-Agent/1.0")

	if auth && c.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Message != "" {
			return nil, fmt.Errorf("API error (%d): %s - %s", resp.StatusCode, errResp.Error, errResp.Message)
		}
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}
