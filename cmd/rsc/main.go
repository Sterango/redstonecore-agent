package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/sterango/redstonecore-agent/internal/agent"
	"github.com/sterango/redstonecore-agent/internal/config"
	"github.com/sterango/redstonecore-agent/internal/version"
)

func main() {
	// Parse command line flags
	configPath := flag.String("config", "", "Path to config file (default: /config/config.yaml)")
	showVersion := flag.Bool("version", false, "Show version information")
	flag.Parse()

	if *showVersion {
		fmt.Printf("RedstoneCore Agent v%s (built %s)\n", version.Version, version.BuildTime)
		os.Exit(0)
	}

	// Setup logging
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetPrefix("[RSC] ")

	log.Printf("RedstoneCore Agent v%s starting...", version.Version)

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Try to load existing credentials
	if err := cfg.LoadCredentials(); err != nil {
		log.Printf("No existing credentials found, will register on startup")
	}

	// Create and run agent
	a := agent.New(cfg)
	if err := a.Run(); err != nil {
		log.Fatalf("Agent error: %v", err)
	}
}
