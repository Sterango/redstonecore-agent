package satisfactory

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ficsitBin is the bundled ficsit-cli binary (see Dockerfile).
const ficsitBin = "ficsit"

// ModInfo describes a mod installed in the server's gamefiles.
type ModInfo struct {
	Reference string `json:"reference"`
	Name      string `json:"name"`
	Version   string `json:"version"`
}

// gamefilesPath is the SteamCMD game install inside the server's /config volume,
// where ficsit-cli installs SML + mods.
func (s *Server) gamefilesPath() string {
	return filepath.Join(s.DataDir, "config", "gamefiles")
}

// ficsitProfile is the per-server ficsit-cli profile name.
func (s *Server) ficsitProfile() string {
	return "rsc-" + s.UUID
}

// ficsit runs ficsit-cli with a per-server config/cache dir so profiles and the
// download cache persist under the server's data dir.
func (s *Server) ficsit(args ...string) (string, error) {
	cfg := filepath.Join(s.DataDir, ".ficsit")
	cmd := exec.Command(ficsitBin, args...)
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+cfg,
		"XDG_CACHE_HOME="+filepath.Join(cfg, "cache"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("ficsit %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// ensureFicsitSetup creates the per-server profile and registers the server's
// game install with ficsit-cli (both idempotent).
func (s *Server) ensureFicsitSetup() error {
	_, _ = s.ficsit("profile", "new", s.ficsitProfile())                            // ignore "already exists"
	_, _ = s.ficsit("installation", "add", s.gamefilesPath(), s.ficsitProfile())    // ignore "already added"
	return nil
}

// AddMod adds a mod (optionally pinned to a version) to the server's profile.
// Call ApplyMods to actually download/install it.
func (s *Server) AddMod(modReference, version string) error {
	if err := s.ensureFicsitSetup(); err != nil {
		return err
	}
	args := []string{"profile", "mod", "add", s.ficsitProfile(), modReference}
	if version != "" {
		args = append(args, version)
	}
	_, err := s.ficsit(args...)
	return err
}

// RemoveMod removes a mod from the server's profile.
func (s *Server) RemoveMod(modReference string) error {
	if err := s.ensureFicsitSetup(); err != nil {
		return err
	}
	_, err := s.ficsit("profile", "mod", "remove", s.ficsitProfile(), modReference)
	return err
}

// ApplyMods installs/uninstalls mods (and the resolved SML + dependencies) to
// the server's game install to match the profile. The server should be stopped.
func (s *Server) ApplyMods() error {
	if err := s.ensureFicsitSetup(); err != nil {
		return err
	}
	_, err := s.ficsit("apply", s.gamefilesPath())
	return err
}

// ListMods reads the mods actually installed in the game files (each mod is a
// folder with a <Reference>.uplugin manifest).
func (s *Server) ListMods() ([]ModInfo, error) {
	modsDir := filepath.Join(s.gamefilesPath(), "FactoryGame", "Mods")
	matches, err := filepath.Glob(filepath.Join(modsDir, "*", "*.uplugin"))
	if err != nil {
		return nil, err
	}

	mods := make([]ModInfo, 0, len(matches))
	for _, p := range matches {
		ref := filepath.Base(filepath.Dir(p))
		info := ModInfo{Reference: ref}
		if data, err := os.ReadFile(p); err == nil {
			var u struct {
				FriendlyName string `json:"FriendlyName"`
				SemVersion   string `json:"SemVersion"`
				VersionName  string `json:"VersionName"`
			}
			if json.Unmarshal(data, &u) == nil {
				info.Name = u.FriendlyName
				info.Version = u.SemVersion
				if info.Version == "" {
					info.Version = u.VersionName
				}
			}
		}
		mods = append(mods, info)
	}
	return mods, nil
}

// ModsJSON returns the installed mods as a JSON string (for reporting to the
// panel). Returns "[]" on error.
func (s *Server) ModsJSON() string {
	mods, err := s.ListMods()
	if err != nil || mods == nil {
		mods = []ModInfo{}
	}
	data, err := json.Marshal(mods)
	if err != nil {
		return "[]"
	}
	return string(data)
}
