package sftp

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sterango/redstonecore-agent/internal/backup"
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

	// Set up pong handler to reset read deadline when we receive a pong
	conn.SetPongHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	// Start ping loop to keep connection alive through proxies
	go c.pingLoop()

	log.Println("[SFTP] Connected to relay server")
	return nil
}

// pingLoop sends periodic ping messages to keep the WebSocket alive
func (c *Client) pingLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.mu.Lock()
			if c.conn != nil {
				if err := c.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second)); err != nil {
					log.Printf("[SFTP] Ping failed: %v", err)
					c.mu.Unlock()
					return
				}
			}
			c.mu.Unlock()
		case <-c.done:
			return
		}
	}
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

// ArchiveTask tracks an async archive operation
type ArchiveTask struct {
	ID         string    `json:"id"`
	ServerUUID string    `json:"server_uuid"`
	Status     string    `json:"status"` // pending, creating, completed, failed
	Progress   int       `json:"progress"`
	Total      int       `json:"total"`
	Filename   string    `json:"filename"`
	OutputPath string    `json:"output_path"`
	Size       int64     `json:"size"`
	Error      string    `json:"error,omitempty"`
	Download   bool      `json:"download"`
	StartedAt  time.Time `json:"started_at"`
}

// DefaultHandler implements RequestHandler for file operations
type DefaultHandler struct {
	getServerPath     func(serverUUID string) (string, error)
	dataDir           string
	onBackupProgress  func(serverUUID, backupUUID, stage, message string, progress, total int)
	onBackupComplete  func(serverUUID, backupUUID string, success bool, filename string, size int64, errMsg string)
	onArchiveProgress func(serverUUID, taskID string, progress, total int, status string)
	archiveTasks      map[string]*ArchiveTask
	archiveTasksMu    sync.RWMutex
}

// NewDefaultHandler creates a handler with the given server path resolver
func NewDefaultHandler(getServerPath func(serverUUID string) (string, error)) *DefaultHandler {
	return &DefaultHandler{
		getServerPath: getServerPath,
		archiveTasks:  make(map[string]*ArchiveTask),
	}
}

// SetArchiveCallback sets the callback for archive progress
func (h *DefaultHandler) SetArchiveCallback(onProgress func(serverUUID, taskID string, progress, total int, status string)) {
	h.onArchiveProgress = onProgress
}

// SetDataDir sets the data directory for backup storage
func (h *DefaultHandler) SetDataDir(dataDir string) {
	h.dataDir = dataDir
}

// SetBackupCallbacks sets the callbacks for backup progress and completion
func (h *DefaultHandler) SetBackupCallbacks(
	onProgress func(serverUUID, backupUUID, stage, message string, progress, total int),
	onComplete func(serverUUID, backupUUID string, success bool, filename string, size int64, errMsg string),
) {
	h.onBackupProgress = onProgress
	h.onBackupComplete = onComplete
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
	case "backup_create":
		return h.handleBackupCreate(req)
	case "backup_list":
		return h.handleBackupList(req)
	case "backup_delete":
		return h.handleBackupDelete(req)
	case "backup_download":
		return h.handleBackupDownload(req)
	case "archive_create":
		return h.handleArchiveCreate(req)
	case "archive_create_async":
		return h.handleArchiveCreateAsync(req)
	case "archive_status":
		return h.handleArchiveStatus(req)
	case "archive_download":
		return h.handleArchiveDownload(req)
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

// getBackupDir returns the backup directory for a server
func (h *DefaultHandler) getBackupDir(serverUUID string) string {
	if h.dataDir == "" {
		// Fallback to server data directory parent
		basePath, err := h.getServerPath(serverUUID)
		if err != nil {
			return ""
		}
		return filepath.Join(filepath.Dir(filepath.Dir(basePath)), "backups", serverUUID)
	}
	return filepath.Join(h.dataDir, "backups", serverUUID)
}

// handleBackupCreate creates a new backup
func (h *DefaultHandler) handleBackupCreate(req *Request) *Response {
	serverPath, err := h.getServerPath(req.ServerUUID)
	if err != nil {
		return &Response{ID: req.ID, Success: false, Error: err.Error()}
	}

	backupType, _ := req.Data["type"].(string)
	if backupType == "" {
		backupType = "full"
	}

	backupUUID, _ := req.Data["backup_uuid"].(string)

	backupDir := h.getBackupDir(req.ServerUUID)

	log.Printf("[Backup] Creating %s backup for server %s", backupType, req.ServerUUID)

	// Run backup in background and report completion
	go func() {
		cfg := backup.Config{
			ServerUUID:    req.ServerUUID,
			ServerDataDir: serverPath,
			BackupDir:     backupDir,
			Type:          backupType,
			BackupUUID:    backupUUID,
			OnProgress: func(stage, message string, progress, total int) {
				if h.onBackupProgress != nil {
					h.onBackupProgress(req.ServerUUID, backupUUID, stage, message, progress, total)
				}
				log.Printf("[Backup] Progress: %s - %s (%d/%d)", stage, message, progress, total)
			},
		}

		result, err := backup.Create(cfg)
		if err != nil {
			log.Printf("[Backup] Failed: %v", err)
			if h.onBackupComplete != nil {
				h.onBackupComplete(req.ServerUUID, backupUUID, false, "", 0, err.Error())
			}
			return
		}

		log.Printf("[Backup] Completed: %s (%.2f MB)", result.Filename, float64(result.Size)/(1024*1024))
		if h.onBackupComplete != nil {
			h.onBackupComplete(req.ServerUUID, backupUUID, true, result.Filename, result.Size, "")
		}
	}()

	// Return immediately - backup runs in background
	return &Response{
		ID:      req.ID,
		Success: true,
		Data: map[string]interface{}{
			"status": "started",
		},
	}
}

// handleBackupList lists all backups for a server
func (h *DefaultHandler) handleBackupList(req *Request) *Response {
	backupDir := h.getBackupDir(req.ServerUUID)

	backups, err := backup.List(backupDir)
	if err != nil {
		return &Response{ID: req.ID, Success: false, Error: err.Error()}
	}

	// Convert to response format
	var backupList []map[string]interface{}
	for _, b := range backups {
		backupList = append(backupList, map[string]interface{}{
			"filename":   b.Filename,
			"size":       b.Size,
			"type":       b.Type,
			"created_at": b.CreatedAt.Unix(),
		})
	}

	return &Response{
		ID:      req.ID,
		Success: true,
		Data: map[string]interface{}{
			"backups": backupList,
		},
	}
}

// handleBackupDelete deletes a backup
func (h *DefaultHandler) handleBackupDelete(req *Request) *Response {
	filename, _ := req.Data["filename"].(string)
	if filename == "" {
		return &Response{ID: req.ID, Success: false, Error: "filename is required"}
	}

	backupDir := h.getBackupDir(req.ServerUUID)

	if err := backup.Delete(backupDir, filename); err != nil {
		return &Response{ID: req.ID, Success: false, Error: err.Error()}
	}

	log.Printf("[Backup] Deleted: %s", filename)
	return &Response{ID: req.ID, Success: true}
}

// handleBackupDownload returns backup file content for download
func (h *DefaultHandler) handleBackupDownload(req *Request) *Response {
	filename, _ := req.Data["filename"].(string)
	if filename == "" {
		return &Response{ID: req.ID, Success: false, Error: "filename is required"}
	}

	backupDir := h.getBackupDir(req.ServerUUID)

	reader, size, err := backup.GetReader(backupDir, filename)
	if err != nil {
		return &Response{ID: req.ID, Success: false, Error: err.Error()}
	}
	defer reader.Close()

	// For very large files, we might need to implement chunked transfer
	// For now, read the entire file (limited to reasonable size)
	const maxDownloadSize = 10 * 1024 * 1024 * 1024 // 10GB
	if size > maxDownloadSize {
		return &Response{ID: req.ID, Success: false, Error: "backup too large for download (max 10GB)"}
	}

	content, err := io.ReadAll(reader)
	if err != nil {
		return &Response{ID: req.ID, Success: false, Error: err.Error()}
	}

	// Encode as base64 for JSON transport
	return &Response{
		ID:      req.ID,
		Success: true,
		Data: map[string]interface{}{
			"filename": filename,
			"size":     size,
			"content":  base64.StdEncoding.EncodeToString(content),
		},
	}
}

// handleArchiveCreate creates an archive from a file or folder
func (h *DefaultHandler) handleArchiveCreate(req *Request) *Response {
	basePath, err := h.getServerPath(req.ServerUUID)
	if err != nil {
		return &Response{ID: req.ID, Success: false, Error: err.Error()}
	}

	// Get parameters
	name, _ := req.Data["name"].(string)
	format, _ := req.Data["format"].(string)
	download, _ := req.Data["download"].(bool)

	if name == "" {
		return &Response{ID: req.ID, Success: false, Error: "name is required"}
	}
	if format != "zip" && format != "tar.gz" {
		return &Response{ID: req.ID, Success: false, Error: "format must be 'zip' or 'tar.gz'"}
	}

	// Sanitize source path
	sourcePath, err := h.sanitizePath(basePath, req.Path)
	if err != nil {
		return &Response{ID: req.ID, Success: false, Error: err.Error()}
	}

	// Check source exists
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return &Response{ID: req.ID, Success: false, Error: "source not found"}
	}

	// Determine output path (in the same directory as source)
	outputDir := filepath.Dir(sourcePath)
	outputFilename := name + "." + format
	outputPath := filepath.Join(outputDir, outputFilename)

	log.Printf("[Archive] Creating %s archive: %s -> %s", format, sourcePath, outputPath)

	// Create archive
	var archiveErr error
	if format == "zip" {
		archiveErr = h.createZipArchive(sourcePath, outputPath, sourceInfo.IsDir())
	} else {
		archiveErr = h.createTarGzArchive(sourcePath, outputPath, sourceInfo.IsDir())
	}

	if archiveErr != nil {
		return &Response{ID: req.ID, Success: false, Error: archiveErr.Error()}
	}

	log.Printf("[Archive] Created: %s", outputPath)

	if download {
		// Read and return the archive content
		content, err := os.ReadFile(outputPath)
		if err != nil {
			return &Response{ID: req.ID, Success: false, Error: "failed to read archive"}
		}

		// Check size limit
		const maxDownloadSize = 10 * 1024 * 1024 * 1024 // 10GB
		if len(content) > maxDownloadSize {
			return &Response{ID: req.ID, Success: false, Error: "archive too large for download (max 10GB)"}
		}

		// Delete the archive file after reading (for download-only case)
		os.Remove(outputPath)

		return &Response{
			ID:      req.ID,
			Success: true,
			Data: map[string]interface{}{
				"filename": outputFilename,
				"size":     len(content),
				"content":  base64.StdEncoding.EncodeToString(content),
			},
		}
	}

	// Just return success (archive stays on disk)
	return &Response{
		ID:      req.ID,
		Success: true,
		Data: map[string]interface{}{
			"filename": outputFilename,
			"path":     outputPath,
		},
	}
}

// createZipArchive creates a zip archive
func (h *DefaultHandler) createZipArchive(source, dest string, isDir bool) error {
	file, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer file.Close()

	w := zip.NewWriter(file)
	defer w.Close()

	if isDir {
		return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			// Get relative path
			relPath, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}

			// Use base name of source as root folder
			baseName := filepath.Base(source)
			archivePath := filepath.Join(baseName, relPath)

			if info.IsDir() {
				// Add directory entry
				_, err = w.Create(archivePath + "/")
				return err
			}

			// Create file entry
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name = archivePath
			header.Method = zip.Deflate

			writer, err := w.CreateHeader(header)
			if err != nil {
				return err
			}

			fileContent, err := os.Open(path)
			if err != nil {
				return err
			}
			defer fileContent.Close()

			_, err = io.Copy(writer, fileContent)
			return err
		})
	}

	// Single file
	info, err := os.Stat(source)
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Method = zip.Deflate

	writer, err := w.CreateHeader(header)
	if err != nil {
		return err
	}

	fileContent, err := os.Open(source)
	if err != nil {
		return err
	}
	defer fileContent.Close()

	_, err = io.Copy(writer, fileContent)
	return err
}

// createTarGzArchive creates a tar.gz archive
func (h *DefaultHandler) createTarGzArchive(source, dest string, isDir bool) error {
	file, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer file.Close()

	gw := gzip.NewWriter(file)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	if isDir {
		return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			// Get relative path
			relPath, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}

			// Use base name of source as root folder
			baseName := filepath.Base(source)
			archivePath := filepath.Join(baseName, relPath)

			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			header.Name = archivePath

			if err := tw.WriteHeader(header); err != nil {
				return err
			}

			if info.IsDir() {
				return nil
			}

			fileContent, err := os.Open(path)
			if err != nil {
				return err
			}
			defer fileContent.Close()

			_, err = io.Copy(tw, fileContent)
			return err
		})
	}

	// Single file
	info, err := os.Stat(source)
	if err != nil {
		return err
	}

	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}

	if err := tw.WriteHeader(header); err != nil {
		return err
	}

	fileContent, err := os.Open(source)
	if err != nil {
		return err
	}
	defer fileContent.Close()

	_, err = io.Copy(tw, fileContent)
	return err
}

// countFiles recursively counts files in a directory
func countFiles(path string) (int, error) {
	count := 0
	err := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			count++
		}
		return nil
	})
	return count, err
}

// handleArchiveCreateAsync creates an archive asynchronously with progress tracking
func (h *DefaultHandler) handleArchiveCreateAsync(req *Request) *Response {
	basePath, err := h.getServerPath(req.ServerUUID)
	if err != nil {
		return &Response{ID: req.ID, Success: false, Error: err.Error()}
	}

	// Get parameters
	name, _ := req.Data["name"].(string)
	format, _ := req.Data["format"].(string)
	download, _ := req.Data["download"].(bool)
	taskID, _ := req.Data["task_id"].(string)

	if name == "" {
		return &Response{ID: req.ID, Success: false, Error: "name is required"}
	}
	if format != "zip" && format != "tar.gz" {
		return &Response{ID: req.ID, Success: false, Error: "format must be 'zip' or 'tar.gz'"}
	}
	if taskID == "" {
		taskID = fmt.Sprintf("archive-%d", time.Now().UnixNano())
	}

	// Sanitize source path
	sourcePath, err := h.sanitizePath(basePath, req.Path)
	if err != nil {
		return &Response{ID: req.ID, Success: false, Error: err.Error()}
	}

	// Check source exists
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return &Response{ID: req.ID, Success: false, Error: "source not found"}
	}

	// Determine output path
	outputDir := filepath.Dir(sourcePath)
	// For root path ("/"), put archive in the server directory
	if req.Path == "/" || req.Path == "" {
		outputDir = basePath
	}
	outputFilename := name + "." + format
	outputPath := filepath.Join(outputDir, outputFilename)

	// Count total files for progress
	var totalFiles int
	if sourceInfo.IsDir() {
		totalFiles, _ = countFiles(sourcePath)
	} else {
		totalFiles = 1
	}

	// Create task
	task := &ArchiveTask{
		ID:         taskID,
		ServerUUID: req.ServerUUID,
		Status:     "creating",
		Progress:   0,
		Total:      totalFiles,
		Filename:   outputFilename,
		OutputPath: outputPath,
		Download:   download,
		StartedAt:  time.Now(),
	}

	h.archiveTasksMu.Lock()
	h.archiveTasks[taskID] = task
	h.archiveTasksMu.Unlock()

	log.Printf("[Archive] Starting async %s archive: %s -> %s (task: %s, files: %d)", format, sourcePath, outputPath, taskID, totalFiles)

	// Run archive in background
	go func() {
		var archiveErr error
		progressCallback := func(current int) {
			h.archiveTasksMu.Lock()
			task.Progress = current
			h.archiveTasksMu.Unlock()

			if h.onArchiveProgress != nil {
				h.onArchiveProgress(req.ServerUUID, taskID, current, totalFiles, "creating")
			}
		}

		if format == "zip" {
			archiveErr = h.createZipArchiveWithProgress(sourcePath, outputPath, sourceInfo.IsDir(), progressCallback)
		} else {
			archiveErr = h.createTarGzArchiveWithProgress(sourcePath, outputPath, sourceInfo.IsDir(), progressCallback)
		}

		h.archiveTasksMu.Lock()
		if archiveErr != nil {
			task.Status = "failed"
			task.Error = archiveErr.Error()
			log.Printf("[Archive] Failed: %s - %v", taskID, archiveErr)
		} else {
			task.Status = "completed"
			task.Progress = totalFiles
			// Get file size
			if info, err := os.Stat(outputPath); err == nil {
				task.Size = info.Size()
			}
			log.Printf("[Archive] Completed: %s (%.2f MB)", taskID, float64(task.Size)/(1024*1024))
		}
		h.archiveTasksMu.Unlock()

		if h.onArchiveProgress != nil {
			h.onArchiveProgress(req.ServerUUID, taskID, task.Progress, totalFiles, task.Status)
		}
	}()

	// Return immediately with task ID
	return &Response{
		ID:      req.ID,
		Success: true,
		Data: map[string]interface{}{
			"task_id": taskID,
			"status":  "creating",
			"total":   totalFiles,
		},
	}
}

// handleArchiveStatus returns the status of an archive task
func (h *DefaultHandler) handleArchiveStatus(req *Request) *Response {
	taskID, _ := req.Data["task_id"].(string)
	if taskID == "" {
		return &Response{ID: req.ID, Success: false, Error: "task_id is required"}
	}

	h.archiveTasksMu.RLock()
	task, exists := h.archiveTasks[taskID]
	h.archiveTasksMu.RUnlock()

	if !exists {
		return &Response{ID: req.ID, Success: false, Error: "task not found"}
	}

	return &Response{
		ID:      req.ID,
		Success: true,
		Data: map[string]interface{}{
			"task_id":  task.ID,
			"status":   task.Status,
			"progress": task.Progress,
			"total":    task.Total,
			"filename": task.Filename,
			"size":     task.Size,
			"error":    task.Error,
		},
	}
}

// handleArchiveDownload downloads a completed archive
func (h *DefaultHandler) handleArchiveDownload(req *Request) *Response {
	taskID, _ := req.Data["task_id"].(string)
	if taskID == "" {
		return &Response{ID: req.ID, Success: false, Error: "task_id is required"}
	}

	h.archiveTasksMu.RLock()
	task, exists := h.archiveTasks[taskID]
	h.archiveTasksMu.RUnlock()

	if !exists {
		return &Response{ID: req.ID, Success: false, Error: "task not found"}
	}

	if task.Status != "completed" {
		return &Response{ID: req.ID, Success: false, Error: "archive not ready"}
	}

	// Read the archive content
	content, err := os.ReadFile(task.OutputPath)
	if err != nil {
		return &Response{ID: req.ID, Success: false, Error: "failed to read archive: " + err.Error()}
	}

	// Check size limit (10GB)
	const maxDownloadSize = 10 * 1024 * 1024 * 1024
	if len(content) > maxDownloadSize {
		return &Response{ID: req.ID, Success: false, Error: "archive too large for download (max 10GB)"}
	}

	// If download was requested, delete the archive after reading
	if task.Download {
		os.Remove(task.OutputPath)
		// Clean up task
		h.archiveTasksMu.Lock()
		delete(h.archiveTasks, taskID)
		h.archiveTasksMu.Unlock()
	}

	return &Response{
		ID:      req.ID,
		Success: true,
		Data: map[string]interface{}{
			"filename": task.Filename,
			"size":     len(content),
			"content":  base64.StdEncoding.EncodeToString(content),
		},
	}
}

// createZipArchiveWithProgress creates a zip archive with progress callback
func (h *DefaultHandler) createZipArchiveWithProgress(source, dest string, isDir bool, onProgress func(int)) error {
	file, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer file.Close()

	w := zip.NewWriter(file)
	defer w.Close()

	fileCount := 0

	if isDir {
		return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			// Get relative path
			relPath, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}

			// Use base name of source as root folder
			baseName := filepath.Base(source)
			archivePath := filepath.Join(baseName, relPath)

			if info.IsDir() {
				_, err = w.Create(archivePath + "/")
				return err
			}

			// Create file entry
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name = archivePath
			header.Method = zip.Deflate

			writer, err := w.CreateHeader(header)
			if err != nil {
				return err
			}

			fileContent, err := os.Open(path)
			if err != nil {
				return err
			}
			defer fileContent.Close()

			_, err = io.Copy(writer, fileContent)
			if err != nil {
				return err
			}

			fileCount++
			if onProgress != nil {
				onProgress(fileCount)
			}

			return nil
		})
	}

	// Single file
	info, err := os.Stat(source)
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Method = zip.Deflate

	writer, err := w.CreateHeader(header)
	if err != nil {
		return err
	}

	fileContent, err := os.Open(source)
	if err != nil {
		return err
	}
	defer fileContent.Close()

	_, err = io.Copy(writer, fileContent)
	if err == nil && onProgress != nil {
		onProgress(1)
	}
	return err
}

// createTarGzArchiveWithProgress creates a tar.gz archive with progress callback
func (h *DefaultHandler) createTarGzArchiveWithProgress(source, dest string, isDir bool, onProgress func(int)) error {
	file, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer file.Close()

	gw := gzip.NewWriter(file)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	fileCount := 0

	if isDir {
		return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			// Get relative path
			relPath, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}

			// Use base name of source as root folder
			baseName := filepath.Base(source)
			archivePath := filepath.Join(baseName, relPath)

			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			header.Name = archivePath

			if err := tw.WriteHeader(header); err != nil {
				return err
			}

			if info.IsDir() {
				return nil
			}

			fileContent, err := os.Open(path)
			if err != nil {
				return err
			}
			defer fileContent.Close()

			_, err = io.Copy(tw, fileContent)
			if err != nil {
				return err
			}

			fileCount++
			if onProgress != nil {
				onProgress(fileCount)
			}

			return nil
		})
	}

	// Single file
	info, err := os.Stat(source)
	if err != nil {
		return err
	}

	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}

	if err := tw.WriteHeader(header); err != nil {
		return err
	}

	fileContent, err := os.Open(source)
	if err != nil {
		return err
	}
	defer fileContent.Close()

	_, err = io.Copy(tw, fileContent)
	if err == nil && onProgress != nil {
		onProgress(1)
	}
	return err
}
