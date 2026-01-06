package heartbeat

import (
	"log"
	"time"

	"github.com/sterango/redstonecore-agent/internal/api"
	"github.com/sterango/redstonecore-agent/internal/version"
)

// ServerAnalytics contains analytics data for a single server
type ServerAnalytics struct {
	UUID         string
	PlayerCount  int
	TPS          float64
	MemoryUsedMB int
	MemoryMaxMB  int
	CPUPercent   float64
}

type ServerStatusProvider interface {
	GetServerStatuses() []api.ServerStatus
	GetServerAnalytics() []ServerAnalytics
	ExecuteCommand(cmd api.Command) error
}

type Heartbeat struct {
	client            *api.Client
	provider          ServerStatusProvider
	interval          time.Duration
	analyticsCounter  int
	updateNotified    bool
	stopChan          chan struct{}
	doneChan          chan struct{}
}

func New(client *api.Client, provider ServerStatusProvider, interval time.Duration) *Heartbeat {
	return &Heartbeat{
		client:   client,
		provider: provider,
		interval: interval,
	}
}

func (h *Heartbeat) Start() {
	h.stopChan = make(chan struct{})
	h.doneChan = make(chan struct{})

	go h.run()
}

func (h *Heartbeat) Stop() {
	if h.stopChan != nil {
		close(h.stopChan)
		<-h.doneChan
	}
}

func (h *Heartbeat) run() {
	defer close(h.doneChan)

	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	// Send initial heartbeat immediately
	h.sendHeartbeat()

	for {
		select {
		case <-ticker.C:
			h.sendHeartbeat()

			// Send analytics every 30 heartbeats (30 seconds with 1s interval)
			h.analyticsCounter++
			if h.analyticsCounter >= 30 {
				h.analyticsCounter = 0
				h.sendAnalytics()
			}
		case <-h.stopChan:
			return
		}
	}
}

func (h *Heartbeat) sendHeartbeat() {
	req := &api.HeartbeatRequest{
		Status:  "online",
		Version: version.Version,
		Servers: h.provider.GetServerStatuses(),
	}

	resp, err := h.client.Heartbeat(req)
	if err != nil {
		log.Printf("[Heartbeat] Error: %v", err)
		return
	}

	// Check for available updates (only notify once per session)
	if resp.UpdateAvailable && !h.updateNotified {
		log.Printf("[UPDATE] A new version (%s) is available! Current: %s. Run './rsc update' to upgrade.", resp.LatestVersion, version.Version)
		h.updateNotified = true
	}

	// Process any pending commands
	for _, cmd := range resp.Commands {
		log.Printf("[Heartbeat] Received command: %s for server %s", cmd.Command, cmd.ServerUUID)
		go h.executeCommand(cmd)
	}
}

func (h *Heartbeat) executeCommand(cmd api.Command) {
	err := h.provider.ExecuteCommand(cmd)

	// Report result back to cloud
	result := &api.CommandResultRequest{
		CommandUUID: cmd.UUID,
		Success:     err == nil,
	}
	if err != nil {
		result.Result = err.Error()
	} else {
		result.Result = "Command executed successfully"
	}

	if reportErr := h.client.ReportCommandResult(result); reportErr != nil {
		log.Printf("[Heartbeat] Failed to report command result: %v", reportErr)
	}
}

func (h *Heartbeat) sendAnalytics() {
	analytics := h.provider.GetServerAnalytics()

	for _, a := range analytics {
		req := &api.AnalyticsRequest{
			ServerUUID:   a.UUID,
			PlayerCount:  a.PlayerCount,
			TPS:          a.TPS,
			MemoryUsedMB: a.MemoryUsedMB,
			MemoryMaxMB:  a.MemoryMaxMB,
			CPUPercent:   a.CPUPercent,
		}

		if err := h.client.ReportAnalytics(req); err != nil {
			log.Printf("[Heartbeat] Failed to report analytics for server %s: %v", a.UUID, err)
		}
	}
}
