package agent

import (
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sterango/redstonecore-agent/internal/api"
	"github.com/sterango/redstonecore-agent/internal/config"
	"github.com/sterango/redstonecore-agent/internal/heartbeat"
	"github.com/sterango/redstonecore-agent/internal/minecraft"
	"github.com/sterango/redstonecore-agent/internal/sftp"
	"gopkg.in/yaml.v3"
)

// Ensure Agent implements the heartbeat.ServerStatusProvider interface
var _ heartbeat.ServerStatusProvider = (*Agent)(nil)

type Agent struct {
	config         *config.Config
	client         *api.Client
	heartbeat      *heartbeat.Heartbeat
	sftpClient     *sftp.Client
	servers        map[string]*minecraft.Server
	serversMu      sync.RWMutex
	consoleBuffers map[string]*minecraft.ConsoleBuffer
	consoleMu      sync.RWMutex
}

func New(cfg *config.Config) *Agent {
	client := api.NewClient(cfg.CloudURL, cfg.APIToken)

	return &Agent{
		config:         cfg,
		client:         client,
		servers:        make(map[string]*minecraft.Server),
		consoleBuffers: make(map[string]*minecraft.ConsoleBuffer),
	}
}

func (a *Agent) Run() error {
	log.Println("Starting RedstoneCore Agent...")

	// Register or load existing registration
	if !a.config.IsRegistered() {
		if err := a.register(); err != nil {
			return fmt.Errorf("failed to register: %w", err)
		}
	} else {
		log.Printf("Using existing registration: %s", a.config.InstanceUUID)
		a.client.SetAPIToken(a.config.APIToken)
	}

	// Initialize servers from config
	if err := a.initServers(); err != nil {
		return fmt.Errorf("failed to initialize servers: %w", err)
	}

	// Discover existing servers on disk
	if err := a.discoverServers(); err != nil {
		log.Printf("Warning: Failed to discover existing servers: %v", err)
	}

	// Sync servers to cloud
	if err := a.syncServers(); err != nil {
		log.Printf("Warning: Failed to sync servers: %v", err)
	}

	// Start heartbeat (1 second interval for immediate command execution)
	a.heartbeat = heartbeat.New(a.client, a, 1*time.Second)
	a.heartbeat.Start()

	// Start SFTP relay client
	if a.config.SFTPRelayURL != "" {
		sftpHandler := sftp.NewDefaultHandler(a.getServerDataDir)
		sftpHandler.SetDataDir(a.config.DataDir)
		sftpHandler.SetBackupCallbacks(
			// Progress callback
			func(serverUUID, backupUUID, stage, message string, progress, total int) {
				if a.client != nil {
					a.client.ReportBackupProgress(&api.BackupProgressRequest{
						ServerUUID: serverUUID,
						BackupUUID: backupUUID,
						Stage:      stage,
						Message:    message,
						Progress:   progress,
						Total:      total,
					})
				}
			},
			// Complete callback
			func(serverUUID, backupUUID string, success bool, filename string, size int64, errMsg string) {
				if a.client != nil {
					a.client.ReportBackupComplete(&api.BackupCompleteRequest{
						ServerUUID: serverUUID,
						BackupUUID: backupUUID,
						Success:    success,
						Filename:   filename,
						Size:       size,
						Error:      errMsg,
					})
				}
			},
		)
		a.sftpClient = sftp.NewClient(a.config.SFTPRelayURL, a.config.APIToken, sftpHandler)
		if err := a.sftpClient.Start(); err != nil {
			log.Printf("Warning: Failed to start SFTP client: %v", err)
		} else {
			log.Println("SFTP relay client started")
		}
	}

	// Auto-start servers marked for auto-start
	a.autoStartServers()

	log.Println("RedstoneCore Agent is running. Press Ctrl+C to stop.")

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	return a.shutdown()
}

func (a *Agent) register() error {
	hostname, _ := os.Hostname()
	publicIP := a.getPublicIP()

	req := &api.RegisterRequest{
		LicenseKey: a.config.LicenseKey,
		Name:       hostname,
		Hostname:   hostname,
		IPAddress:  publicIP,
		Version:    "1.0.0",
		SystemInfo: a.getSystemInfo(),
	}

	log.Println("Registering with cloud...")
	resp, err := a.client.Register(req)
	if err != nil {
		return err
	}

	a.config.InstanceUUID = resp.Instance.UUID
	a.config.APIToken = resp.Instance.APIToken
	a.client.SetAPIToken(resp.Instance.APIToken)

	// Save credentials
	if err := a.config.SaveCredentials(); err != nil {
		log.Printf("Warning: Failed to save credentials: %v", err)
	}

	log.Printf("Registered successfully! Instance UUID: %s", resp.Instance.UUID)
	log.Printf("License: %s (%s), Max servers: %d", resp.License.UUID, resp.License.Plan, resp.License.MaxServers)

	return nil
}

// getPublicIP fetches the public IP address using an external service
func (a *Agent) getPublicIP() string {
	services := []string{
		"https://api.ipify.org",
		"https://icanhazip.com",
		"https://ifconfig.me/ip",
	}

	client := &http.Client{Timeout: 5 * time.Second}

	for _, svc := range services {
		resp, err := client.Get(svc)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				continue
			}
			ip := strings.TrimSpace(string(body))
			if ip != "" {
				log.Printf("Detected public IP: %s", ip)
				return ip
			}
		}
	}

	log.Println("Could not detect public IP")
	return ""
}

func (a *Agent) initServers() error {
	for i, serverCfg := range a.config.Servers {
		serverDir := filepath.Join(a.config.DataDir, "servers", serverCfg.Name)

		// Create server directory
		if err := os.MkdirAll(serverDir, 0755); err != nil {
			return fmt.Errorf("failed to create server directory: %w", err)
		}

		// Generate a stable UUID based on index (or load from file)
		uuid := a.loadOrCreateServerUUID(serverDir, i)

		// Create console buffer for this server
		consoleBuf := a.createConsoleBuffer(uuid)

		server := minecraft.NewServer(minecraft.ServerConfig{
			UUID:             uuid,
			Name:             serverCfg.Name,
			Type:             minecraft.ServerType(serverCfg.Type),
			MinecraftVersion: serverCfg.MinecraftVersion,
			Port:             serverCfg.Port,
			MaxPlayers:       serverCfg.MaxPlayers,
			AllocatedRAM:     serverCfg.AllocatedRAM,
			DataDir:          serverDir,
			OnConsoleLine: func(line string) {
				consoleBuf.AddLine(line)
			},
			OnPlayerEvent: a.createPlayerEventCallback(uuid),
		})

		// Ensure server.properties is set up
		if err := server.EnsureServerProperties(); err != nil {
			log.Printf("Warning: Failed to setup server.properties for %s: %v", serverCfg.Name, err)
		}

		a.serversMu.Lock()
		a.servers[uuid] = server
		a.serversMu.Unlock()

		log.Printf("Initialized server: %s (%s)", serverCfg.Name, uuid)
	}

	return nil
}

// discoverServers scans the data directory for existing servers and loads them
func (a *Agent) discoverServers() error {
	serversDir := filepath.Join(a.config.DataDir, "servers")

	// Check if servers directory exists
	if _, err := os.Stat(serversDir); os.IsNotExist(err) {
		return nil // No servers directory yet
	}

	entries, err := os.ReadDir(serversDir)
	if err != nil {
		return fmt.Errorf("failed to read servers directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		serverDir := filepath.Join(serversDir, entry.Name())
		uuidFile := filepath.Join(serverDir, ".uuid")

		// Check if this server has a UUID file
		uuidData, err := os.ReadFile(uuidFile)
		if err != nil {
			continue // No UUID file, skip
		}

		uuid := strings.TrimSpace(string(uuidData))

		// Skip if already loaded
		a.serversMu.RLock()
		_, exists := a.servers[uuid]
		a.serversMu.RUnlock()
		if exists {
			continue
		}

		// Try to load server.properties to get config
		props, _ := minecraft.LoadServerProperties(serverDir)

		// Default values
		serverType := minecraft.TypePaper
		minecraftVersion := "1.21"
		port := 25565
		maxPlayers := 20
		allocatedRAM := 2048

		// Try to read allocated RAM from user_jvm_args.txt if it exists
		jvmArgsPath := filepath.Join(serverDir, "user_jvm_args.txt")
		if jvmArgsData, err := os.ReadFile(jvmArgsPath); err == nil {
			jvmArgsContent := string(jvmArgsData)
			// Parse -Xmx value (e.g., -Xmx12288M or -Xmx8G)
			for _, line := range strings.Split(jvmArgsContent, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "-Xmx") {
					memStr := strings.TrimPrefix(line, "-Xmx")
					// Parse the value
					var memValue int
					if strings.HasSuffix(memStr, "G") {
						fmt.Sscanf(strings.TrimSuffix(memStr, "G"), "%d", &memValue)
						allocatedRAM = memValue * 1024
					} else if strings.HasSuffix(memStr, "M") {
						fmt.Sscanf(strings.TrimSuffix(memStr, "M"), "%d", &memValue)
						allocatedRAM = memValue
					}
					break
				}
			}
		}

		// Detect server type from files
		if _, err := os.Stat(filepath.Join(serverDir, "startserver.sh")); err == nil {
			// ATM-style modpack with NeoForge
			serverType = minecraft.TypeNeoForge
		} else if matches, _ := filepath.Glob(filepath.Join(serverDir, "neoforge-*.jar")); len(matches) > 0 {
			serverType = minecraft.TypeNeoForge
		} else if matches, _ := filepath.Glob(filepath.Join(serverDir, "forge-*.jar")); len(matches) > 0 {
			serverType = minecraft.TypeForge
		} else if matches, _ := filepath.Glob(filepath.Join(serverDir, "fabric-*.jar")); len(matches) > 0 {
			serverType = minecraft.TypeFabric
		} else if matches, _ := filepath.Glob(filepath.Join(serverDir, "paper-*.jar")); len(matches) > 0 {
			serverType = minecraft.TypePaper
		} else if matches, _ := filepath.Glob(filepath.Join(serverDir, "spigot-*.jar")); len(matches) > 0 {
			serverType = minecraft.TypeSpigot
		}

		// Override from properties if available
		if props != nil {
			if p, ok := props["server-port"]; ok {
				fmt.Sscanf(p, "%d", &port)
			}
			if p, ok := props["max-players"]; ok {
				fmt.Sscanf(p, "%d", &maxPlayers)
			}
		}

		// Create console buffer for this server
		consoleBuf := a.createConsoleBuffer(uuid)

		server := minecraft.NewServer(minecraft.ServerConfig{
			UUID:             uuid,
			Name:             entry.Name(),
			Type:             serverType,
			MinecraftVersion: minecraftVersion,
			Port:             port,
			MaxPlayers:       maxPlayers,
			AllocatedRAM:     allocatedRAM,
			DataDir:          serverDir,
			OnConsoleLine: func(line string) {
				consoleBuf.AddLine(line)
			},
			OnPlayerEvent: a.createPlayerEventCallback(uuid),
		})

		a.serversMu.Lock()
		a.servers[uuid] = server
		a.serversMu.Unlock()

		log.Printf("Discovered existing server: %s (%s)", entry.Name(), uuid)

		// Sync properties to cloud
		go a.syncServerProperties(server)
	}

	return nil
}

func (a *Agent) loadOrCreateServerUUID(serverDir string, index int) string {
	uuidFile := filepath.Join(serverDir, ".uuid")

	// Try to load existing UUID
	if data, err := os.ReadFile(uuidFile); err == nil {
		return string(data)
	}

	// Generate a new UUID v4
	uuid := generateUUID()

	// Save UUID
	os.WriteFile(uuidFile, []byte(uuid), 0644)

	return uuid
}

// generateUUID generates a random UUID v4
func generateUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	// Set version 4
	b[6] = (b[6] & 0x0f) | 0x40
	// Set variant
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (a *Agent) syncServers() error {
	a.serversMu.Lock()
	defer a.serversMu.Unlock()

	var servers []api.SyncServer
	for _, server := range a.servers {
		servers = append(servers, api.SyncServer{
			UUID:             server.UUID,
			Name:             server.Name,
			Type:             string(server.Type),
			MinecraftVersion: server.MinecraftVersion,
			Port:             server.Port,
			MaxPlayers:       server.MaxPlayers,
			AllocatedRAM:     server.AllocatedRAM,
			Status:           string(server.Status),
			PlayerCount:      server.PlayerCount,
			CurrentPlayers:   server.GetCurrentPlayers(),
		})
	}

	if len(servers) == 0 {
		return nil
	}

	resp, err := a.client.SyncServers(&api.SyncServersRequest{Servers: servers})
	if err != nil {
		return err
	}

	// Build a set of cloud server UUIDs for quick lookup
	cloudServerUUIDs := make(map[string]string) // UUID -> Name
	for _, syncedServer := range resp.Servers {
		cloudServerUUIDs[syncedServer.UUID] = syncedServer.Name
	}

	// Update UUIDs from cloud response (in case they were assigned by cloud)
	for _, syncedServer := range resp.Servers {
		for uuid, server := range a.servers {
			if server.Name == syncedServer.Name && uuid != syncedServer.UUID {
				// UUID was assigned by cloud, update our reference
				a.servers[syncedServer.UUID] = server
				server.UUID = syncedServer.UUID
				delete(a.servers, uuid)
				// Update the cloud lookup with the new UUID
				cloudServerUUIDs[syncedServer.UUID] = syncedServer.Name
				break
			}
		}
	}

	// Remove orphaned servers (exist locally but not in cloud)
	for uuid, server := range a.servers {
		if _, existsInCloud := cloudServerUUIDs[uuid]; !existsInCloud {
			log.Printf("Removing orphaned server from tracking: %s (%s) - no longer exists in cloud", server.Name, uuid)
			// Stop console buffer if exists
			a.stopConsoleBuffer(uuid)
			// Remove from local tracking (but keep files - user can manually delete)
			delete(a.servers, uuid)
		}
	}

	log.Printf("Synced %d servers to cloud", len(resp.Servers))
	return nil
}

func (a *Agent) autoStartServers() {
	for i, serverCfg := range a.config.Servers {
		if serverCfg.AutoStart {
			uuid := a.loadOrCreateServerUUID(filepath.Join(a.config.DataDir, "servers", serverCfg.Name), i)
			if server, ok := a.servers[uuid]; ok {
				log.Printf("Auto-starting server: %s", serverCfg.Name)
				go func(s *minecraft.Server) {
					if err := s.Start(); err != nil {
						log.Printf("Failed to auto-start server %s: %v", s.Name, err)
					}
				}(server)
			}
		}
	}
}

func (a *Agent) shutdown() error {
	// Stop SFTP client
	if a.sftpClient != nil {
		a.sftpClient.Stop()
	}

	// Stop heartbeat
	if a.heartbeat != nil {
		a.heartbeat.Stop()
	}

	// Stop all servers gracefully
	a.serversMu.RLock()
	for _, server := range a.servers {
		if server.Status == minecraft.StatusRunning {
			log.Printf("Stopping server: %s", server.Name)
			if err := server.Stop(); err != nil {
				log.Printf("Error stopping server %s: %v", server.Name, err)
				server.Kill()
			}
		}
	}
	a.serversMu.RUnlock()

	// Stop all console buffers
	a.consoleMu.Lock()
	for uuid, buf := range a.consoleBuffers {
		buf.Stop()
		delete(a.consoleBuffers, uuid)
	}
	a.consoleMu.Unlock()

	log.Println("Shutdown complete.")
	return nil
}

// getServerDataDir returns the data directory for a server by UUID
func (a *Agent) getServerDataDir(serverUUID string) (string, error) {
	a.serversMu.RLock()
	defer a.serversMu.RUnlock()

	if server, exists := a.servers[serverUUID]; exists {
		return server.DataDir, nil
	}

	return "", fmt.Errorf("server not found: %s", serverUUID)
}

// GetServerStatuses implements heartbeat.ServerStatusProvider
func (a *Agent) GetServerStatuses() []api.ServerStatus {
	a.serversMu.RLock()
	defer a.serversMu.RUnlock()

	var statuses []api.ServerStatus
	for _, server := range a.servers {
		statuses = append(statuses, api.ServerStatus{
			UUID:        server.UUID,
			Status:      string(server.Status),
			PlayerCount: server.PlayerCount,
		})
	}

	return statuses
}

// GetServerAnalytics implements heartbeat.ServerStatusProvider
func (a *Agent) GetServerAnalytics() []heartbeat.ServerAnalytics {
	a.serversMu.RLock()
	defer a.serversMu.RUnlock()

	var analytics []heartbeat.ServerAnalytics
	for _, server := range a.servers {
		// Only report analytics for running servers
		if server.Status != minecraft.StatusRunning {
			continue
		}

		analytics = append(analytics, heartbeat.ServerAnalytics{
			UUID:         server.UUID,
			PlayerCount:  server.PlayerCount,
			TPS:          0, // TPS tracking not yet implemented
			MemoryUsedMB: 0, // Memory tracking not yet implemented
			MemoryMaxMB:  server.AllocatedRAM,
			CPUPercent:   0, // CPU tracking not yet implemented
		})
	}

	return analytics
}

// ExecuteCommand implements heartbeat.ServerStatusProvider
func (a *Agent) ExecuteCommand(cmd api.Command) error {
	// Handle create_server command specially (no existing server)
	if cmd.Command == "create_server" {
		return a.createServer(cmd)
	}

	// Handle create_modpack_server command
	if cmd.Command == "create_modpack_server" {
		return a.createModpackServer(cmd)
	}

	// Handle delete_server command
	if cmd.Command == "delete_server" {
		return a.deleteServer(cmd)
	}

	// Handle change_modpack command (requires server lookup)
	if cmd.Command == "change_modpack" {
		return a.changeModpack(cmd)
	}

	// Handle update_agent command (self-update via Docker)
	if cmd.Command == "update_agent" {
		return a.updateAgent(cmd)
	}

	a.serversMu.RLock()
	server, ok := a.servers[cmd.ServerUUID]
	a.serversMu.RUnlock()

	if !ok {
		// Server not found - might have been deleted, return success to clear the command
		log.Printf("Warning: Server not found for command %s: %s", cmd.Command, cmd.ServerUUID)
		return nil
	}

	switch cmd.Command {
	case "start":
		return server.Start()
	case "stop":
		return server.Stop()
	case "restart":
		return server.Restart()
	case "kill":
		return server.Kill()
	case "console":
		if cmd.Payload == nil {
			return fmt.Errorf("console command requires payload")
		}
		input, ok := cmd.Payload["input"].(string)
		if !ok || input == "" {
			return fmt.Errorf("console command requires input")
		}
		return server.SendCommand(input)
	case "install_plugin":
		return a.installPlugin(cmd, server)
	case "update_plugin":
		return a.updatePlugin(cmd, server)
	case "delete_plugin":
		return a.deletePlugin(cmd, server)
	case "update_properties":
		return a.updateProperties(cmd, server)
	case "change_server_type":
		return a.changeServerType(cmd, server)
	case "update_proxy_config":
		return a.updateProxyConfig(cmd, server)
	case "configure_velocity_forwarding":
		return a.configureVelocityForwarding(cmd, server)
	case "files_list", "files_read", "files_write", "files_delete", "files_rename", "files_mkdir", "files_upload", "files_download":
		// File operations are handled async and report their own results
		go a.handleFileOperation(cmd, server)
		return nil
	default:
		return fmt.Errorf("unknown command: %s", cmd.Command)
	}
}

// createServer handles the create_server command from cloud
func (a *Agent) createServer(cmd api.Command) error {
	if cmd.Payload == nil {
		return fmt.Errorf("create_server requires payload")
	}

	// Extract server config from payload
	name, _ := cmd.Payload["name"].(string)
	serverType, _ := cmd.Payload["type"].(string)
	version, _ := cmd.Payload["minecraft_version"].(string)
	port, _ := cmd.Payload["port"].(float64)
	maxPlayers, _ := cmd.Payload["max_players"].(float64)
	ram, _ := cmd.Payload["allocated_ram_mb"].(float64)

	if name == "" {
		return fmt.Errorf("server name is required")
	}

	log.Printf("Creating server: %s (type: %s, version: %s)", name, serverType, version)

	// Create server directory
	serverDir := filepath.Join(a.config.DataDir, "servers", name)
	if err := os.MkdirAll(serverDir, 0755); err != nil {
		return fmt.Errorf("failed to create server directory: %w", err)
	}

	// Download the server JAR
	downloader := minecraft.NewDownloader(filepath.Join(a.config.DataDir, "cache"))
	jarPath, err := downloader.DownloadServer(minecraft.ServerType(serverType), version, serverDir)
	if err != nil {
		return fmt.Errorf("failed to download server JAR: %w", err)
	}

	log.Printf("Downloaded JAR to: %s", jarPath)

	// Create console buffer for this server
	consoleBuf := a.createConsoleBuffer(cmd.ServerUUID)

	// Create server instance
	server := minecraft.NewServer(minecraft.ServerConfig{
		UUID:             cmd.ServerUUID,
		Name:             name,
		Type:             minecraft.ServerType(serverType),
		MinecraftVersion: version,
		Port:             int(port),
		MaxPlayers:       int(maxPlayers),
		AllocatedRAM:     int(ram),
		DataDir:          serverDir,
		OnConsoleLine: func(line string) {
			consoleBuf.AddLine(line)
		},
		OnPlayerEvent: a.createPlayerEventCallback(cmd.ServerUUID),
	})

	// Ensure server.properties is set up
	if err := server.EnsureServerProperties(); err != nil {
		log.Printf("Warning: Failed to setup server.properties: %v", err)
	}

	// Save UUID file
	uuidFile := filepath.Join(serverDir, ".uuid")
	os.WriteFile(uuidFile, []byte(cmd.ServerUUID), 0644)

	// Add to servers map
	a.serversMu.Lock()
	a.servers[cmd.ServerUUID] = server
	a.serversMu.Unlock()

	log.Printf("Server %s created successfully!", name)
	return nil
}

// deleteServer handles the delete_server command from cloud
func (a *Agent) deleteServer(cmd api.Command) error {
	a.serversMu.Lock()
	server, ok := a.servers[cmd.ServerUUID]
	if !ok {
		a.serversMu.Unlock()
		log.Printf("Warning: Server not found for delete: %s", cmd.ServerUUID)
		return nil // Server doesn't exist, consider it deleted
	}

	// Stop the server if running
	if server.Status == minecraft.StatusRunning {
		log.Printf("Stopping server %s before deletion...", server.Name)
		server.Stop()
	}

	// Remove from servers map
	delete(a.servers, cmd.ServerUUID)
	a.serversMu.Unlock()

	// Stop console buffer
	a.stopConsoleBuffer(cmd.ServerUUID)

	// Delete server files if requested
	if cmd.Payload != nil {
		if deleteFiles, ok := cmd.Payload["delete_files"].(bool); ok && deleteFiles {
			serverDir := filepath.Join(a.config.DataDir, "servers", server.Name)
			log.Printf("Deleting server files: %s", serverDir)
			if err := os.RemoveAll(serverDir); err != nil {
				log.Printf("Warning: Failed to delete server files: %v", err)
			}
		}
	}

	log.Printf("Server %s deleted successfully!", server.Name)
	return nil
}

// updateAgent handles the update_agent command from cloud
// This function triggers a self-update via Docker
func (a *Agent) updateAgent(cmd api.Command) error {
	log.Printf("[Update] Starting self-update...")

	// Pull the latest image
	log.Printf("[Update] Pulling latest image...")
	pullCmd := exec.Command("docker", "pull", "ghcr.io/sterango/redstonecore-agent:latest")
	pullCmd.Stdout = os.Stdout
	pullCmd.Stderr = os.Stderr
	if err := pullCmd.Run(); err != nil {
		return fmt.Errorf("failed to pull latest image: %w", err)
	}

	log.Printf("[Update] Image pulled successfully. Restarting container...")

	// Use a temporary alpine container to run docker compose
	// This ensures the restart command survives when this container is killed
	restartCmd := exec.Command("docker", "run", "--rm", "-d",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", "/docker-compose.yml:/docker-compose.yml:ro",
		"docker/compose:latest",
		"-p", "redstonecore", "-f", "/docker-compose.yml", "up", "-d", "--force-recreate")
	restartCmd.Stdout = os.Stdout
	restartCmd.Stderr = os.Stderr

	if err := restartCmd.Run(); err != nil {
		return fmt.Errorf("failed to start restart command: %w", err)
	}

	log.Printf("[Update] Restart command initiated. Container will restart shortly...")

	// Give Docker a moment to start the restart process
	time.Sleep(2 * time.Second)

	return nil
}

// createModpackServer handles the create_modpack_server command from cloud
func (a *Agent) createModpackServer(cmd api.Command) error {
	if cmd.Payload == nil {
		return fmt.Errorf("create_modpack_server requires payload")
	}

	// Extract server config from payload
	name, _ := cmd.Payload["name"].(string)
	port, _ := cmd.Payload["port"].(float64)
	maxPlayers, _ := cmd.Payload["max_players"].(float64)
	ram, _ := cmd.Payload["allocated_ram_mb"].(float64)

	// Extract modpack info
	modpackData, ok := cmd.Payload["modpack"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("modpack info is required")
	}

	versionID, _ := modpackData["version_id"].(string)
	modpackName, _ := modpackData["name"].(string)
	loader, _ := modpackData["loader"].(string)
	downloadURL, _ := modpackData["download_url"].(string)
	filename, _ := modpackData["filename"].(string)
	curseforgeAPIKey, _ := modpackData["curseforge_api_key"].(string)
	serverPackURL, _ := modpackData["server_pack_url"].(string)
	serverPackFilename, _ := modpackData["server_pack_filename"].(string)

	if name == "" || versionID == "" {
		return fmt.Errorf("server name and modpack version_id are required")
	}

	useServerPack := serverPackURL != ""
	log.Printf("Creating modpack server: %s (modpack: %s, loader: %s, using server pack: %v)", name, modpackName, loader, useServerPack)

	// Create server directory
	serverDir := filepath.Join(a.config.DataDir, "servers", name)
	if err := os.MkdirAll(serverDir, 0755); err != nil {
		return fmt.Errorf("failed to create server directory: %w", err)
	}

	// Determine server type based on loader
	var serverType minecraft.ServerType
	switch loader {
	case "fabric":
		serverType = minecraft.TypeFabric
	case "forge":
		serverType = minecraft.TypeForge
	case "neoforge":
		serverType = minecraft.TypeNeoForge
	case "quilt":
		serverType = minecraft.TypeFabric
	default:
		serverType = minecraft.TypeFabric
	}

	mcVersion, _ := cmd.Payload["minecraft_version"].(string)
	if mcVersion == "" {
		mcVersion = "1.20.1"
	}

	var jarPath string

	// If server pack URL is provided, use it (includes loader and mods)
	if useServerPack {
		log.Printf("Using server pack from: %s", serverPackURL)

		if a.client != nil {
			a.client.ReportModpackProgress(&api.ModpackProgressRequest{
				ServerUUID: cmd.ServerUUID,
				Stage:      "downloading_server_pack",
				Message:    "Downloading server pack...",
				Progress:   10,
				Total:      100,
			})
		}

		// Download and extract server pack
		serverPackInstaller := minecraft.NewModpackInstaller(filepath.Join(a.config.DataDir, "cache"), curseforgeAPIKey)
		serverPackInstaller.SetProgressCallback(func(stage, message string, progress, total int, currentFile string) {
			log.Printf("[Progress] Stage: %s, Progress: %d/%d, File: %s", stage, progress, total, currentFile)
			if a.client != nil {
				a.client.ReportModpackProgress(&api.ModpackProgressRequest{
					ServerUUID:  cmd.ServerUUID,
					Stage:       stage,
					Message:     message,
					Progress:    progress,
					Total:       total,
					CurrentFile: currentFile,
				})
			}
		})

		err := serverPackInstaller.InstallServerPack(serverPackURL, serverPackFilename, serverDir)
		if err != nil {
			log.Printf("Warning: Failed to install server pack: %v, falling back to manual install", err)
			useServerPack = false // Fall through to manual installation
		} else {
			log.Printf("Server pack installed successfully")
			// Server pack includes the loader, find the run script or jar
			// Check for startserver.sh first (used by ATM and similar modpacks)
			startScript := filepath.Join(serverDir, "startserver.sh")
			runScript := filepath.Join(serverDir, "run.sh")
			if _, err := os.Stat(startScript); err == nil {
				os.Chmod(startScript, 0755)
				jarPath = startScript
				log.Printf("Found startserver.sh for server pack")
			} else if _, err := os.Stat(runScript); err == nil {
				os.Chmod(runScript, 0755)
				jarPath = runScript
				log.Printf("Found run.sh for server pack")
			} else {
				// Look for server jar
				patterns := []string{"forge-*.jar", "neoforge-*.jar", "fabric-*.jar", "server.jar"}
				for _, pattern := range patterns {
					matches, _ := filepath.Glob(filepath.Join(serverDir, pattern))
					for _, m := range matches {
						if !strings.Contains(filepath.Base(m), "installer") {
							jarPath = m
							break
						}
					}
					if jarPath != "" {
						break
					}
				}
			}
		}
	}

	// Fall back to manual installation if server pack not available or failed
	if !useServerPack {
		// Install the modpack (use direct URL if provided)
		modpackInstaller := minecraft.NewModpackInstaller(filepath.Join(a.config.DataDir, "cache"), curseforgeAPIKey)

		// Set up progress callback to report to cloud
		modpackInstaller.SetProgressCallback(func(stage, message string, progress, total int, currentFile string) {
			log.Printf("[Progress] Stage: %s, Progress: %d/%d, File: %s", stage, progress, total, currentFile)
			if a.client != nil {
				err := a.client.ReportModpackProgress(&api.ModpackProgressRequest{
					ServerUUID:  cmd.ServerUUID,
					Stage:       stage,
					Message:     message,
					Progress:    progress,
					Total:       total,
					CurrentFile: currentFile,
				})
				if err != nil {
					log.Printf("[Progress] Failed to report progress: %v", err)
				}
			}
		})

		var downloadInfo *minecraft.ModpackDownloadInfo
		if downloadURL != "" {
			downloadInfo = &minecraft.ModpackDownloadInfo{
				URL:      downloadURL,
				Filename: filename,
			}
		}
		modpackInfo, err := modpackInstaller.InstallModpack(versionID, serverDir, downloadInfo)
		if err != nil {
			return fmt.Errorf("failed to install modpack: %w", err)
		}

		// Determine Minecraft version and loader from modpack
		newMcVersion := modpackInfo.GetMinecraftVersion()
		loaderType := modpackInfo.GetLoaderType()
		loaderVersion := modpackInfo.GetLoaderVersion()

		if newMcVersion != "" {
			mcVersion = newMcVersion
		}
		if loaderType == "" {
			loaderType = loader // Use the loader from payload if not in index
		}

		log.Printf("Modpack requires: Minecraft %s, %s %s", mcVersion, loaderType, loaderVersion)

		// Update server type based on actual loader from modpack
		switch loaderType {
		case "fabric":
			serverType = minecraft.TypeFabric
		case "forge":
			serverType = minecraft.TypeForge
		case "neoforge":
			serverType = minecraft.TypeNeoForge
		case "quilt":
			serverType = minecraft.TypeFabric
		}

		// Download the appropriate server JAR based on loader
		downloader := minecraft.NewDownloader(filepath.Join(a.config.DataDir, "cache"))

		jarPath, err = downloader.DownloadServerWithLoader(serverType, mcVersion, loaderVersion, serverDir)
		if err != nil {
			log.Printf("Warning: Failed to download %s server: %v, trying alternative...", loaderType, err)
			// Fallback to just the loader without specific version
			jarPath, err = downloader.DownloadServer(serverType, mcVersion, serverDir)
			if err != nil {
				return fmt.Errorf("failed to download server JAR: %w", err)
			}
		}
	}

	log.Printf("Server JAR/script: %s", jarPath)

	// Create console buffer for this server
	consoleBuf := a.createConsoleBuffer(cmd.ServerUUID)

	// Create server instance
	server := minecraft.NewServer(minecraft.ServerConfig{
		UUID:             cmd.ServerUUID,
		Name:             name,
		Type:             serverType,
		MinecraftVersion: mcVersion,
		Port:             int(port),
		MaxPlayers:       int(maxPlayers),
		AllocatedRAM:     int(ram),
		DataDir:          serverDir,
		OnConsoleLine: func(line string) {
			consoleBuf.AddLine(line)
		},
		OnPlayerEvent: a.createPlayerEventCallback(cmd.ServerUUID),
	})

	// Ensure server.properties is set up
	if err := server.EnsureServerProperties(); err != nil {
		log.Printf("Warning: Failed to setup server.properties: %v", err)
	}

	// Save UUID file
	uuidFile := filepath.Join(serverDir, ".uuid")
	os.WriteFile(uuidFile, []byte(cmd.ServerUUID), 0644)

	// Add to servers map
	a.serversMu.Lock()
	a.servers[cmd.ServerUUID] = server
	a.serversMu.Unlock()

	log.Printf("Modpack server %s created successfully! (%s %s)",
		name, loader, mcVersion)
	return nil
}

// changeModpack handles the change_modpack command - switches an existing server to a different modpack
func (a *Agent) changeModpack(cmd api.Command) error {
	if cmd.Payload == nil {
		return fmt.Errorf("change_modpack requires payload")
	}

	// Get the server
	a.serversMu.RLock()
	server, ok := a.servers[cmd.ServerUUID]
	a.serversMu.RUnlock()

	if !ok {
		return fmt.Errorf("server not found: %s", cmd.ServerUUID)
	}

	// Ensure server is stopped
	if server.Status == minecraft.StatusRunning {
		return fmt.Errorf("server must be stopped before changing modpacks")
	}

	// Extract modpack info
	modpackData, ok := cmd.Payload["modpack"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("modpack info is required")
	}

	versionID, _ := modpackData["version_id"].(string)
	modpackName, _ := modpackData["name"].(string)
	loader, _ := modpackData["loader"].(string)
	downloadURL, _ := modpackData["download_url"].(string)
	filename, _ := modpackData["filename"].(string)
	curseforgeAPIKey, _ := modpackData["curseforge_api_key"].(string)
	serverPackURL, _ := modpackData["server_pack_url"].(string)
	serverPackFilename, _ := modpackData["server_pack_filename"].(string)
	mcVersion, _ := cmd.Payload["minecraft_version"].(string)

	if versionID == "" {
		return fmt.Errorf("modpack version_id is required")
	}

	useServerPack := serverPackURL != ""
	log.Printf("Changing modpack on server %s to: %s (loader: %s, using server pack: %v)", server.Name, modpackName, loader, useServerPack)

	serverDir := server.DataDir

	// Clean up old server files before installing new modpack
	log.Printf("Cleaning up old server files...")
	filesToDelete := []string{
		"server.jar",
		"run.sh",
		"run.bat",
		"user_jvm_args.txt",
		"forge-installer.jar",
	}
	for _, file := range filesToDelete {
		filePath := filepath.Join(serverDir, file)
		if _, err := os.Stat(filePath); err == nil {
			log.Printf("Removing old file: %s", file)
			os.Remove(filePath)
		}
	}

	// Remove old directories that might conflict
	dirsToDelete := []string{
		"mods",
		"libraries",
		"config",
	}
	for _, dir := range dirsToDelete {
		dirPath := filepath.Join(serverDir, dir)
		if _, err := os.Stat(dirPath); err == nil {
			log.Printf("Removing old directory: %s", dir)
			os.RemoveAll(dirPath)
		}
	}

	// Also remove any old forge/neoforge JAR patterns
	oldJarPatterns := []string{"forge-*.jar", "neoforge-*.jar", "fabric-*.jar"}
	for _, pattern := range oldJarPatterns {
		matches, _ := filepath.Glob(filepath.Join(serverDir, pattern))
		for _, match := range matches {
			log.Printf("Removing old JAR: %s", filepath.Base(match))
			os.Remove(match)
		}
	}

	// Determine server type based on loader
	var serverType minecraft.ServerType
	switch loader {
	case "fabric":
		serverType = minecraft.TypeFabric
	case "forge":
		serverType = minecraft.TypeForge
	case "neoforge":
		serverType = minecraft.TypeNeoForge
	case "quilt":
		serverType = minecraft.TypeFabric
	default:
		serverType = minecraft.TypeFabric
	}

	var jarPath string

	// If server pack URL is provided, use it (includes loader and mods)
	if useServerPack {
		log.Printf("Using server pack from: %s", serverPackURL)

		if a.client != nil {
			a.client.ReportModpackProgress(&api.ModpackProgressRequest{
				ServerUUID: cmd.ServerUUID,
				Stage:      "downloading_server_pack",
				Message:    "Downloading server pack...",
				Progress:   10,
				Total:      100,
			})
		}

		// Download and extract server pack
		serverPackInstaller := minecraft.NewModpackInstaller(filepath.Join(a.config.DataDir, "cache"), curseforgeAPIKey)
		serverPackInstaller.SetProgressCallback(func(stage, message string, progress, total int, currentFile string) {
			log.Printf("[Progress] Stage: %s, Progress: %d/%d, File: %s", stage, progress, total, currentFile)
			if a.client != nil {
				a.client.ReportModpackProgress(&api.ModpackProgressRequest{
					ServerUUID:  cmd.ServerUUID,
					Stage:       stage,
					Message:     message,
					Progress:    progress,
					Total:       total,
					CurrentFile: currentFile,
				})
			}
		})

		err := serverPackInstaller.InstallServerPack(serverPackURL, serverPackFilename, serverDir)
		if err != nil {
			log.Printf("Warning: Failed to install server pack: %v, falling back to manual install", err)
			useServerPack = false // Fall through to manual installation
		} else {
			log.Printf("Server pack installed successfully")
			// Server pack includes the loader, find the run script or jar
			// Check for startserver.sh first (used by ATM and similar modpacks)
			startScript := filepath.Join(serverDir, "startserver.sh")
			runScript := filepath.Join(serverDir, "run.sh")
			if _, err := os.Stat(startScript); err == nil {
				os.Chmod(startScript, 0755)
				jarPath = startScript
				log.Printf("Found startserver.sh for server pack")
			} else if _, err := os.Stat(runScript); err == nil {
				os.Chmod(runScript, 0755)
				jarPath = runScript
				log.Printf("Found run.sh for server pack")
			} else {
				// Look for server jar
				patterns := []string{"forge-*.jar", "neoforge-*.jar", "fabric-*.jar", "server.jar"}
				for _, pattern := range patterns {
					matches, _ := filepath.Glob(filepath.Join(serverDir, pattern))
					for _, m := range matches {
						if !strings.Contains(filepath.Base(m), "installer") {
							jarPath = m
							break
						}
					}
					if jarPath != "" {
						break
					}
				}
			}
		}
	}

	// Fall back to manual installation if server pack not available or failed
	if !useServerPack {
		// Install the modpack with progress reporting
		modpackInstaller := minecraft.NewModpackInstaller(filepath.Join(a.config.DataDir, "cache"), curseforgeAPIKey)

		// Set up progress callback to report to cloud
		modpackInstaller.SetProgressCallback(func(stage, message string, progress, total int, currentFile string) {
			log.Printf("[Progress] Stage: %s, Progress: %d/%d, File: %s", stage, progress, total, currentFile)
			if a.client != nil {
				err := a.client.ReportModpackProgress(&api.ModpackProgressRequest{
					ServerUUID:  cmd.ServerUUID,
					Stage:       stage,
					Message:     message,
					Progress:    progress,
					Total:       total,
					CurrentFile: currentFile,
				})
				if err != nil {
					log.Printf("[Progress] Failed to report progress: %v", err)
				}
			}
		})

		// Create download info if we have a direct URL
		var downloadInfo *minecraft.ModpackDownloadInfo
		if downloadURL != "" {
			downloadInfo = &minecraft.ModpackDownloadInfo{
				URL:      downloadURL,
				Filename: filename,
			}
		}

		modpackInfo, err := modpackInstaller.InstallModpack(versionID, serverDir, downloadInfo)
		if err != nil {
			return fmt.Errorf("failed to install modpack: %w", err)
		}

		// Determine loader info from modpack
		newMcVersion := modpackInfo.GetMinecraftVersion()
		loaderType := modpackInfo.GetLoaderType()
		loaderVersion := modpackInfo.GetLoaderVersion()

		if newMcVersion == "" {
			newMcVersion = mcVersion
		}
		if loaderType == "" {
			loaderType = loader
		}
		mcVersion = newMcVersion

		log.Printf("Modpack requires: Minecraft %s, %s %s", newMcVersion, loaderType, loaderVersion)

		// Update server type based on actual loader from modpack
		switch loaderType {
		case "fabric":
			serverType = minecraft.TypeFabric
		case "forge":
			serverType = minecraft.TypeForge
		case "neoforge":
			serverType = minecraft.TypeNeoForge
		case "quilt":
			serverType = minecraft.TypeFabric
		}

		// Report loader installation progress
		if a.client != nil {
			a.client.ReportModpackProgress(&api.ModpackProgressRequest{
				ServerUUID: cmd.ServerUUID,
				Stage:      "installing_loader",
				Message:    fmt.Sprintf("Installing %s loader...", loaderType),
				Progress:   85,
				Total:      100,
			})
		}

		// Download the loader JAR for the modpack
		log.Printf("Downloading %s server for Minecraft %s (loader version: %s)...", loaderType, newMcVersion, loaderVersion)
		downloader := minecraft.NewDownloader(filepath.Join(a.config.DataDir, "cache"))

		jarPath, err = downloader.DownloadServerWithLoader(serverType, newMcVersion, loaderVersion, serverDir)
		if err != nil {
			log.Printf("Warning: Failed to download %s server: %v, trying alternative...", loaderType, err)
			jarPath, err = downloader.DownloadServer(serverType, newMcVersion, serverDir)
			if err != nil {
				if a.client != nil {
					a.client.ReportModpackProgress(&api.ModpackProgressRequest{
						ServerUUID: cmd.ServerUUID,
						Stage:      "error",
						Message:    fmt.Sprintf("Failed to download %s: %v", loaderType, err),
						Progress:   0,
						Total:      100,
					})
				}
				return fmt.Errorf("failed to download server JAR: %w", err)
			}
		}
	}

	log.Printf("Server JAR/script: %s", jarPath)

	// Update server config
	server.Type = serverType
	server.MinecraftVersion = mcVersion

	// Report completion
	if a.client != nil {
		a.client.ReportModpackProgress(&api.ModpackProgressRequest{
			ServerUUID: cmd.ServerUUID,
			Stage:      "complete",
			Message:    "Modpack installed successfully!",
			Progress:   100,
			Total:      100,
		})
	}

	log.Printf("Modpack changed successfully on server %s! (%s %s)",
		server.Name, loader, mcVersion)
	return nil
}

func (a *Agent) getSystemInfo() map[string]interface{} {
	hostname, _ := os.Hostname()

	info := map[string]interface{}{
		"hostname": hostname,
		"os":       a.getOSInfo(),
	}

	// Get total RAM
	if ramBytes := a.getTotalRAM(); ramBytes > 0 {
		info["ram_total_mb"] = ramBytes / (1024 * 1024)
	}

	return info
}

// getOSInfo reads /etc/os-release to get OS name and version
func (a *Agent) getOSInfo() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "Linux"
	}

	var prettyName string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			prettyName = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
			break
		}
	}

	if prettyName != "" {
		return prettyName
	}
	return "Linux"
}

// getTotalRAM reads /proc/meminfo to get total RAM in bytes
func (a *Agent) getTotalRAM() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				var kb int64
				fmt.Sscanf(fields[1], "%d", &kb)
				return kb * 1024 // Convert KB to bytes
			}
		}
	}
	return 0
}

// sendConsoleOutput sends console lines to the cloud API
func (a *Agent) sendConsoleOutput(serverUUID string, lines []string) error {
	return a.client.SendConsoleOutput(&api.ConsoleRequest{
		ServerUUID: serverUUID,
		Lines:      lines,
	})
}

// createConsoleBuffer creates a console buffer for a server
func (a *Agent) createConsoleBuffer(serverUUID string) *minecraft.ConsoleBuffer {
	a.consoleMu.Lock()
	defer a.consoleMu.Unlock()

	// Check if buffer already exists
	if buf, ok := a.consoleBuffers[serverUUID]; ok {
		return buf
	}

	buf := minecraft.NewConsoleBuffer(serverUUID, a.sendConsoleOutput)
	a.consoleBuffers[serverUUID] = buf
	return buf
}

// stopConsoleBuffer stops and removes a console buffer
func (a *Agent) stopConsoleBuffer(serverUUID string) {
	a.consoleMu.Lock()
	defer a.consoleMu.Unlock()

	if buf, ok := a.consoleBuffers[serverUUID]; ok {
		buf.Stop()
		delete(a.consoleBuffers, serverUUID)
	}
}

// createPlayerEventCallback creates a callback function for player join/leave events
func (a *Agent) createPlayerEventCallback(serverUUID string) func(event, playerName, playerUUID string) {
	return func(event, playerName, playerUUID string) {
		if a.client == nil {
			return
		}
		// If UUID is empty, generate a placeholder based on player name
		// The backend will look up or create the player by name
		if playerUUID == "" {
			playerUUID = "00000000-0000-0000-0000-000000000000"
		}
		err := a.client.ReportPlayerEvent(&api.PlayerEventRequest{
			ServerUUID: serverUUID,
			Event:      event,
			PlayerUUID: playerUUID,
			PlayerName: playerName,
		})
		if err != nil {
			log.Printf("Failed to report player event: %v", err)
		}
	}
}

// installPlugin downloads and installs a plugin to the server's plugins folder
func (a *Agent) installPlugin(cmd api.Command, server *minecraft.Server) error {
	if cmd.Payload == nil {
		return fmt.Errorf("install_plugin requires payload")
	}

	pluginUUID, _ := cmd.Payload["plugin_uuid"].(string)
	filename, _ := cmd.Payload["filename"].(string)

	if pluginUUID == "" || filename == "" {
		return fmt.Errorf("plugin_uuid and filename are required")
	}

	log.Printf("Installing plugin %s to server %s", filename, server.Name)

	// Create plugins directory
	pluginsDir := filepath.Join(server.DataDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		return fmt.Errorf("failed to create plugins directory: %w", err)
	}

	// Download plugin from cloud
	destPath := filepath.Join(pluginsDir, filename)
	if err := a.client.DownloadPlugin(pluginUUID, destPath); err != nil {
		return fmt.Errorf("failed to download plugin: %w", err)
	}

	log.Printf("Plugin %s installed successfully!", filename)
	return nil
}

// deletePlugin removes a plugin from the server's plugins folder
func (a *Agent) deletePlugin(cmd api.Command, server *minecraft.Server) error {
	if cmd.Payload == nil {
		return fmt.Errorf("delete_plugin requires payload")
	}

	filename, _ := cmd.Payload["filename"].(string)
	if filename == "" {
		return fmt.Errorf("filename is required")
	}

	log.Printf("Deleting plugin %s from server %s", filename, server.Name)

	pluginPath := filepath.Join(server.DataDir, "plugins", filename)
	if err := os.Remove(pluginPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete plugin: %w", err)
	}

	log.Printf("Plugin %s deleted successfully!", filename)
	return nil
}

// updatePlugin updates a plugin by downloading from Modrinth and replacing the old file
func (a *Agent) updatePlugin(cmd api.Command, server *minecraft.Server) error {
	if cmd.Payload == nil {
		return fmt.Errorf("update_plugin requires payload")
	}

	oldFilename, _ := cmd.Payload["old_filename"].(string)
	newFilename, _ := cmd.Payload["new_filename"].(string)
	downloadURL, _ := cmd.Payload["download_url"].(string)

	if newFilename == "" || downloadURL == "" {
		return fmt.Errorf("new_filename and download_url are required")
	}

	log.Printf("Updating plugin on server %s: %s -> %s", server.Name, oldFilename, newFilename)

	pluginsDir := filepath.Join(server.DataDir, "plugins")

	// Delete old plugin file if it has a different name
	if oldFilename != "" && oldFilename != newFilename {
		oldPath := filepath.Join(pluginsDir, oldFilename)
		if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
			log.Printf("Warning: Failed to remove old plugin file %s: %v", oldFilename, err)
		} else {
			log.Printf("Removed old plugin file: %s", oldFilename)
		}
	}

	// Download new plugin directly from Modrinth
	destPath := filepath.Join(pluginsDir, newFilename)

	// Create HTTP client for downloading
	client := &http.Client{Timeout: 120 * time.Second}
	req, err := http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create download request: %w", err)
	}

	// Set User-Agent header for Modrinth API compliance
	req.Header.Set("User-Agent", "RedstoneCore-Agent/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download plugin: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download plugin: HTTP %d", resp.StatusCode)
	}

	// Create destination file
	destFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create plugin file: %w", err)
	}
	defer destFile.Close()

	// Copy the content
	_, err = io.Copy(destFile, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write plugin file: %w", err)
	}

	log.Printf("Plugin updated successfully: %s", newFilename)
	return nil
}

// updateProperties updates the server.properties file
func (a *Agent) updateProperties(cmd api.Command, server *minecraft.Server) error {
	if cmd.Payload == nil {
		return fmt.Errorf("update_properties requires payload")
	}

	propertiesRaw, ok := cmd.Payload["properties"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("properties must be a map")
	}

	// Convert to map[string]string
	properties := make(map[string]string)
	for k, v := range propertiesRaw {
		properties[k] = fmt.Sprintf("%v", v)
	}

	log.Printf("Updating server.properties for %s (%d properties)", server.Name, len(properties))

	if err := server.UpdateProperties(properties); err != nil {
		return fmt.Errorf("failed to update properties: %w", err)
	}

	log.Printf("Properties updated for server %s", server.Name)
	return nil
}

// changeServerType downloads a new server JAR for a different server type
func (a *Agent) changeServerType(cmd api.Command, server *minecraft.Server) error {
	if cmd.Payload == nil {
		return fmt.Errorf("change_server_type requires payload")
	}

	newType, _ := cmd.Payload["new_type"].(string)
	version, _ := cmd.Payload["minecraft_version"].(string)

	if newType == "" || version == "" {
		return fmt.Errorf("new_type and minecraft_version are required")
	}

	// Ensure server is stopped
	if server.Status == minecraft.StatusRunning {
		return fmt.Errorf("server must be stopped before changing type")
	}

	log.Printf("Changing server %s type from %s to %s (version %s)", server.Name, server.Type, newType, version)

	// Check if this is a modpack server with startserver.sh (server pack)
	// If so, skip downloading - the server pack already has the correct loader
	startScript := filepath.Join(server.DataDir, "startserver.sh")
	if _, err := os.Stat(startScript); err == nil {
		log.Printf("Server pack detected (startserver.sh exists), skipping loader download")
		server.Type = minecraft.ServerType(newType)
		server.MinecraftVersion = version
		log.Printf("Server type updated to %s (using existing server pack)", newType)
		return nil
	}

	// Download new server JAR
	downloader := minecraft.NewDownloader(filepath.Join(a.config.DataDir, "cache"))
	jarPath, err := downloader.DownloadServer(minecraft.ServerType(newType), version, server.DataDir)
	if err != nil {
		return fmt.Errorf("failed to download new server JAR: %w", err)
	}

	// Update server config
	server.Type = minecraft.ServerType(newType)
	server.MinecraftVersion = version

	log.Printf("Server type changed successfully! New JAR: %s", jarPath)
	return nil
}

// syncServerProperties reads server.properties and syncs to cloud
func (a *Agent) syncServerProperties(server *minecraft.Server) error {
	properties, err := server.ReadProperties()
	if err != nil {
		log.Printf("Failed to read properties for %s: %v", server.Name, err)
		return err
	}

	log.Printf("Syncing %d properties for server %s", len(properties), server.Name)

	err = a.client.SyncProperties(&api.PropertiesRequest{
		ServerUUID: server.UUID,
		Properties: properties,
	})
	if err != nil {
		log.Printf("Failed to sync properties for %s: %v", server.Name, err)
	}
	return err
}

// updateProxyConfig writes the proxy configuration file (BungeeCord config.yml or Velocity velocity.toml)
func (a *Agent) updateProxyConfig(cmd api.Command, server *minecraft.Server) error {
	if cmd.Payload == nil {
		return fmt.Errorf("update_proxy_config requires payload")
	}

	configContent, _ := cmd.Payload["config_content"].(string)
	configFilename, _ := cmd.Payload["config_filename"].(string)

	if configContent == "" || configFilename == "" {
		return fmt.Errorf("config_content and config_filename are required")
	}

	log.Printf("Updating proxy config for %s: %s", server.Name, configFilename)

	// Write the config file to the server's data directory
	configPath := filepath.Join(server.DataDir, configFilename)

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		return fmt.Errorf("failed to write proxy config: %w", err)
	}

	log.Printf("Proxy config %s updated successfully for server %s", configFilename, server.Name)

	// If the server is running, send a reload command
	if server.Status == minecraft.StatusRunning {
		configType, _ := cmd.Payload["config_type"].(string)
		var reloadCmd string
		if configType == "velocity" {
			reloadCmd = "velocity reload"
		} else {
			reloadCmd = "greload"
		}
		log.Printf("Server is running, sending reload command: %s", reloadCmd)
		if err := server.SendCommand(reloadCmd); err != nil {
			log.Printf("Warning: Failed to send reload command: %v", err)
			// Don't fail the whole operation if reload fails
		}
	}

	return nil
}

// configureVelocityForwarding configures a backend server for Velocity modern forwarding
func (a *Agent) configureVelocityForwarding(cmd api.Command, server *minecraft.Server) error {
	if cmd.Payload == nil {
		return fmt.Errorf("configure_velocity_forwarding requires payload")
	}

	forwardingSecret, _ := cmd.Payload["forwarding_secret"].(string)
	serverType, _ := cmd.Payload["server_type"].(string)

	if forwardingSecret == "" {
		return fmt.Errorf("forwarding_secret is required")
	}

	log.Printf("Configuring Velocity forwarding for %s (type: %s)", server.Name, serverType)

	// Create config directory if it doesn't exist
	configDir := filepath.Join(server.DataDir, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Write paper-global.yml with velocity forwarding enabled
	paperGlobalPath := filepath.Join(configDir, "paper-global.yml")

	// Check if file exists and read it
	var config map[string]interface{}
	if data, err := os.ReadFile(paperGlobalPath); err == nil {
		if err := yaml.Unmarshal(data, &config); err != nil {
			log.Printf("Warning: Failed to parse existing paper-global.yml, creating new: %v", err)
			config = make(map[string]interface{})
		}
	} else {
		config = make(map[string]interface{})
	}

	// Ensure proxies section exists
	if config["proxies"] == nil {
		config["proxies"] = make(map[string]interface{})
	}
	proxies, ok := config["proxies"].(map[string]interface{})
	if !ok {
		proxies = make(map[string]interface{})
		config["proxies"] = proxies
	}

	// Set velocity configuration
	proxies["velocity"] = map[string]interface{}{
		"enabled":     true,
		"online-mode": true,
		"secret":      forwardingSecret,
	}

	// Write the config back
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal paper-global.yml: %w", err)
	}

	if err := os.WriteFile(paperGlobalPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write paper-global.yml: %w", err)
	}

	log.Printf("Velocity forwarding configured successfully for server %s", server.Name)

	// Also update server.properties to use offline-mode (Velocity handles auth)
	propsPath := filepath.Join(server.DataDir, "server.properties")
	if propsData, err := os.ReadFile(propsPath); err == nil {
		propsStr := string(propsData)
		modified := false

		if strings.Contains(propsStr, "online-mode=true") {
			// Replace online-mode=true with online-mode=false
			propsStr = strings.Replace(propsStr, "online-mode=true", "online-mode=false", 1)
			modified = true
		} else if !strings.Contains(propsStr, "online-mode=") {
			// Add online-mode=false if not present
			propsStr = strings.TrimRight(propsStr, "\n") + "\nonline-mode=false\n"
			modified = true
		}

		if modified {
			if err := os.WriteFile(propsPath, []byte(propsStr), 0644); err != nil {
				log.Printf("Warning: Failed to update server.properties: %v", err)
			} else {
				log.Printf("Updated server.properties to use online-mode=false")
			}
		}
	}

	return nil
}
