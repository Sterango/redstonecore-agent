package sftp

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client handles the SFTP relay WebSocket connection
type Client struct {
	relayURL   string
	apiToken   string
	conn       *websocket.Conn
	handler    RequestHandler
	mu         sync.Mutex
	done       chan struct{}
	reconnect  bool
}

// RequestHandler interface for handling SFTP requests
type RequestHandler interface {
	HandleSFTPRequest(req *Request) *Response
}

// Request from the relay server
type Request struct {
	ID         string                 `json:"id"`
	ServerUUID string                 `json:"server_uuid"`
	Operation  string                 `json:"operation"` // list, read, write, delete, rename, mkdir, stat
	Path       string                 `json:"path"`
	Data       map[string]interface{} `json:"data,omitempty"`
}

// Response to the relay server
type Response struct {
	ID      string                 `json:"id"`
	Success bool                   `json:"success"`
	Error   string                 `json:"error,omitempty"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

// NewClient creates a new SFTP relay client
func NewClient(relayURL, apiToken string, handler RequestHandler) *Client {
	return &Client{
		relayURL:  relayURL,
		apiToken:  apiToken,
		handler:   handler,
		done:      make(chan struct{}),
		reconnect: true,
	}
}

// Start connects to the relay and starts handling requests
func (c *Client) Start() error {
	go c.connectionLoop()
	return nil
}

// Stop disconnects from the relay
func (c *Client) Stop() {
	c.reconnect = false
	close(c.done)
	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
	}
	c.mu.Unlock()
}

func (c *Client) connectionLoop() {
	for c.reconnect {
		if err := c.connect(); err != nil {
			log.Printf("[SFTP] Connection failed: %v, retrying in 5s", err)
			select {
			case <-time.After(5 * time.Second):
				continue
			case <-c.done:
				return
			}
		}

		c.handleMessages()

		if c.reconnect {
			log.Println("[SFTP] Disconnected, reconnecting in 5s...")
			select {
			case <-time.After(5 * time.Second):
			case <-c.done:
				return
			}
		}
	}
}

func (c *Client) connect() error {
	header := make(map[string][]string)
	header["Authorization"] = []string{"Bearer " + c.apiToken}

	conn, _, err := websocket.DefaultDialer.Dial(c.relayURL, header)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	log.Println("[SFTP] Connected to relay server")
	return nil
}

func (c *Client) handleMessages() {
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[SFTP] Read error: %v", err)
			}
			return
		}

		var req Request
		if err := json.Unmarshal(message, &req); err != nil {
			log.Printf("[SFTP] Invalid request: %v", err)
			continue
		}

		// Handle request in goroutine to not block
		go c.handleRequest(&req)
	}
}

func (c *Client) handleRequest(req *Request) {
	resp := c.handler.HandleSFTPRequest(req)

	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("[SFTP] Failed to marshal response: %v", err)
		return
	}

	c.mu.Lock()
	err = c.conn.WriteMessage(websocket.TextMessage, data)
	c.mu.Unlock()

	if err != nil {
		log.Printf("[SFTP] Failed to send response: %v", err)
	}
}

// DefaultHandler implements RequestHandler for file operations
type DefaultHandler struct {
	getServerPath func(serverUUID string) (string, error)
}

// NewDefaultHandler creates a handler with the given server path resolver
func NewDefaultHandler(getServerPath func(serverUUID string) (string, error)) *DefaultHandler {
	return &DefaultHandler{getServerPath: getServerPath}
}

// HandleSFTPRequest handles an SFTP request
func (h *DefaultHandler) HandleSFTPRequest(req *Request) *Response {
	basePath, err := h.getServerPath(req.ServerUUID)
	if err != nil {
		return &Response{ID: req.ID, Success: false, Error: err.Error()}
	}

	// Sanitize path
	fullPath, err := h.sanitizePath(basePath, req.Path)
	if err != nil {
		return &Response{ID: req.ID, Success: false, Error: err.Error()}
	}

	switch req.Operation {
	case "list":
		return h.handleList(req.ID, fullPath)
	case "read":
		return h.handleRead(req.ID, fullPath)
	case "write":
		return h.handleWrite(req.ID, fullPath, req.Data)
	case "delete":
		return h.handleDelete(req.ID, fullPath)
	case "rename":
		newPath, _ := req.Data["to"].(string)
		newFullPath, err := h.sanitizePath(basePath, newPath)
		if err != nil {
			return &Response{ID: req.ID, Success: false, Error: err.Error()}
		}
		return h.handleRename(req.ID, fullPath, newFullPath)
	case "mkdir":
		return h.handleMkdir(req.ID, fullPath)
	case "stat":
		return h.handleStat(req.ID, fullPath)
	default:
		return &Response{ID: req.ID, Success: false, Error: "unknown operation"}
	}
}

func (h *DefaultHandler) sanitizePath(basePath, requestedPath string) (string, error) {
	requestedPath = filepath.Clean(requestedPath)
	if requestedPath == "." {
		requestedPath = ""
	}
	requestedPath = filepath.Join("/", requestedPath)
	requestedPath = requestedPath[1:] // Remove leading /

	fullPath := filepath.Join(basePath, requestedPath)

	absBase, _ := filepath.Abs(basePath)
	absPath, _ := filepath.Abs(fullPath)

	if len(absPath) < len(absBase) || absPath[:len(absBase)] != absBase {
		return "", fmt.Errorf("access denied")
	}

	return absPath, nil
}

func (h *DefaultHandler) handleList(id, path string) *Response {
	entries, err := os.ReadDir(path)
	if err != nil {
		return &Response{ID: id, Success: false, Error: err.Error()}
	}

	var files []map[string]interface{}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, map[string]interface{}{
			"name":     entry.Name(),
			"is_dir":   entry.IsDir(),
			"size":     info.Size(),
			"mod_time": info.ModTime().Unix(),
		})
	}

	return &Response{
		ID:      id,
		Success: true,
		Data:    map[string]interface{}{"files": files},
	}
}

func (h *DefaultHandler) handleRead(id, path string) *Response {
	info, err := os.Stat(path)
	if err != nil {
		return &Response{ID: id, Success: false, Error: err.Error()}
	}

	if info.Size() > 10*1024*1024 { // 10MB limit
		return &Response{ID: id, Success: false, Error: "file too large"}
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return &Response{ID: id, Success: false, Error: err.Error()}
	}

	return &Response{
		ID:      id,
		Success: true,
		Data:    map[string]interface{}{"content": string(content)},
	}
}

func (h *DefaultHandler) handleWrite(id, path string, data map[string]interface{}) *Response {
	content, _ := data["content"].(string)

	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &Response{ID: id, Success: false, Error: err.Error()}
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return &Response{ID: id, Success: false, Error: err.Error()}
	}

	return &Response{ID: id, Success: true}
}

func (h *DefaultHandler) handleDelete(id, path string) *Response {
	// Protect critical files
	name := filepath.Base(path)
	protected := []string{"server.jar", "eula.txt", ".uuid"}
	for _, p := range protected {
		if name == p {
			return &Response{ID: id, Success: false, Error: "cannot delete protected file"}
		}
	}

	if err := os.RemoveAll(path); err != nil {
		return &Response{ID: id, Success: false, Error: err.Error()}
	}

	return &Response{ID: id, Success: true}
}

func (h *DefaultHandler) handleRename(id, oldPath, newPath string) *Response {
	if err := os.Rename(oldPath, newPath); err != nil {
		return &Response{ID: id, Success: false, Error: err.Error()}
	}

	return &Response{ID: id, Success: true}
}

func (h *DefaultHandler) handleMkdir(id, path string) *Response {
	if err := os.MkdirAll(path, 0755); err != nil {
		return &Response{ID: id, Success: false, Error: err.Error()}
	}

	return &Response{ID: id, Success: true}
}

func (h *DefaultHandler) handleStat(id, path string) *Response {
	info, err := os.Stat(path)
	if err != nil {
		return &Response{ID: id, Success: false, Error: err.Error()}
	}

	return &Response{
		ID:      id,
		Success: true,
		Data: map[string]interface{}{
			"name":     info.Name(),
			"is_dir":   info.IsDir(),
			"size":     info.Size(),
			"mod_time": info.ModTime().Unix(),
		},
	}
}
