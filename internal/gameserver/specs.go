// Package gameserver runs SteamCMD-based dedicated game servers as sibling
// Docker containers using LinuxGSM's uniform multi-game images
// (gameservermanagers/gameserver:<tag>). Each game is a GameSpec — adding a new
// game is (mostly) a spec entry, not new code.
//
// LinuxGSM images: mount a host dir at /data (the LGSM home), run with host
// networking, and the server installs via SteamCMD on first boot. Per-instance
// ports are pinned by writing the game's LinuxGSM config
// (/data/lgsm/config-lgsm/<name>/<name>.cfg) before start, so multiple servers
// don't collide.
package gameserver

import "fmt"

// Port is a published port relative to a server's allocated base.
type Port struct {
	Offset int    // added to the allocated base port
	Proto  string // "udp", "tcp", or "udp/tcp"
}

// GameSpec describes how to run one game via LinuxGSM.
type GameSpec struct {
	Key   string // our game key (matches the panel registry), e.g. "valheim"
	Label string
	Tag   string // docker image tag, e.g. "vh"
	Name  string // LinuxGSM gameserver name, e.g. "vhserver"

	Ports       []Port // ports the server uses, relative to the allocated base
	QueryOffset int    // A2S query port = base + QueryOffset; -1 if the game has no A2S
	PortCount   int    // size of the contiguous port block to reserve for this game

	// Config returns the LinuxGSM <name>.cfg contents that pin this server's
	// port(s)/name/player cap. base is the allocated base port.
	Config func(base, maxPlayers int, serverName string) string
}

// Image is the Docker image for this game.
func (s GameSpec) Image() string { return "gameservermanagers/gameserver:" + s.Tag }

// kv builds a LinuxGSM config file body from key=value lines.
func kv(pairs ...string) string {
	out := ""
	for i := 0; i+1 < len(pairs); i += 2 {
		out += fmt.Sprintf("%s=%q\n", pairs[i], pairs[i+1])
	}
	return out
}

// Specs is the registry of supported LinuxGSM games.
//
// Port schemes are best-effort from LinuxGSM defaults and are validated per game
// on first real launch; the primary game `port` is always pinned from the
// allocated base so instances don't collide.
var Specs = map[string]GameSpec{
	"valheim": {
		Key: "valheim", Label: "Valheim", Tag: "vh", Name: "vhserver",
		Ports: []Port{{0, "udp"}, {1, "udp"}}, QueryOffset: 1, PortCount: 4,
		Config: func(base, mp int, name string) string {
			return kv("servername", name, "port", fmt.Sprintf("%d", base), "maxplayers", fmt.Sprintf("%d", mp))
		},
	},
	"cs2": {
		Key: "cs2", Label: "Counter-Strike 2", Tag: "cs2", Name: "cs2server",
		Ports: []Port{{0, "udp/tcp"}}, QueryOffset: 0, PortCount: 4,
		Config: func(base, mp int, name string) string {
			return kv("servername", name, "port", fmt.Sprintf("%d", base), "maxplayers", fmt.Sprintf("%d", mp))
		},
	},
	"rust": {
		Key: "rust", Label: "Rust", Tag: "rust", Name: "rustserver",
		Ports: []Port{{0, "udp"}, {1, "tcp"}, {2, "tcp"}}, QueryOffset: 1, PortCount: 8,
		Config: func(base, mp int, name string) string {
			return kv("servername", name,
				"port", fmt.Sprintf("%d", base),
				"queryport", fmt.Sprintf("%d", base+1),
				"rconport", fmt.Sprintf("%d", base+2),
				"maxplayers", fmt.Sprintf("%d", mp))
		},
	},
	"palworld": {
		Key: "palworld", Label: "Palworld", Tag: "pw", Name: "pwserver",
		Ports: []Port{{0, "udp"}, {1, "tcp"}}, QueryOffset: -1, PortCount: 4,
		Config: func(base, mp int, name string) string {
			return kv("servername", name,
				"port", fmt.Sprintf("%d", base),
				"rconport", fmt.Sprintf("%d", base+1),
				"maxplayers", fmt.Sprintf("%d", mp))
		},
	},
	"7daystodie": {
		Key: "7daystodie", Label: "7 Days to Die", Tag: "sdtd", Name: "sdtdserver",
		Ports: []Port{{0, "udp"}, {1, "udp"}, {2, "udp"}}, QueryOffset: -1, PortCount: 6,
		Config: func(base, mp int, name string) string {
			return kv("servername", name, "port", fmt.Sprintf("%d", base), "maxplayers", fmt.Sprintf("%d", mp))
		},
	},
	"projectzomboid": {
		Key: "projectzomboid", Label: "Project Zomboid", Tag: "pz", Name: "pzserver",
		Ports: []Port{{0, "udp"}, {1, "udp"}}, QueryOffset: -1, PortCount: 4,
		Config: func(base, mp int, name string) string {
			return kv("servername", name, "port", fmt.Sprintf("%d", base), "maxplayers", fmt.Sprintf("%d", mp))
		},
	},
	"corekeeper": {
		Key: "corekeeper", Label: "Core Keeper", Tag: "ck", Name: "ckserver",
		Ports: []Port{{0, "udp"}}, QueryOffset: -1, PortCount: 2,
		Config: func(base, mp int, name string) string {
			return kv("servername", name, "port", fmt.Sprintf("%d", base), "maxplayers", fmt.Sprintf("%d", mp))
		},
	},
	"dontstarvetogether": {
		Key: "dontstarvetogether", Label: "Don't Starve Together", Tag: "dst", Name: "dstserver",
		Ports: []Port{{0, "udp"}}, QueryOffset: -1, PortCount: 4,
		Config: func(base, mp int, name string) string {
			return kv("servername", name, "port", fmt.Sprintf("%d", base), "maxplayers", fmt.Sprintf("%d", mp))
		},
	},
	"terraria": {
		Key: "terraria", Label: "Terraria", Tag: "terraria", Name: "terrariaserver",
		Ports: []Port{{0, "tcp"}}, QueryOffset: -1, PortCount: 2,
		Config: func(base, mp int, name string) string {
			return kv("servername", name, "port", fmt.Sprintf("%d", base), "maxplayers", fmt.Sprintf("%d", mp))
		},
	},
}

// Spec returns the spec for a game key.
func Spec(key string) (GameSpec, bool) {
	s, ok := Specs[key]
	return s, ok
}

// IsGame reports whether a key is a supported LinuxGSM game.
func IsGame(key string) bool {
	_, ok := Specs[key]
	return ok
}
