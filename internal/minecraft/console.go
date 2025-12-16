package minecraft

import (
	"sync"
	"time"
)

// ConsoleBuffer buffers console lines and sends them in batches
type ConsoleBuffer struct {
	serverUUID string
	lines      []string
	mu         sync.Mutex
	sendFunc   func(serverUUID string, lines []string) error
	stopChan   chan struct{}
	wg         sync.WaitGroup
}

// NewConsoleBuffer creates a new console buffer that will send lines using the provided function
func NewConsoleBuffer(serverUUID string, sendFunc func(string, []string) error) *ConsoleBuffer {
	cb := &ConsoleBuffer{
		serverUUID: serverUUID,
		lines:      make([]string, 0, 50),
		sendFunc:   sendFunc,
		stopChan:   make(chan struct{}),
	}

	// Start the flush goroutine
	cb.wg.Add(1)
	go cb.flushLoop()

	return cb
}

// AddLine adds a console line to the buffer
func (cb *ConsoleBuffer) AddLine(line string) {
	cb.mu.Lock()
	cb.lines = append(cb.lines, line)
	cb.mu.Unlock()
}

// flushLoop periodically sends buffered lines to the cloud
func (cb *ConsoleBuffer) flushLoop() {
	defer cb.wg.Done()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cb.flush()
		case <-cb.stopChan:
			// Final flush before stopping
			cb.flush()
			return
		}
	}
}

// flush sends all buffered lines to the cloud
func (cb *ConsoleBuffer) flush() {
	cb.mu.Lock()
	if len(cb.lines) == 0 {
		cb.mu.Unlock()
		return
	}

	// Copy lines and clear buffer
	linesToSend := make([]string, len(cb.lines))
	copy(linesToSend, cb.lines)
	cb.lines = cb.lines[:0]
	cb.mu.Unlock()

	// Send to cloud (non-blocking)
	if cb.sendFunc != nil {
		go func() {
			if err := cb.sendFunc(cb.serverUUID, linesToSend); err != nil {
				// Log error but don't stop - console output shouldn't block
				// Could add retry logic here if needed
			}
		}()
	}
}

// Stop stops the console buffer and flushes remaining lines
func (cb *ConsoleBuffer) Stop() {
	close(cb.stopChan)
	cb.wg.Wait()
}

// UpdateServerUUID updates the server UUID (useful if assigned by cloud after creation)
func (cb *ConsoleBuffer) UpdateServerUUID(uuid string) {
	cb.mu.Lock()
	cb.serverUUID = uuid
	cb.mu.Unlock()
}
