package minecraft

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ServerType string

const (
	TypeVanilla    ServerType = "vanilla"
	TypePaper      ServerType = "paper"
	TypeSpigot     ServerType = "spigot"
	TypeForge      ServerType = "forge"
	TypeNeoForge   ServerType = "neoforge"
	TypeFabric     ServerType = "fabric"
	TypeBungeeCord ServerType = "bungeecord"
	TypeVelocity   ServerType = "velocity"
	TypeOther      ServerType = "other"
)

type ServerStatus string

const (
	StatusRunning  ServerStatus = "running"
	StatusStopped  ServerStatus = "stopped"
	StatusStarting ServerStatus = "starting"
	StatusStopping ServerStatus = "stopping"
	StatusCrashed  ServerStatus = "crashed"
	StatusUnknown  ServerStatus = "unknown"
)

type Server struct {
	UUID             string
	Name             string
	Type             ServerType
	MinecraftVersion string
	Port             int
	MaxPlayers       int
	AllocatedRAM     int
	Status           ServerStatus
	PlayerCount      int
	CurrentPlayers   []string
	DataDir          string
	JarFile          string

	// Metrics
	TPS          float64
	MemoryUsedMB int
	CPUPercent   float64

	cmd              *exec.Cmd
	stdin            io.WriteCloser
	consoleBuffer    []string
	consoleMutex     sync.Mutex
	playersMutex     sync.Mutex
	metricsMutex     sync.Mutex
	onConsoleLine    func(line string)
	onPlayerEvent    func(event, playerName, playerUUID string)
	stopChan         chan struct{}
	lastCPUTime      uint64
	lastCPUCheckTime time.Time
}

type ServerConfig struct {
	UUID             string
	Name             string
	Type             ServerType
	MinecraftVersion string
	Port             int
	MaxPlayers       int
	AllocatedRAM     int
	DataDir          string
	OnConsoleLine    func(line string)
	OnPlayerEvent    func(event, playerName, playerUUID string)
}

func NewServer(cfg ServerConfig) *Server {
	return &Server{
		UUID:             cfg.UUID,
		Name:             cfg.Name,
		Type:             cfg.Type,
		MinecraftVersion: cfg.MinecraftVersion,
		Port:             cfg.Port,
		MaxPlayers:       cfg.MaxPlayers,
		AllocatedRAM:     cfg.AllocatedRAM,
		DataDir:          cfg.DataDir,
		Status:           StatusStopped,
		PlayerCount:      0,
		CurrentPlayers:   make([]string, 0),
		consoleBuffer:    make([]string, 0, 1000),
		onConsoleLine:    cfg.OnConsoleLine,
		onPlayerEvent:    cfg.OnPlayerEvent,
	}
}

func (s *Server) Start() error {
	if s.Status == StatusRunning || s.Status == StatusStarting {
		return fmt.Errorf("server is already running or starting")
	}

	s.Status = StatusStarting

	// Find the JAR file or run script
	jarPath, err := s.findJarFile()
	if err != nil {
		s.Status = StatusStopped
		return fmt.Errorf("JAR file not found: %w", err)
	}
	s.JarFile = jarPath

	// Accept EULA if needed
	if err := s.acceptEULA(); err != nil {
		s.Status = StatusStopped
		return fmt.Errorf("failed to accept EULA: %w", err)
	}

	// Check if this is a run script (Forge/NeoForge) or a JAR file
	isRunScript := strings.HasSuffix(jarPath, ".sh")

	if isRunScript {
		// For run.sh, update user_jvm_args.txt with memory settings
		jvmArgsPath := filepath.Join(s.DataDir, "user_jvm_args.txt")
		jvmArgs := fmt.Sprintf("# Auto-configured by RedstoneCore\n-Xmx%dM\n-Xms%dM\n", s.AllocatedRAM, s.AllocatedRAM/2)
		os.WriteFile(jvmArgsPath, []byte(jvmArgs), 0644)

		// Execute run.sh directly
		s.cmd = exec.Command("/bin/bash", jarPath, "--nogui")
	} else {
		// Build Java command for JAR files
		javaArgs := []string{
			fmt.Sprintf("-Xmx%dM", s.AllocatedRAM),
			fmt.Sprintf("-Xms%dM", s.AllocatedRAM/2),
			"-jar", jarPath,
			"--nogui",
		}
		s.cmd = exec.Command("java", javaArgs...)
	}

	s.cmd.Dir = s.DataDir

	// Set up pipes for stdin/stdout/stderr
	stdin, err := s.cmd.StdinPipe()
	if err != nil {
		s.Status = StatusStopped
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}
	s.stdin = stdin

	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		s.Status = StatusStopped
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	stderr, err := s.cmd.StderrPipe()
	if err != nil {
		s.Status = StatusStopped
		return fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	// Start the process
	if err := s.cmd.Start(); err != nil {
		s.Status = StatusStopped
		return fmt.Errorf("failed to start server: %w", err)
	}

	s.stopChan = make(chan struct{})

	// Read stdout/stderr in goroutines
	go s.readOutput(stdout)
	go s.readOutput(stderr)

	// Monitor process exit
	go s.monitorProcess()

	s.Status = StatusRunning
	return nil
}

func (s *Server) Stop() error {
	if s.Status != StatusRunning {
		return fmt.Errorf("server is not running")
	}

	s.Status = StatusStopping

	// Send "stop" command to Minecraft
	if err := s.SendCommand("stop"); err != nil {
		// If we can't send stop command, try to kill
		return s.Kill()
	}

	// Wait for graceful shutdown (max 30 seconds)
	select {
	case <-s.stopChan:
		// Process exited
	case <-time.After(30 * time.Second):
		// Force kill after timeout
		return s.Kill()
	}

	s.Status = StatusStopped
	return nil
}

func (s *Server) Kill() error {
	if s.cmd == nil || s.cmd.Process == nil {
		s.Status = StatusStopped
		return nil
	}

	if err := s.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("failed to kill server: %w", err)
	}

	s.Status = StatusStopped
	return nil
}

func (s *Server) Restart() error {
	if err := s.Stop(); err != nil {
		// If stop fails, try to kill
		s.Kill()
	}

	// Wait a bit before restarting
	time.Sleep(2 * time.Second)

	return s.Start()
}

func (s *Server) SendCommand(cmd string) error {
	if s.stdin == nil {
		return fmt.Errorf("server stdin not available")
	}

	_, err := fmt.Fprintln(s.stdin, cmd)
	return err
}

func (s *Server) GetConsoleBuffer() []string {
	s.consoleMutex.Lock()
	defer s.consoleMutex.Unlock()

	result := make([]string, len(s.consoleBuffer))
	copy(result, s.consoleBuffer)
	return result
}

func (s *Server) GetCurrentPlayers() []string {
	s.playersMutex.Lock()
	defer s.playersMutex.Unlock()

	result := make([]string, len(s.CurrentPlayers))
	copy(result, s.CurrentPlayers)
	return result
}

func (s *Server) readOutput(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()

		s.consoleMutex.Lock()
		s.consoleBuffer = append(s.consoleBuffer, line)
		// Keep buffer at max 1000 lines
		if len(s.consoleBuffer) > 1000 {
			s.consoleBuffer = s.consoleBuffer[len(s.consoleBuffer)-1000:]
		}
		s.consoleMutex.Unlock()

		// Parse player count from console output
		s.parseConsoleLine(line)

		// Call callback if set
		if s.onConsoleLine != nil {
			s.onConsoleLine(line)
		}
	}
}

// TPS regex patterns for various server types
var (
	// Paper/Spigot: "TPS from last 1m, 5m, 15m: 20.0, 20.0, 20.0" or "TPS from last 1m, 5m, 15m: *20.0, *20.0, *20.0"
	tpsRegex = regexp.MustCompile(`TPS.*?:\s*\*?([\d.]+)`)
	// Spark plugin: various TPS formats
	sparkTPSRegex = regexp.MustCompile(`(?:TPS|tps).*?([\d.]+)`)
	// MSPT (milliseconds per tick) - Paper's /mspt command
	msptRegex = regexp.MustCompile(`MSPT.*?:\s*([\d.]+)`)
)

func (s *Server) parseConsoleLine(line string) {
	// Parse TPS from console output
	s.parseTPS(line)

	// Parse player join/leave messages to update player count and names
	// Format: "[HH:MM:SS] [Server thread/INFO] [minecraft/MinecraftServer]: PlayerName joined the game"
	if strings.Contains(line, "joined the game") {
		// Extract player name - it's the word before "joined the game"
		parts := strings.Split(line, " joined the game")
		if len(parts) > 0 {
			// Get the last word before "joined the game" which is the player name
			words := strings.Fields(parts[0])
			if len(words) > 0 {
				playerName := words[len(words)-1]
				// Clean up any trailing colons or brackets
				playerName = strings.TrimSuffix(playerName, ":")

				s.playersMutex.Lock()
				// Add player if not already in list
				found := false
				for _, p := range s.CurrentPlayers {
					if p == playerName {
						found = true
						break
					}
				}
				if !found {
					s.CurrentPlayers = append(s.CurrentPlayers, playerName)
				}
				s.PlayerCount = len(s.CurrentPlayers)
				s.playersMutex.Unlock()

				// Notify callback about player join
				if s.onPlayerEvent != nil {
					s.onPlayerEvent("join", playerName, "")
				}
			}
		}
	} else if strings.Contains(line, "left the game") {
		// Extract player name
		parts := strings.Split(line, " left the game")
		if len(parts) > 0 {
			words := strings.Fields(parts[0])
			if len(words) > 0 {
				playerName := words[len(words)-1]
				playerName = strings.TrimSuffix(playerName, ":")

				s.playersMutex.Lock()
				// Remove player from list
				for i, p := range s.CurrentPlayers {
					if p == playerName {
						s.CurrentPlayers = append(s.CurrentPlayers[:i], s.CurrentPlayers[i+1:]...)
						break
					}
				}
				s.PlayerCount = len(s.CurrentPlayers)
				s.playersMutex.Unlock()

				// Notify callback about player leave
				if s.onPlayerEvent != nil {
					s.onPlayerEvent("leave", playerName, "")
				}
			}
		}
	}

	// Detect server ready
	if strings.Contains(line, "Done") && strings.Contains(line, "For help, type") {
		s.Status = StatusRunning
	}
}

func (s *Server) monitorProcess() {
	if s.cmd == nil {
		return
	}

	err := s.cmd.Wait()

	close(s.stopChan)

	if s.Status == StatusStopping {
		s.Status = StatusStopped
	} else if err != nil {
		s.Status = StatusCrashed
	} else {
		s.Status = StatusStopped
	}

	s.stdin = nil
	s.cmd = nil
}

func (s *Server) findJarFile() (string, error) {
	// First check for startserver.sh (used by modpack server packs like ATM)
	startScript := filepath.Join(s.DataDir, "startserver.sh")
	if _, err := os.Stat(startScript); err == nil {
		return startScript, nil
	}

	// Check for run.sh (used by modern Forge/NeoForge)
	runScript := filepath.Join(s.DataDir, "run.sh")
	if _, err := os.Stat(runScript); err == nil {
		return runScript, nil
	}

	// Look for server jar in data directory
	patterns := []string{
		"server.jar",
		"paper*.jar",
		"spigot*.jar",
		"forge*.jar",
		"neoforge*.jar",
		"fabric*.jar",
		"bungeecord*.jar",
		"velocity*.jar",
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(s.DataDir, pattern))
		if err == nil && len(matches) > 0 {
			// Skip installer jars
			for _, match := range matches {
				if !strings.Contains(filepath.Base(match), "installer") {
					return match, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no server JAR or run script found in %s", s.DataDir)
}

func (s *Server) acceptEULA() error {
	eulaPath := filepath.Join(s.DataDir, "eula.txt")

	// Check if eula.txt exists
	if _, err := os.Stat(eulaPath); os.IsNotExist(err) {
		// Create eula.txt with eula=true
		return os.WriteFile(eulaPath, []byte("eula=true\n"), 0644)
	}

	// Read existing file
	data, err := os.ReadFile(eulaPath)
	if err != nil {
		return err
	}

	content := string(data)
	if strings.Contains(content, "eula=true") {
		return nil
	}

	// Replace eula=false with eula=true
	content = strings.ReplaceAll(content, "eula=false", "eula=true")
	return os.WriteFile(eulaPath, []byte(content), 0644)
}

func (s *Server) EnsureServerProperties() error {
	propsPath := filepath.Join(s.DataDir, "server.properties")

	// Check if server.properties exists
	if _, err := os.Stat(propsPath); err == nil {
		// Update port and max-players if they differ
		data, err := os.ReadFile(propsPath)
		if err != nil {
			return err
		}

		content := string(data)
		lines := strings.Split(content, "\n")
		newLines := make([]string, 0, len(lines))

		for _, line := range lines {
			if strings.HasPrefix(line, "server-port=") {
				line = fmt.Sprintf("server-port=%d", s.Port)
			} else if strings.HasPrefix(line, "max-players=") {
				line = fmt.Sprintf("max-players=%d", s.MaxPlayers)
			}
			newLines = append(newLines, line)
		}

		return os.WriteFile(propsPath, []byte(strings.Join(newLines, "\n")), 0644)
	}

	// Create default server.properties
	props := fmt.Sprintf(`server-port=%d
max-players=%d
motd=A RedstoneCore Minecraft Server
enable-command-block=true
`, s.Port, s.MaxPlayers)

	return os.WriteFile(propsPath, []byte(props), 0644)
}

// ReadProperties reads server.properties and returns as a map
func (s *Server) ReadProperties() (map[string]string, error) {
	propsPath := filepath.Join(s.DataDir, "server.properties")

	data, err := os.ReadFile(propsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), nil
		}
		return nil, err
	}

	props := make(map[string]string)
	lines := strings.Split(string(data), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Split on first =
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			props[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}

	return props, nil
}

// LoadServerProperties reads server.properties from a directory without needing a Server instance
func LoadServerProperties(serverDir string) (map[string]string, error) {
	propsPath := filepath.Join(serverDir, "server.properties")

	data, err := os.ReadFile(propsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), nil
		}
		return nil, err
	}

	props := make(map[string]string)
	lines := strings.Split(string(data), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Split on first =
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			props[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}

	return props, nil
}

// parseTPS extracts TPS from console output
func (s *Server) parseTPS(line string) {
	// Check for TPS in console output
	if strings.Contains(strings.ToUpper(line), "TPS") {
		if match := tpsRegex.FindStringSubmatch(line); len(match) > 1 {
			if tps, err := strconv.ParseFloat(match[1], 64); err == nil && tps > 0 && tps <= 20 {
				s.metricsMutex.Lock()
				s.TPS = tps
				s.metricsMutex.Unlock()
			}
		}
	}

	// Check for MSPT (convert to approximate TPS: TPS = 1000/MSPT, capped at 20)
	if strings.Contains(strings.ToUpper(line), "MSPT") {
		if match := msptRegex.FindStringSubmatch(line); len(match) > 1 {
			if mspt, err := strconv.ParseFloat(match[1], 64); err == nil && mspt > 0 {
				tps := 1000.0 / mspt
				if tps > 20 {
					tps = 20
				}
				s.metricsMutex.Lock()
				s.TPS = tps
				s.metricsMutex.Unlock()
			}
		}
	}
}

// GetProcessMetrics reads memory and CPU usage from /proc for the Java process
func (s *Server) GetProcessMetrics() (memoryMB int, cpuPercent float64) {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0, 0
	}

	pid := s.cmd.Process.Pid

	// Read memory from /proc/[pid]/status
	memoryMB = s.readProcessMemory(pid)

	// Read CPU from /proc/[pid]/stat
	cpuPercent = s.readProcessCPU(pid)

	// Update stored metrics
	s.metricsMutex.Lock()
	s.MemoryUsedMB = memoryMB
	s.CPUPercent = cpuPercent
	s.metricsMutex.Unlock()

	return memoryMB, cpuPercent
}

// readProcessMemory reads RSS memory from /proc/[pid]/status
func (s *Server) readProcessMemory(pid int) int {
	statusPath := fmt.Sprintf("/proc/%d/status", pid)
	data, err := os.ReadFile(statusPath)
	if err != nil {
		return 0
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "VmRSS:") {
			// Format: "VmRSS:    123456 kB"
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					return int(kb / 1024) // Convert kB to MB
				}
			}
		}
	}

	return 0
}

// readProcessCPU calculates CPU usage percentage since last check
func (s *Server) readProcessCPU(pid int) float64 {
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	data, err := os.ReadFile(statPath)
	if err != nil {
		return 0
	}

	// Parse /proc/[pid]/stat - fields are space-separated
	// Field 14 (utime) and 15 (stime) are CPU times in clock ticks
	fields := strings.Fields(string(data))
	if len(fields) < 15 {
		return 0
	}

	utime, _ := strconv.ParseUint(fields[13], 10, 64)
	stime, _ := strconv.ParseUint(fields[14], 10, 64)
	totalCPUTime := utime + stime

	now := time.Now()

	s.metricsMutex.Lock()
	defer s.metricsMutex.Unlock()

	if s.lastCPUTime == 0 || s.lastCPUCheckTime.IsZero() {
		// First reading, just store values
		s.lastCPUTime = totalCPUTime
		s.lastCPUCheckTime = now
		return 0
	}

	// Calculate CPU percentage
	timeDelta := now.Sub(s.lastCPUCheckTime).Seconds()
	if timeDelta < 0.1 {
		// Too short interval, return last known value
		return s.CPUPercent
	}

	cpuDelta := totalCPUTime - s.lastCPUTime

	// Clock ticks per second (usually 100 on Linux)
	clockTicks := float64(100)

	// CPU percentage = (CPU ticks used / elapsed ticks) * 100
	// elapsed ticks = timeDelta * clockTicks
	cpuPercent := (float64(cpuDelta) / (timeDelta * clockTicks)) * 100

	// Update stored values
	s.lastCPUTime = totalCPUTime
	s.lastCPUCheckTime = now

	// Cap at reasonable value (can exceed 100% on multi-core)
	if cpuPercent > 800 {
		cpuPercent = 0 // Invalid reading
	}

	return cpuPercent
}

// GetMetrics returns current metrics for this server
func (s *Server) GetMetrics() (tps float64, memoryMB int, cpuPercent float64) {
	// Update process metrics
	memoryMB, cpuPercent = s.GetProcessMetrics()

	s.metricsMutex.Lock()
	tps = s.TPS
	s.metricsMutex.Unlock()

	return tps, memoryMB, cpuPercent
}

// UpdateProperties updates server.properties with the given values
func (s *Server) UpdateProperties(newProps map[string]string) error {
	propsPath := filepath.Join(s.DataDir, "server.properties")

	// Read existing properties
	existingProps, err := s.ReadProperties()
	if err != nil {
		existingProps = make(map[string]string)
	}

	// Merge new properties (overwrite existing)
	for k, v := range newProps {
		existingProps[k] = v
	}

	// Build new file content
	var lines []string
	lines = append(lines, "# Minecraft server properties")
	lines = append(lines, fmt.Sprintf("# Updated by RedstoneCore Agent on %s", time.Now().Format(time.RFC3339)))
	lines = append(lines, "")

	for k, v := range existingProps {
		lines = append(lines, fmt.Sprintf("%s=%s", k, v))
	}

	return os.WriteFile(propsPath, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}
