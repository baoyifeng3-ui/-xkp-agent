package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"xkp-agent/internal/client"
	"xkp-agent/internal/collect"
	"xkp-agent/internal/config"
	"xkp-agent/internal/identity"
	"xkp-agent/internal/power"
	agentruntime "xkp-agent/internal/runtime"
)

const version = "0.1.0"

func main() {
	configPath := flag.String("config", "/etc/xkp-agent/config.yaml", "configuration file")
	tokenFile := flag.String("enrollment-token-file", "", "one-time enrollment token file")
	displayName := flag.String("display-name", "", "processing server display name")
	enrollOnly := flag.Bool("enroll-only", false, "enroll and exit without starting the runtime")
	flag.Parse()
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "xkp-agent configuration error:", err)
		os.Exit(1)
	}
	api, err := client.New(cfg)
	if err != nil {
		fail("client initialization failed", err)
	}
	info, err := identity.Discover("/etc/machine-id", cfg.ManagementURL)
	if err != nil {
		fail("identity discovery failed", err)
	}
	if cfg.AgentID == "" {
		if *tokenFile == "" {
			fail("agent is not enrolled", fmt.Errorf("enrollment token file is required"))
		}
		tokenBytes, readErr := os.ReadFile(*tokenFile)
		if readErr != nil {
			fail("read enrollment token file", readErr)
		}
		name := strings.TrimSpace(*displayName)
		if name == "" {
			name = info.Hostname
		}
		enrollment, enrollErr := api.EnrollWithDisplayName(context.Background(), strings.TrimSpace(string(tokenBytes)), name, info)
		if enrollErr != nil {
			fail("enrollment failed", enrollErr)
		}
		cfg.AgentID, cfg.Credential = enrollment.AgentID, enrollment.Credential
		if saveErr := config.Save(*configPath, cfg); saveErr != nil {
			fail("save enrollment credential", saveErr)
		}
		if removeErr := os.Remove(*tokenFile); removeErr != nil {
			fail("remove enrollment token file", removeErr)
		}
		api, err = client.New(cfg)
		if err != nil {
			fail("client initialization failed", err)
		}
	}
	if *enrollOnly {
		return
	}

	gatherer := collect.NewMultiCollector(collect.NewSystemCollector(cfg.WorkspacePath), collect.NewNvidiaCollector(), collect.NewDockerCollector())
	dispatcher := agentruntime.NewCommandDispatcher(api, power.NewController())
	agent := agentruntime.NewAgent(cfg.AgentID, version, gatherer, api, dispatcher)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := agent.Run(ctx); err != nil {
		fail("agent runtime stopped", err)
	}
}

func fail(message string, err error) {
	fmt.Fprintln(os.Stderr, "xkp-agent:", message+":", err)
	os.Exit(1)
}
