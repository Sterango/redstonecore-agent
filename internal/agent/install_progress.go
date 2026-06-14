package agent

import (
	"log"

	"github.com/sterango/redstonecore-agent/internal/api"
)

// reportInstall pushes install progress for ANY server type to the cloud, reusing
// the modpack-progress channel (the panel renders it as "Installing — <message>"
// with a progress bar). The web endpoint accepts a free-form stage string.
func (a *Agent) reportInstall(uuid, stage, message string, progress int) {
	if a.client == nil {
		return
	}
	if err := a.client.ReportModpackProgress(&api.ModpackProgressRequest{
		ServerUUID: uuid,
		Stage:      stage,
		Message:    message,
		Progress:   progress,
		Total:      100,
	}); err != nil {
		log.Printf("[Install] %s: failed to report %q: %v", uuid, stage, err)
	}
}

// installStagePct maps a coarse install stage to a rough bar percentage.
func installStagePct(stage string) int {
	switch stage {
	case "preparing":
		return 8
	case "pulling_image":
		return 25
	case "downloading_server", "downloading":
		return 40
	case "starting":
		return 60
	case "installing_loader":
		return 70
	case "installing_gamefiles", "installing":
		return 85
	case "finalizing":
		return 95
	default:
		return 50
	}
}
