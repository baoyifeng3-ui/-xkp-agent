package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	dockerclient "github.com/docker/docker/client"
	"xkp-agent/internal/client"
	"xkp-agent/internal/collect"
	"xkp-agent/internal/config"
	containerexecutor "xkp-agent/internal/container"
	"xkp-agent/internal/dockerinventory"
	"xkp-agent/internal/identity"
	imagedeploypkg "xkp-agent/internal/imagedeploy"
	modeldeploypkg "xkp-agent/internal/modeldeploy"
	"xkp-agent/internal/power"
	agentruntime "xkp-agent/internal/runtime"
	terminalpkg "xkp-agent/internal/terminal"
	transferpkg "xkp-agent/internal/transfer"
	upgradepkg "xkp-agent/internal/upgrade"
)

const version = "0.2.34"

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

	gatherer := collect.NewMultiCollector(collect.NewSystemCollector(cfg.WorkspacePath), collect.NewNvidiaCollector(), collect.NewDockerCollector(), collect.NewNetworkCollector())
	commandStore := agentruntime.NewFileCommandStore(filepath.Join(filepath.Dir(*configPath),
		"pending-command.json"))
	dockerClient, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		fail("Docker client initialization failed", err)
	}
	defer dockerClient.Close()
	executor := containerexecutor.NewDockerExecutor(dockerClient, containerexecutor.Validator{
		WorkspaceRoot:    cfg.EnvironmentWorkspaceRoot,
		CodeServerTLSDir: cfg.CodeServerTLSDir,
	}, containerexecutor.NewHostMPSManager(filepath.Join(cfg.WorkspacePath, "mps")))
	grantStore := agentruntime.NewMemoryOperationGrantStore()
	recoveryStore := terminalpkg.NewFileRecoveryStore(filepath.Join(filepath.Dir(*configPath), "terminal-recovery.json"))
	terminalManager, err := terminalpkg.NewManager(recoveryStore, terminalpkg.NewRunner(api, cfg.Development), api)
	if err != nil {
		fail("terminal manager initialization failed", err)
	}
	if _, err := terminalManager.RetryRecovery(context.Background()); err != nil {
		fail("terminal recovery failed", err)
	}
	dispatcher := agentruntime.NewCommandDispatcherWithGrantAndTerminal(api, power.NewController(), commandStore, executor, grantStore, terminalManager)
	upgradeManager, err := upgradepkg.NewManager(api)
	if err != nil {
		fail("upgrade manager initialization failed", err)
	}
	dispatcher.SetUpgradeManager(upgradeManager)
	dispatcher.SetImageDeploymentManager(imagedeploypkg.NewManager(api))
	dispatcher.SetDockerInventoryManager(dockerinventory.New(dockerClient))
	dispatcher.SetFileTransferManager(transferpkg.New(api, cfg.EnvironmentWorkspaceRoot))
	dispatcher.SetModelWorkspaceManager(modeldeploypkg.New(modeldeploypkg.NewDockerRunner(dockerClient)))
	agent := agentruntime.NewAgentWithGrantStore(cfg.AgentID, version, gatherer, api, dispatcher, grantStore)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = terminalManager.Close(shutdownCtx, "agent-shutdown")
	}()
	if err := agent.Run(ctx); err != nil {
		fail("agent runtime stopped", err)
	}
}

func fail(message string, err error) {
	fmt.Fprintln(os.Stderr, "xkp-agent:", message+":", err)
	os.Exit(1)
}
