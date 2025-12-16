package agent

import (
	"crypto/rand"
	"fmt"
	"log"
	"os"
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
)

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

	// Start heartbeat (5 second interval for responsive commands)
	a.heartbeat = heartbeat.New(a.client, a, 5*time.Second)
	a.heartbeat.Start()

	// Start SFTP relay client
	if a.config.SFTPRelayURL != "" {
		sftpHandler := sftp.NewDefaultHandler(a.getServerDataDir)
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

	req := &api.RegisterRequest{
		LicenseKey: a.config.LicenseKey,
		Name:       hostname,
		Hostname:   hostname,
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
		})

		a.serversMu.Lock()
		a.servers[uuid] = server
		a.serversMu.Unlock()

		log.Printf("Discovered existing server: %s (%s)", entry.Name(), uuid)
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
	a.serversMu.RLock()
	defer a.serversMu.RUnlock()

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
		})
	}

	if len(servers) == 0 {
		return nil
	}

	resp, err := a.client.SyncServers(&api.SyncServersRequest{Servers: servers})
	if err != nil {
		return err
	}

	// Update UUIDs from cloud response (in case they were assigned by cloud)
	for _, syncedServer := range resp.Servers {
		for uuid, server := range a.servers {
			if server.Name == syncedServer.Name && uuid != syncedServer.UUID {
				// UUID was assigned by cloud, update our reference
				a.servers[syncedServer.UUID] = server
				server.UUID = syncedServer.UUID
				delete(a.servers, uuid)
				break
			}
		}
	}

	log.Printf("Synced %d servers to cloud", len(servers))
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
	case "delete_plugin":
		return a.deletePlugin(cmd, server)
	case "update_properties":
		return a.updateProperties(cmd, server)
	case "change_server_type":
		return a.changeServerType(cmd, server)
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

	if name == "" || versionID == "" {
		return fmt.Errorf("server name and modpack version_id are required")
	}

	log.Printf("Creating modpack server: %s (modpack: %s, loader: %s)", name, modpackName, loader)

	// Create server directory
	serverDir := filepath.Join(a.config.DataDir, "servers", name)
	if err := os.MkdirAll(serverDir, 0755); err != nil {
		return fmt.Errorf("failed to create server directory: %w", err)
	}

	// Install the modpack
	modpackInstaller := minecraft.NewModpackInstaller(filepath.Join(a.config.DataDir, "cache"))
	index, err := modpackInstaller.InstallModpack(versionID, serverDir)
	if err != nil {
		return fmt.Errorf("failed to install modpack: %w", err)
	}

	// Determine Minecraft version and loader from modpack
	mcVersion := index.GetMinecraftVersion()
	loaderType := index.GetLoaderType()
	loaderVersion := index.GetLoaderVersion()

	if mcVersion == "" {
		mcVersion = "1.20.1" // Fallback
	}
	if loaderType == "" {
		loaderType = loader // Use the loader from payload if not in index
	}

	log.Printf("Modpack requires: Minecraft %s, %s %s", mcVersion, loaderType, loaderVersion)

	// Download the appropriate server JAR based on loader
	downloader := minecraft.NewDownloader(filepath.Join(a.config.DataDir, "cache"))
	var serverType minecraft.ServerType

	switch loaderType {
	case "fabric":
		serverType = minecraft.TypeFabric
	case "forge":
		serverType = minecraft.TypeForge
	case "neoforge":
		serverType = minecraft.TypeForge // NeoForge uses similar installer
	case "quilt":
		serverType = minecraft.TypeFabric // Quilt is Fabric-compatible for server
	default:
		serverType = minecraft.TypeFabric // Default to Fabric
	}

	jarPath, err := downloader.DownloadServerWithLoader(serverType, mcVersion, loaderVersion, serverDir)
	if err != nil {
		log.Printf("Warning: Failed to download %s server: %v, trying alternative...", loaderType, err)
		// Fallback to just the loader without specific version
		jarPath, err = downloader.DownloadServer(serverType, mcVersion, serverDir)
		if err != nil {
			return fmt.Errorf("failed to download server JAR: %w", err)
		}
	}

	log.Printf("Downloaded server JAR: %s", jarPath)

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

	log.Printf("Modpack server %s created successfully! (%s %s with %d mods)",
		name, loaderType, mcVersion, len(index.Files))
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
	mcVersion, _ := cmd.Payload["minecraft_version"].(string)

	if versionID == "" {
		return fmt.Errorf("modpack version_id is required")
	}

	log.Printf("Changing modpack on server %s to: %s (loader: %s)", server.Name, modpackName, loader)

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

	// Install the new modpack with progress reporting
	modpackInstaller := minecraft.NewModpackInstaller(filepath.Join(a.config.DataDir, "cache"))

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
		} else {
			log.Printf("[Progress] Warning: client is nil, cannot report progress")
		}
	})

	index, err := modpackInstaller.InstallModpack(versionID, serverDir)
	if err != nil {
		return fmt.Errorf("failed to install modpack: %w", err)
	}

	// Determine loader info from modpack
	newMcVersion := index.GetMinecraftVersion()
	loaderType := index.GetLoaderType()
	loaderVersion := index.GetLoaderVersion()

	if newMcVersion == "" {
		newMcVersion = mcVersion
	}
	if loaderType == "" {
		loaderType = loader
	}

	log.Printf("Modpack requires: Minecraft %s, %s %s", newMcVersion, loaderType, loaderVersion)

	// Determine server type based on loader
	var serverType minecraft.ServerType
	switch loaderType {
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

	// Always download the loader JAR for the modpack
	log.Printf("Downloading %s server for Minecraft %s (loader version: %s)...", loaderType, newMcVersion, loaderVersion)
	downloader := minecraft.NewDownloader(filepath.Join(a.config.DataDir, "cache"))

	jarPath, err := downloader.DownloadServerWithLoader(serverType, newMcVersion, loaderVersion, serverDir)
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
	log.Printf("Downloaded server: %s", jarPath)

	// Update server config
	server.Type = serverType
	server.MinecraftVersion = newMcVersion

	// Report completion
	if a.client != nil {
		a.client.ReportModpackProgress(&api.ModpackProgressRequest{
			ServerUUID: cmd.ServerUUID,
			Stage:      "complete",
			Message:    fmt.Sprintf("Modpack installed successfully! (%d mods)", len(index.Files)),
			Progress:   100,
			Total:      100,
		})
	}

	log.Printf("Modpack changed successfully on server %s! (%s %s with %d mods)",
		server.Name, loaderType, newMcVersion, len(index.Files))
	return nil
}

func (a *Agent) getSystemInfo() map[string]interface{} {
	hostname, _ := os.Hostname()

	return map[string]interface{}{
		"hostname": hostname,
		"os":       "linux",
		// Could add more system info here (CPU, RAM, etc.)
	}
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
		return err
	}

	return a.client.SyncProperties(&api.PropertiesRequest{
		ServerUUID: server.UUID,
		Properties: properties,
	})
}
