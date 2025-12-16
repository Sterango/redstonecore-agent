package agent

import (
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sterango/redstonecore-agent/internal/api"
	"github.com/sterango/redstonecore-agent/internal/minecraft"
)

// FileItem represents a file or directory in the file manager
type FileItem struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Type        string `json:"type"` // "file" or "directory"
	Size        int64  `json:"size"`
	Modified    string `json:"modified"`
	Permissions string `json:"permissions,omitempty"`
}

// handleFileOperation handles file operation commands and reports results
func (a *Agent) handleFileOperation(cmd api.Command, server *minecraft.Server) {
	if cmd.Payload == nil {
		a.reportFileError(cmd.ServerUUID, "", "payload is required")
		return
	}

	requestID, _ := cmd.Payload["request_id"].(string)
	if requestID == "" {
		log.Printf("[Files] Missing request_id in payload")
		return
	}

	var result map[string]interface{}
	var err error

	switch cmd.Command {
	case "files_list":
		result, err = a.filesList(cmd, server)
	case "files_read":
		result, err = a.filesRead(cmd, server)
	case "files_write":
		result, err = a.filesWrite(cmd, server)
	case "files_delete":
		result, err = a.filesDelete(cmd, server)
	case "files_rename":
		result, err = a.filesRename(cmd, server)
	case "files_mkdir":
		result, err = a.filesMkdir(cmd, server)
	case "files_upload":
		result, err = a.filesUpload(cmd, server)
	case "files_download":
		result, err = a.filesDownload(cmd, server)
	default:
		err = fmt.Errorf("unknown file operation: %s", cmd.Command)
	}

	// Report result back to cloud
	req := &api.FileOperationResultRequest{
		ServerUUID: cmd.ServerUUID,
		RequestID:  requestID,
		Success:    err == nil,
		Data:       result,
	}
	if err != nil {
		req.Error = err.Error()
	}

	if reportErr := a.client.ReportFileOperationResult(req); reportErr != nil {
		log.Printf("[Files] Failed to report result: %v", reportErr)
	}
}

// reportFileError reports a file operation error
func (a *Agent) reportFileError(serverUUID, requestID, errMsg string) {
	if requestID == "" {
		return
	}
	a.client.ReportFileOperationResult(&api.FileOperationResultRequest{
		ServerUUID: serverUUID,
		RequestID:  requestID,
		Success:    false,
		Error:      errMsg,
	})
}

// sanitizePath prevents directory traversal attacks
func sanitizePath(basePath, requestedPath string) (string, error) {
	// Normalize the path
	requestedPath = filepath.Clean(requestedPath)

	// Remove leading slash for joining
	requestedPath = strings.TrimPrefix(requestedPath, "/")

	// Join with base path
	fullPath := filepath.Join(basePath, requestedPath)

	// Resolve any symlinks and get absolute path
	absBase, err := filepath.Abs(basePath)
	if err != nil {
		return "", fmt.Errorf("invalid base path")
	}

	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		return "", fmt.Errorf("invalid path")
	}

	// Ensure the path is within the base directory
	if !strings.HasPrefix(absPath, absBase) {
		return "", fmt.Errorf("access denied: path outside server directory")
	}

	return absPath, nil
}

// filesList lists files in a directory
func (a *Agent) filesList(cmd api.Command, server *minecraft.Server) (map[string]interface{}, error) {
	path, _ := cmd.Payload["path"].(string)
	if path == "" {
		path = "/"
	}

	fullPath, err := sanitizePath(server.DataDir, path)
	if err != nil {
		return nil, err
	}

	log.Printf("[Files] Listing directory: %s", fullPath)

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var files []FileItem
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Calculate relative path from server root
		relativePath := strings.TrimPrefix(filepath.Join(path, entry.Name()), "/")
		if !strings.HasPrefix(relativePath, "/") {
			relativePath = "/" + relativePath
		}

		fileType := "file"
		if entry.IsDir() {
			fileType = "directory"
		}

		files = append(files, FileItem{
			Name:        entry.Name(),
			Path:        relativePath,
			Type:        fileType,
			Size:        info.Size(),
			Modified:    info.ModTime().Format(time.RFC3339),
			Permissions: info.Mode().String(),
		})
	}

	return map[string]interface{}{
		"files": files,
		"path":  path,
	}, nil
}

// filesRead reads a file's content
func (a *Agent) filesRead(cmd api.Command, server *minecraft.Server) (map[string]interface{}, error) {
	path, _ := cmd.Payload["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}

	fullPath, err := sanitizePath(server.DataDir, path)
	if err != nil {
		return nil, err
	}

	log.Printf("[Files] Reading file: %s", fullPath)

	// Check file size (limit to 5MB for text files)
	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, fmt.Errorf("file not found: %w", err)
	}

	if info.IsDir() {
		return nil, fmt.Errorf("cannot read directory as file")
	}

	if info.Size() > 5*1024*1024 {
		return nil, fmt.Errorf("file too large (max 5MB)")
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return map[string]interface{}{
		"content": string(content),
		"path":    path,
		"size":    info.Size(),
	}, nil
}

// filesWrite writes content to a file
func (a *Agent) filesWrite(cmd api.Command, server *minecraft.Server) (map[string]interface{}, error) {
	path, _ := cmd.Payload["path"].(string)
	content, _ := cmd.Payload["content"].(string)

	if path == "" {
		return nil, fmt.Errorf("path is required")
	}

	fullPath, err := sanitizePath(server.DataDir, path)
	if err != nil {
		return nil, err
	}

	log.Printf("[Files] Writing file: %s (%d bytes)", fullPath, len(content))

	// Ensure parent directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	return map[string]interface{}{
		"success": true,
		"path":    path,
	}, nil
}

// filesDelete deletes a file or directory
func (a *Agent) filesDelete(cmd api.Command, server *minecraft.Server) (map[string]interface{}, error) {
	path, _ := cmd.Payload["path"].(string)
	if path == "" || path == "/" {
		return nil, fmt.Errorf("cannot delete root directory")
	}

	fullPath, err := sanitizePath(server.DataDir, path)
	if err != nil {
		return nil, err
	}

	// Extra safety: prevent deleting critical files
	filename := filepath.Base(fullPath)
	protectedFiles := []string{"server.jar", "eula.txt", ".uuid"}
	for _, protected := range protectedFiles {
		if filename == protected {
			return nil, fmt.Errorf("cannot delete protected file: %s", protected)
		}
	}

	log.Printf("[Files] Deleting: %s", fullPath)

	if err := os.RemoveAll(fullPath); err != nil {
		return nil, fmt.Errorf("failed to delete: %w", err)
	}

	return map[string]interface{}{
		"success": true,
		"path":    path,
	}, nil
}

// filesRename renames a file or directory
func (a *Agent) filesRename(cmd api.Command, server *minecraft.Server) (map[string]interface{}, error) {
	from, _ := cmd.Payload["from"].(string)
	to, _ := cmd.Payload["to"].(string)

	if from == "" || to == "" {
		return nil, fmt.Errorf("from and to paths are required")
	}

	fromPath, err := sanitizePath(server.DataDir, from)
	if err != nil {
		return nil, err
	}

	toPath, err := sanitizePath(server.DataDir, to)
	if err != nil {
		return nil, err
	}

	log.Printf("[Files] Renaming: %s -> %s", fromPath, toPath)

	if err := os.Rename(fromPath, toPath); err != nil {
		return nil, fmt.Errorf("failed to rename: %w", err)
	}

	return map[string]interface{}{
		"success": true,
		"from":    from,
		"to":      to,
	}, nil
}

// filesMkdir creates a directory
func (a *Agent) filesMkdir(cmd api.Command, server *minecraft.Server) (map[string]interface{}, error) {
	path, _ := cmd.Payload["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}

	fullPath, err := sanitizePath(server.DataDir, path)
	if err != nil {
		return nil, err
	}

	log.Printf("[Files] Creating directory: %s", fullPath)

	if err := os.MkdirAll(fullPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	return map[string]interface{}{
		"success": true,
		"path":    path,
	}, nil
}

// filesUpload uploads a file (base64 encoded)
func (a *Agent) filesUpload(cmd api.Command, server *minecraft.Server) (map[string]interface{}, error) {
	path, _ := cmd.Payload["path"].(string)
	content, _ := cmd.Payload["content"].(string)
	encoding, _ := cmd.Payload["encoding"].(string)

	if path == "" || content == "" {
		return nil, fmt.Errorf("path and content are required")
	}

	fullPath, err := sanitizePath(server.DataDir, path)
	if err != nil {
		return nil, err
	}

	log.Printf("[Files] Uploading file: %s", fullPath)

	var data []byte
	if encoding == "base64" {
		data, err = base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, fmt.Errorf("failed to decode base64: %w", err)
		}
	} else {
		data = []byte(content)
	}

	// Ensure parent directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	if err := os.WriteFile(fullPath, data, 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	return map[string]interface{}{
		"success": true,
		"path":    path,
		"size":    len(data),
	}, nil
}

// filesDownload downloads a file (returns base64 encoded)
func (a *Agent) filesDownload(cmd api.Command, server *minecraft.Server) (map[string]interface{}, error) {
	path, _ := cmd.Payload["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}

	fullPath, err := sanitizePath(server.DataDir, path)
	if err != nil {
		return nil, err
	}

	log.Printf("[Files] Downloading file: %s", fullPath)

	// Check file size (limit to 100MB)
	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, fmt.Errorf("file not found: %w", err)
	}

	if info.IsDir() {
		return nil, fmt.Errorf("cannot download directory")
	}

	if info.Size() > 100*1024*1024 {
		return nil, fmt.Errorf("file too large (max 100MB)")
	}

	file, err := os.Open(fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return map[string]interface{}{
		"content":  base64.StdEncoding.EncodeToString(data),
		"path":     path,
		"size":     info.Size(),
		"filename": filepath.Base(fullPath),
	}, nil
}
