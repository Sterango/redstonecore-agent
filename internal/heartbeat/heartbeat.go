package heartbeat

import (
	"log"
	"time"

	"github.com/redstonecore/agent/internal/api"
)

type ServerStatusProvider interface {
	GetServerStatuses() []api.ServerStatus
	ExecuteCommand(cmd api.Command) error
}

type Heartbeat struct {
	client   *api.Client
	provider ServerStatusProvider
	interval time.Duration
	stopChan chan struct{}
	doneChan chan struct{}
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
		case <-h.stopChan:
			return
		}
	}
}

func (h *Heartbeat) sendHeartbeat() {
	req := &api.HeartbeatRequest{
		Status:  "online",
		Servers: h.provider.GetServerStatuses(),
	}

	resp, err := h.client.Heartbeat(req)
	if err != nil {
		log.Printf("[Heartbeat] Error: %v", err)
		return
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
