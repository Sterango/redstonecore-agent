package version

// Version and build information, can be overridden at build time via ldflags:
// go build -ldflags "-X github.com/sterango/redstonecore-agent/internal/version.Version=1.2.3"
var (
	Version   = "1.0.0"
	BuildTime = "unknown"
)
