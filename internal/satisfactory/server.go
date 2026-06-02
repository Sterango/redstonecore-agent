// Package satisfactory manages Satisfactory dedicated servers.
//
// Unlike Minecraft servers (which run as child Java processes inside the agent
// container), Satisfactory is a glibc game binary with no console/RCON. Each
// server runs as a *sibling Docker container* using the community image
// wolveix/satisfactory-server, driven through the Docker socket the agent
// already mounts. The image installs the game via SteamCMD (Steam app 1690800)
// on first boot and exposes a game port (UDP+TCP) plus a reliable/messaging
// port (TCP). Ports cannot be remapped, so each container publishes its own
// matching pair 1:1.
package satisfactory

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Image is the community Satisfactory dedicated-server Docker image.
const Image = "wolveix/satisfactory-server:latest"

// ContainerName returns the docker container name for a server UUID.
func ContainerName(uuid string) string {
	return "rsc-sat-" + uuid
}

// Config describes a Satisfactory server to run.
type Config struct {
	UUID         string
	Name         string
	GamePort     int // SERVERGAMEPORT (UDP+TCP), also the HTTPS API port
	ReliablePort int // SERVERMESSAGINGPORT (TCP)
	MaxPlayers   int
	AllocatedRAM int    // MB; mapped to the container memory limit
	DataDir      string // in-container path to the server dir (e.g. /data/servers/foo)
	HostConfig   string // host path mounted into the container at /config
	SkipUpdate   bool   // pin the game build (modded posture); SKIPUPDATE=true
}

// satCredentials is persisted (.sat-credentials) so a claimed server can be
// re-administered after an agent restart.
type satCredentials struct {
	AdminPassword  string `json:"admin_password"`
	ClientPassword string `json:"client_password"`
	Session        string `json:"session"`
	Claimed        bool   `json:"claimed"`
}

// Server is a running (or installable) Satisfactory dedicated server backed by
// a Docker container.
type Server struct {
	UUID         string
	Name         string
	GamePort     int
	ReliablePort int
	MaxPlayers   int
	AllocatedRAM int
	DataDir      string
	HostConfig   string
	SkipUpdate   bool
	Host         string // where the agent reaches the HTTPS API (localhost, host-networked)

	mu         sync.Mutex
	installing bool
	// Runtime state (guarded by mu), refreshed by Poll/Provision.
	creds       satCredentials
	healthy     bool
	gameRunning bool
	paused      bool
	players     int
	tickRate    float64
	api         *APIClient
}

// NewServer builds a Server from a Config.
func NewServer(cfg Config) *Server {
	host := "127.0.0.1"
	s := &Server{
		UUID:         cfg.UUID,
		Name:         cfg.Name,
		GamePort:     cfg.GamePort,
		ReliablePort: cfg.ReliablePort,
		MaxPlayers:   cfg.MaxPlayers,
		AllocatedRAM: cfg.AllocatedRAM,
		DataDir:      cfg.DataDir,
		HostConfig:   cfg.HostConfig,
		SkipUpdate:   cfg.SkipUpdate,
		Host:         host,
	}
	s.loadCredentials()
	return s
}

func (s *Server) container() string { return ContainerName(s.UUID) }

// Install pulls the image and (re)creates the container. The container then
// downloads the ~8 GB of game files on first boot in the background; the
// progress callback receives coarse stage updates.
func (s *Server) Install(progress func(stage, message string)) error {
	s.setInstalling(true)
	defer s.setInstalling(false)

	report := func(stage, message string) {
		if progress != nil {
			progress(stage, message)
		}
	}

	report("pulling_image", "Pulling Satisfactory server image")
	if out, err := exec.Command("docker", "pull", Image).CombinedOutput(); err != nil {
		return fmt.Errorf("docker pull failed: %v: %s", err, strings.TrimSpace(string(out)))
	}

	// Remove any stale container with the same name so re-create is idempotent.
	_ = exec.Command("docker", "rm", "-f", s.container()).Run()

	report("starting", "Creating server container")
	if out, err := exec.Command("docker", s.runArgs()...).CombinedOutput(); err != nil {
		return fmt.Errorf("docker run failed: %v: %s", err, strings.TrimSpace(string(out)))
	}

	report("downloading_gamefiles", "Downloading game files (first boot)")
	return nil
}

// runArgs builds the `docker run` argument list for this server.
func (s *Server) runArgs() []string {
	mem := "8192m"
	if s.AllocatedRAM > 0 {
		mem = fmt.Sprintf("%dm", s.AllocatedRAM)
	}
	skipUpdate := "false"
	if s.SkipUpdate {
		skipUpdate = "true" // modded posture: pin the game build so SML isn't outrun
	}
	return []string{
		"run", "-d",
		"--name", s.container(),
		"--restart", "unless-stopped",
		"-v", s.HostConfig + ":/config",
		"-e", fmt.Sprintf("MAXPLAYERS=%d", s.MaxPlayers),
		"-e", fmt.Sprintf("SERVERGAMEPORT=%d", s.GamePort),
		"-e", fmt.Sprintf("SERVERMESSAGINGPORT=%d", s.ReliablePort),
		"-e", "PUID=1000",
		"-e", "PGID=1000",
		"-e", "SKIPUPDATE=" + skipUpdate,
		"-p", fmt.Sprintf("%d:%d/tcp", s.GamePort, s.GamePort),
		"-p", fmt.Sprintf("%d:%d/udp", s.GamePort, s.GamePort),
		"-p", fmt.Sprintf("%d:%d/tcp", s.ReliablePort, s.ReliablePort),
		"--memory", mem,
		Image,
	}
}

func (s *Server) dockerAction(action string) error {
	out, err := exec.Command("docker", action, s.container()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s failed: %v: %s", action, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Start, Stop, Restart and Kill control the container lifecycle.
func (s *Server) Start() error   { return s.dockerAction("start") }
func (s *Server) Stop() error    { return s.dockerAction("stop") }
func (s *Server) Restart() error { return s.dockerAction("restart") }
func (s *Server) Kill() error    { return s.dockerAction("kill") }

// Remove force-removes the container (used when deleting the server). A missing
// container is not an error.
func (s *Server) Remove() error {
	out, err := exec.Command("docker", "rm", "-f", s.container()).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "No such container") {
		return fmt.Errorf("docker rm failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Status returns running|stopped|installing|starting|crashed. It combines the
// container's docker state with the game's live readiness (from the HTTPS API):
// a running container whose world isn't up yet reports "installing" (first-boot
// game-file download) or "starting" (world loading after claim).
func (s *Server) Status() string {
	s.mu.Lock()
	installing := s.installing
	healthy := s.healthy
	gameRunning := s.gameRunning
	claimed := s.creds.Claimed
	s.mu.Unlock()
	if installing {
		return "installing"
	}

	out, err := exec.Command("docker", "inspect", "-f",
		"{{.State.Running}} {{.State.Status}} {{.State.ExitCode}}", s.container()).Output()
	if err != nil {
		return "stopped" // container not created / not found
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 2 {
		return "stopped"
	}
	running, status := fields[0], fields[1]
	if running == "true" {
		switch {
		case healthy:
			// HTTPS API is responding → the server is up and joinable. (A fresh
			// empty world reports isGameRunning=false even while healthy/ticking,
			// so health, not isGameRunning, is the right "running" signal.)
			return "running"
		case claimed:
			return "starting" // claimed but API not yet up (booting / applying a game update)
		default:
			return "installing" // first boot — still downloading the ~8GB of game files
		}
	}
	_ = gameRunning // retained in state for the panel; not used for status
	if status == "exited" && len(fields) >= 3 && fields[2] != "0" {
		return "crashed"
	}
	return "stopped"
}

// PlayerCount returns the last-polled number of connected players.
func (s *Server) PlayerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.players
}

func (s *Server) setInstalling(v bool) {
	s.mu.Lock()
	s.installing = v
	s.mu.Unlock()
}

// Recreate stops, removes, and re-runs the container with the current config.
// Used when a setting (MaxPlayers, SkipUpdate) can only change via a new
// container rather than the live HTTPS API.
func (s *Server) Recreate() error {
	_ = s.Remove()
	if out, err := exec.Command("docker", s.runArgs()...).CombinedOutput(); err != nil {
		return fmt.Errorf("docker run failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---- credentials ----

func (s *Server) credentialsPath() string {
	return filepath.Join(s.DataDir, ".sat-credentials")
}

func (s *Server) loadCredentials() {
	data, err := os.ReadFile(s.credentialsPath())
	if err != nil {
		return
	}
	var c satCredentials
	if json.Unmarshal(data, &c) == nil {
		s.mu.Lock()
		s.creds = c
		s.mu.Unlock()
	}
}

func (s *Server) saveCredentials() {
	s.mu.Lock()
	c := s.creds
	s.mu.Unlock()
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	if err := os.WriteFile(s.credentialsPath(), data, 0600); err != nil {
		log.Printf("[Satisfactory] failed to persist credentials for %s: %v", s.UUID, err)
	}
}

// IsClaimed reports whether the server has been claimed/provisioned.
func (s *Server) IsClaimed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creds.Claimed
}

// ---- API helpers ----

func (s *Server) ensureAPI() *APIClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.api == nil {
		s.api = NewAPIClient(s.Host, s.GamePort)
	}
	return s.api
}

// adminAPI returns a client authenticated at Administrator level using the
// stored admin password.
func (s *Server) adminAPI() (*APIClient, error) {
	s.mu.Lock()
	pw := s.creds.AdminPassword
	s.mu.Unlock()
	if pw == "" {
		return nil, fmt.Errorf("server not yet claimed")
	}
	api := s.ensureAPI()
	tok, err := api.PasswordLogin(PrivilegeAdministrator, pw)
	if err != nil {
		return nil, err
	}
	api.SetToken(tok)
	return api, nil
}

func generatePassword() string {
	b := make([]byte, 18)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func defaultSession(name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		return "RedstoneCore"
	}
	return n
}

// ---- provisioning + polling ----

// Provision waits for the server's API to come up after first boot, then claims
// it (setting a generated admin password) and creates a starting session so the
// world is immediately joinable. Safe to call again after an agent restart: an
// already-claimed server just resumes polling. onUpdate fires whenever the
// reportable state changes so the agent can push it to the panel.
func (s *Server) Provision(onUpdate func()) {
	api := s.ensureAPI()

	// First boot downloads ~8GB before the game (and its API) come up.
	if !s.waitHealthy(45 * time.Minute) {
		log.Printf("[Satisfactory] %s did not become healthy in time", s.Name)
		return
	}
	s.mu.Lock()
	s.healthy = true
	s.mu.Unlock()
	if onUpdate != nil {
		onUpdate()
	}

	if s.IsClaimed() {
		log.Printf("[Satisfactory] %s already claimed; resuming", s.Name)
		s.Poll()
		if onUpdate != nil {
			onUpdate()
		}
		return
	}

	// Claim the unclaimed server (passwordless login yields InitialAdmin).
	initTok, err := api.PasswordlessLogin(PrivilegeInitialAdmin)
	if err != nil {
		log.Printf("[Satisfactory] %s passwordless login failed (claimed externally?): %v", s.Name, err)
		return
	}
	api.SetToken(initTok)

	adminPw := generatePassword()
	adminTok, err := api.ClaimServer(s.Name, adminPw)
	if err != nil {
		log.Printf("[Satisfactory] %s claim failed: %v", s.Name, err)
		return
	}
	api.SetToken(adminTok)

	session := defaultSession(s.Name)
	if err := api.CreateNewGame(session); err != nil {
		// Non-fatal: the server is claimed; an admin can create a session manually.
		log.Printf("[Satisfactory] %s create-new-game failed: %v", s.Name, err)
	}

	s.mu.Lock()
	s.creds = satCredentials{AdminPassword: adminPw, Session: session, Claimed: true}
	s.mu.Unlock()
	s.saveCredentials()

	log.Printf("[Satisfactory] %s claimed; session %q created", s.Name, session)
	s.Poll()
	if onUpdate != nil {
		onUpdate()
	}
}

func (s *Server) waitHealthy(timeout time.Duration) bool {
	api := s.ensureAPI()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := api.HealthCheck(); err == nil {
			return true
		}
		time.Sleep(10 * time.Second)
	}
	return false
}

// Poll refreshes live state (health, player count, tick rate) from the API.
func (s *Server) Poll() {
	api := s.ensureAPI()
	healthy := api.HealthCheck() == nil
	if !healthy {
		s.mu.Lock()
		s.healthy = false
		s.gameRunning = false
		s.players = 0
		s.mu.Unlock()
		return
	}
	state, err := api.QueryServerState()
	if err != nil {
		s.mu.Lock()
		s.healthy = true
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.healthy = true
	s.gameRunning = state.IsGameRunning
	s.paused = state.IsGamePaused
	s.players = state.NumConnectedPlayers
	s.tickRate = state.AverageTickRate
	if state.ActiveSessionName != "" {
		s.creds.Session = state.ActiveSessionName
	}
	s.mu.Unlock()
}

// State is a snapshot of reportable Satisfactory state for the panel.
type State struct {
	AdminPassword string
	ClientPassword string
	Session       string
	Claimed       bool
	Healthy       bool
	GameRunning   bool
	Paused        bool
	Players       int
	TickRate      float64
}

// StateSnapshot returns the current reportable state.
func (s *Server) StateSnapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return State{
		AdminPassword:  s.creds.AdminPassword,
		ClientPassword: s.creds.ClientPassword,
		Session:        s.creds.Session,
		Claimed:        s.creds.Claimed,
		Healthy:        s.healthy,
		GameRunning:    s.gameRunning,
		Paused:         s.paused,
		Players:        s.players,
		TickRate:       s.tickRate,
	}
}

// ---- settings ----

// Settings is a set of Satisfactory settings to apply over the live API
// (anything requiring a container recreate is handled by the agent).
type Settings struct {
	ServerName     *string
	ClientPassword *string
	Options        map[string]string // raw FG.* server options
}

// ApplySettings applies name/join-password/options to a claimed server.
func (s *Server) ApplySettings(set Settings) error {
	api, err := s.adminAPI()
	if err != nil {
		return err
	}
	if set.ServerName != nil && *set.ServerName != "" {
		if err := api.RenameServer(*set.ServerName); err != nil {
			return err
		}
		s.mu.Lock()
		s.Name = *set.ServerName
		s.mu.Unlock()
	}
	if set.ClientPassword != nil {
		if err := api.SetClientPassword(*set.ClientPassword); err != nil {
			return err
		}
		s.mu.Lock()
		s.creds.ClientPassword = *set.ClientPassword
		s.mu.Unlock()
		s.saveCredentials()
	}
	if len(set.Options) > 0 {
		if err := api.ApplyServerOptions(set.Options); err != nil {
			return err
		}
	}
	return nil
}

// SaveWorld flushes the current world to disk via the API so a subsequent file
// backup captures the latest state.
func (s *Server) SaveWorld(name string) error {
	api, err := s.adminAPI()
	if err != nil {
		return err
	}
	if name == "" {
		name = defaultSession(s.Name)
	}
	return api.SaveGame(name)
}
