package satisfactory

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Privilege levels for the Satisfactory HTTPS API.
const (
	PrivilegeClient       = "Client"
	PrivilegeAdministrator = "Administrator"
	PrivilegeInitialAdmin = "InitialAdmin"
)

// APIClient talks to a Satisfactory dedicated server's HTTPS API. The server
// uses a self-signed certificate, so TLS verification is disabled (the agent
// only ever connects to co-located servers over localhost).
//
// Every call is a POST to /api/v1 with a {"function","data"} envelope; success
// responses carry a {"data":{...}} object (or an empty 2xx body), and errors
// carry {"errorCode":...}.
type APIClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewAPIClient builds a client for https://host:port/api/v1.
func NewAPIClient(host string, port int) *APIClient {
	return &APIClient{
		baseURL: fmt.Sprintf("https://%s:%d/api/v1", host, port),
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // self-signed cert
			},
		},
	}
}

func (c *APIClient) SetToken(t string) { c.token = t }
func (c *APIClient) Token() string     { return c.token }

type apiError struct {
	ErrorCode    string `json:"errorCode"`
	ErrorMessage string `json:"errorMessage"`
}

// call posts an API envelope. If out is non-nil, the response "data" object is
// unmarshaled into it. Empty 2xx responses (204/202/201) are treated as success.
func (c *APIClient) call(function string, data, out any) error {
	body := map[string]any{"function": function}
	if data != nil {
		body["data"] = data
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", c.baseURL, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		var ae apiError
		if json.Unmarshal(respBody, &ae) == nil && ae.ErrorCode != "" {
			return fmt.Errorf("satisfactory api %s: %s", function, ae.ErrorCode)
		}
		return fmt.Errorf("satisfactory api %s: http %d: %s", function, resp.StatusCode, string(respBody))
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(respBody, &env); err != nil {
		return fmt.Errorf("decode %s response: %w", function, err)
	}
	if len(env.Data) == 0 {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// loginResp accommodates the API's inconsistent token casing: PasswordlessLogin
// and ClaimServer return "authenticationToken", PasswordLogin returns
// "AuthenticationToken".
type loginResp struct {
	Upper string `json:"AuthenticationToken"`
	Lower string `json:"authenticationToken"`
}

func (r loginResp) token() string {
	if r.Upper != "" {
		return r.Upper
	}
	return r.Lower
}

// HealthCheck returns nil when the server's API is up and the game responsive.
func (c *APIClient) HealthCheck() error {
	return c.call("HealthCheck", map[string]any{"ClientCustomData": ""}, nil)
}

// PasswordlessLogin works only while the server is unclaimed; it yields a token
// at up to InitialAdmin privilege.
func (c *APIClient) PasswordlessLogin(level string) (string, error) {
	var r loginResp
	if err := c.call("PasswordlessLogin", map[string]any{"MinimumPrivilegeLevel": level}, &r); err != nil {
		return "", err
	}
	return r.token(), nil
}

// PasswordLogin exchanges a password for a token at the requested level.
func (c *APIClient) PasswordLogin(level, password string) (string, error) {
	var r loginResp
	if err := c.call("PasswordLogin", map[string]any{"MinimumPrivilegeLevel": level, "Password": password}, &r); err != nil {
		return "", err
	}
	return r.token(), nil
}

// ClaimServer claims an unclaimed server (requires an InitialAdmin token), sets
// the admin password, and returns an Administrator token.
func (c *APIClient) ClaimServer(name, adminPassword string) (string, error) {
	var r loginResp
	if err := c.call("ClaimServer", map[string]any{"ServerName": name, "AdminPassword": adminPassword}, &r); err != nil {
		return "", err
	}
	return r.token(), nil
}

// RenameServer changes the server's display name (Administrator).
func (c *APIClient) RenameServer(name string) error {
	return c.call("RenameServer", map[string]any{"ServerName": name}, nil)
}

// SetClientPassword sets (or clears, when empty) the join password (Administrator).
func (c *APIClient) SetClientPassword(password string) error {
	return c.call("SetClientPassword", map[string]any{"Password": password}, nil)
}

// CreateNewGame starts a fresh session so the server has a running world.
func (c *APIClient) CreateNewGame(session string) error {
	return c.call("CreateNewGame", map[string]any{
		"NewGameData": map[string]any{
			"SessionName":     session,
			"bSkipOnboarding": true,
		},
	}, nil)
}

// ServerGameState is the live state from QueryServerState (lowercase JSON keys).
type ServerGameState struct {
	ActiveSessionName   string  `json:"activeSessionName"`
	NumConnectedPlayers int     `json:"numConnectedPlayers"`
	PlayerLimit         int     `json:"playerLimit"`
	TechTier            int     `json:"techTier"`
	GamePhase           string  `json:"gamePhase"`
	IsGameRunning       bool    `json:"isGameRunning"`
	IsGamePaused        bool    `json:"isGamePaused"`
	AverageTickRate     float64 `json:"averageTickRate"`
	TotalGameDuration   int     `json:"totalGameDuration"`
	AutoLoadSessionName string  `json:"autoLoadSessionName"`
}

// QueryServerState returns the live game state (no auth required).
func (c *APIClient) QueryServerState() (*ServerGameState, error) {
	var out struct {
		ServerGameState ServerGameState `json:"serverGameState"`
	}
	if err := c.call("QueryServerState", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return &out.ServerGameState, nil
}

// GetServerOptions returns the currently applied server options.
func (c *APIClient) GetServerOptions() (map[string]string, error) {
	var out struct {
		ServerOptions        map[string]string `json:"ServerOptions"`
		PendingServerOptions map[string]string `json:"PendingServerOptions"`
	}
	if err := c.call("GetServerOptions", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out.ServerOptions, nil
}

// ApplyServerOptions updates server options (Administrator).
func (c *APIClient) ApplyServerOptions(opts map[string]string) error {
	return c.call("ApplyServerOptions", map[string]any{"UpdatedServerOptions": opts}, nil)
}

// SaveGame flushes the current world to a named save (Administrator).
func (c *APIClient) SaveGame(name string) error {
	return c.call("SaveGame", map[string]any{"SaveName": name}, nil)
}

// RunCommand executes an arbitrary console command (Administrator) and returns
// its textual output.
func (c *APIClient) RunCommand(command string) (string, error) {
	var out struct {
		CommandResult string `json:"CommandResult"`
		ReturnValue   bool   `json:"ReturnValue"`
	}
	if err := c.call("RunCommand", map[string]any{"Command": command}, &out); err != nil {
		return "", err
	}
	return out.CommandResult, nil
}
