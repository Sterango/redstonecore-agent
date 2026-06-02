package gameserver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ContainerName is the docker container name for a game server UUID.
func ContainerName(uuid string) string { return "rsc-gs-" + uuid }

// Config describes a game server to run.
type Config struct {
	UUID         string
	Name         string
	Game         string // spec key, e.g. "valheim"
	BasePort     int
	MaxPlayers   int
	AllocatedRAM int    // MB
	DataDir      string // in-agent path to the server's data dir (mounted at /data)
	HostData     string // host path of DataDir (for the sibling bind mount)
}

// Server is a LinuxGSM-backed dedicated game server in a sibling container.
type Server struct {
	UUID         string
	Name         string
	Game         string
	BasePort     int
	MaxPlayers   int
	AllocatedRAM int
	DataDir      string
	HostData     string
	spec         GameSpec

	mu         sync.Mutex
	installing bool
	players    int
}

// NewServer builds a Server for a supported game.
func NewServer(cfg Config) (*Server, error) {
	spec, ok := Spec(cfg.Game)
	if !ok {
		return nil, fmt.Errorf("unsupported game: %s", cfg.Game)
	}
	return &Server{
		UUID: cfg.UUID, Name: cfg.Name, Game: cfg.Game,
		BasePort: cfg.BasePort, MaxPlayers: cfg.MaxPlayers, AllocatedRAM: cfg.AllocatedRAM,
		DataDir: cfg.DataDir, HostData: cfg.HostData, spec: spec,
	}, nil
}

func (s *Server) container() string { return ContainerName(s.UUID) }

// Spec returns the game spec.
func (s *Server) Spec() GameSpec { return s.spec }

// writeConfig pins the LinuxGSM port/name/maxplayers config so instances don't
// collide (host networking means there's no docker port remap).
func (s *Server) writeConfig() error {
	dir := filepath.Join(s.DataDir, "lgsm", "config-lgsm", s.spec.Name)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	body := "## Managed by RedstoneCore — edits may be overwritten\n" +
		s.spec.Config(s.BasePort, s.MaxPlayers, s.Name)
	return os.WriteFile(filepath.Join(dir, s.spec.Name+".cfg"), []byte(body), 0o664)
}

// Install pulls the image, writes config, and starts the container. LinuxGSM
// installs the game via SteamCMD on first boot.
func (s *Server) Install(progress func(stage, message string)) error {
	s.setInstalling(true)
	defer s.setInstalling(false)
	report := func(st, m string) {
		if progress != nil {
			progress(st, m)
		}
	}

	if err := os.MkdirAll(s.DataDir, 0o777); err != nil {
		return err
	}
	_ = os.Chown(s.DataDir, 1000, 1000) // LinuxGSM runs as uid 1000
	if err := s.writeConfig(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	report("pulling_image", "Pulling "+s.spec.Label+" server image")
	if out, err := exec.Command("docker", "pull", s.spec.Image()).CombinedOutput(); err != nil {
		return fmt.Errorf("docker pull: %v: %s", err, strings.TrimSpace(string(out)))
	}
	_ = exec.Command("docker", "rm", "-f", s.container()).Run()

	report("starting", "Creating server container")
	if out, err := exec.Command("docker", s.runArgs()...).CombinedOutput(); err != nil {
		return fmt.Errorf("docker run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	report("installing_gamefiles", "Installing game files (first boot)")
	return nil
}

func (s *Server) runArgs() []string {
	args := []string{
		"run", "-d",
		"--name", s.container(),
		"--restart", "unless-stopped",
		"--network", "host", // LinuxGSM servers advertise their real ports
		"-v", s.HostData + ":/data",
	}
	if s.AllocatedRAM > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", s.AllocatedRAM))
	}
	return append(args, s.spec.Image())
}

func (s *Server) dockerAction(action string) error {
	out, err := exec.Command("docker", action, s.container()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %v: %s", action, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *Server) Start() error   { return s.dockerAction("start") }
func (s *Server) Stop() error    { return s.dockerAction("stop") }
func (s *Server) Restart() error { return s.dockerAction("restart") }
func (s *Server) Kill() error    { return s.dockerAction("kill") }

// Remove force-removes the container (missing container is not an error).
func (s *Server) Remove() error {
	out, err := exec.Command("docker", "rm", "-f", s.container()).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "No such container") {
		return fmt.Errorf("docker rm: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Recreate re-runs the container with current config (after a settings change).
func (s *Server) Recreate() error {
	_ = s.Remove()
	if err := s.writeConfig(); err != nil {
		return err
	}
	if out, err := exec.Command("docker", s.runArgs()...).CombinedOutput(); err != nil {
		return fmt.Errorf("docker run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Status returns running|stopped|installing|crashed from the container state.
func (s *Server) Status() string {
	s.mu.Lock()
	installing := s.installing
	s.mu.Unlock()
	if installing {
		return "installing"
	}
	out, err := exec.Command("docker", "inspect", "-f",
		"{{.State.Running}} {{.State.Status}} {{.State.ExitCode}}", s.container()).Output()
	if err != nil {
		return "stopped"
	}
	f := strings.Fields(strings.TrimSpace(string(out)))
	if len(f) < 2 {
		return "stopped"
	}
	if f[0] == "true" {
		return "running"
	}
	if f[1] == "exited" && len(f) >= 3 && f[2] != "0" {
		return "crashed"
	}
	return "stopped"
}

// Poll refreshes the player count via A2S (games without A2S report 0).
func (s *Server) Poll() {
	if s.spec.QueryOffset < 0 {
		return
	}
	info, err := QueryA2S(fmt.Sprintf("127.0.0.1:%d", s.BasePort+s.spec.QueryOffset))
	if err != nil {
		s.mu.Lock()
		s.players = 0
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.players = info.Players
	s.mu.Unlock()
}

// PlayerCount returns the last-polled player count.
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

// Stats returns the server container's live memory (MB) and CPU%.
func (s *Server) Stats() (memMB int, cpuPct float64) {
	return ContainerStats(s.container())
}

// ContainerStats reads a container's live memory (MB) and CPU% via `docker stats`.
// Returns zeros if the container isn't running or stats are unavailable.
func ContainerStats(container string) (memMB int, cpuPct float64) {
	out, err := exec.Command("docker", "stats", "--no-stream",
		"--format", "{{.MemUsage}}|{{.CPUPerc}}", container).Output()
	if err != nil {
		return 0, 0
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	return parseMemMB(parts[0]), parseCPUPercent(parts[1])
}

// parseMemMB parses the "used" side of a docker MemUsage string ("123.4MiB / 8GiB").
func parseMemMB(s string) int {
	used := strings.TrimSpace(s)
	if i := strings.Index(used, "/"); i >= 0 {
		used = strings.TrimSpace(used[:i])
	}
	var num float64
	var unit string
	for i, r := range used {
		if (r < '0' || r > '9') && r != '.' {
			fmt.Sscanf(used[:i], "%f", &num)
			unit = strings.TrimSpace(used[i:])
			break
		}
	}
	switch {
	case strings.HasPrefix(unit, "GiB"), strings.HasPrefix(unit, "GB"):
		return int(num * 1024)
	case strings.HasPrefix(unit, "MiB"), strings.HasPrefix(unit, "MB"):
		return int(num)
	case strings.HasPrefix(unit, "KiB"), strings.HasPrefix(unit, "kB"):
		return int(num / 1024)
	}
	return int(num)
}

func parseCPUPercent(s string) float64 {
	var v float64
	fmt.Sscanf(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%")), "%f", &v)
	return v
}
