package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/documentparser"
	"content-agent/backend/internal/executor"
	"content-agent/backend/internal/httpapi"
	"content-agent/backend/internal/mediakit"
	"content-agent/backend/internal/mediaprocessor"
	"content-agent/backend/internal/revision"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/scriptsandbox"
	"content-agent/backend/internal/shell"
)

func main() {
	var err error
	var (
		listen            = flag.String("listen", "127.0.0.1:8850", "HTTP listen address")
		projectRoot       = flag.String("project-root", "", "content-agent project root")
		capabilityDir     = flag.String("capability-dir", "", "capability manifest directory")
		databasePath      = flag.String("database", "", "SQLite runtime database path")
		retentionInterval = flag.Duration(
			"retention-interval",
			time.Minute,
			"asset retention sweep interval",
		)
		retentionLeaseSeconds = flag.Int(
			"retention-lease-seconds",
			60,
			"asset retention job lease in seconds",
		)
		retentionMaxAttempts = flag.Int(
			"retention-max-attempts",
			5,
			"maximum asset retention deletion attempts",
		)
	)
	flag.Parse()
	if *retentionInterval <= 0 || *retentionLeaseSeconds < 5 || *retentionMaxAttempts < 1 {
		slog.Error("retention worker configuration invalid")
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	root, err := resolveProjectRoot(*projectRoot)
	if err != nil {
		logger.Error("resolve project root", "error", err)
		os.Exit(1)
	}
	if err := loadLocalEnvironment(root); err != nil {
		logger.Error("load local environment", "error", err)
		os.Exit(1)
	}
	configureInternalEndpointProxyBypass()
	configureBundledMediaTools(root)

	registry, err := capability.LoadRegistry(capability.LoadOptions{
		ProjectRoot:   root,
		CapabilityDir: *capabilityDir,
	})
	if err != nil {
		logger.Error("load capability registry", "error", err)
		os.Exit(1)
	}
	toolConfigPath := strings.TrimSpace(os.Getenv("CONTENT_AGENT_TOOL_CONFIG_PATH"))
	if toolConfigPath != "" && !filepath.IsAbs(toolConfigPath) {
		toolConfigPath = filepath.Join(root, toolConfigPath)
	}
	toolRegistry, err := agenttool.Load(toolConfigPath)
	if err != nil {
		logger.Error("load Agent tool registry", "error", err)
		os.Exit(1)
	}
	if err := registry.SetSkillDependencyResolver(toolRegistry); err != nil {
		logger.Error("resolve Skill tool dependencies", "error", err)
		os.Exit(1)
	}
	executorRegistry := executor.NewDefault()
	documentParser := documentparser.NewDefaultFromEnv()
	providerSet := executor.ProviderSet{
		"content_model_provider":    true,
		"document_parser":           documentParser.Supports(".docx"),
		"multimodal_video_provider": true,
		"subtitle_ocr_provider":     mediakit.AvailableFromEnv(),
		"media_processor":           mediaprocessor.AvailableFromEnv(),
	}
	controlMode := "openai_agents_sdk"
	mainAgentMode := "openai_agents_sdk_required"
	runtimePath := *databasePath
	if runtimePath == "" {
		runtimePath = filepath.Join(root, "data", "content_agent.db")
	} else if !filepath.IsAbs(runtimePath) {
		runtimePath = filepath.Join(root, runtimePath)
	}
	runtimeStore, err := businessruntime.Open(runtimePath, registry)
	if err != nil {
		logger.Error("open business runtime", "error", err)
		os.Exit(1)
	}
	defer runtimeStore.Close()
	runtimeStore.SetAgentToolRegistry(toolRegistry)
	if err := runtimeStore.ConfigureMCPCredentials(os.Getenv("CONTENT_AGENT_MCP_CREDENTIAL_KEY")); err != nil {
		logger.Error("configure MCP credential storage", "error", err)
		os.Exit(1)
	}
	scriptSandbox, sandboxErr := scriptsandbox.NewFromEnvironment()
	if sandboxErr != nil {
		logger.Error("configure Skill script sandbox", "error", sandboxErr)
		os.Exit(1)
	}
	runtimeStore.SetScriptSandbox(scriptSandbox)
	sandboxStatus := scriptSandbox.Status()
	nativeEngine, nativeErr := nativeWorkspaceEngineFromEnvironment(scriptSandbox)
	if nativeErr != nil {
		logger.Error("configure native workspace", "error", nativeErr)
		os.Exit(1)
	}
	assessments := executorRegistry.ApplyAvailability(registry, providerSet)
	for _, entry := range registry.Entries() {
		if entry.Status != capability.Available {
			logger.Warn(
				"capability unavailable",
				"capability_id", entry.CapabilityID,
				"reason_code", entry.ReasonCode,
				"validation_message", entry.Message,
			)
		}
	}
	sidecarURL := strings.TrimSpace(os.Getenv("CONTENT_AGENT_OPENAI_SIDECAR_URL"))
	if sidecarURL == "" {
		logger.Error("CONTENT_AGENT_OPENAI_SIDECAR_URL is required after SDK migration")
		os.Exit(1)
	}
	sidecarTimeoutSeconds, timeoutErr := integerEnvironment("CONTENT_AGENT_OPENAI_SIDECAR_TIMEOUT_SECONDS", 420)
	if timeoutErr != nil || sidecarTimeoutSeconds <= 0 {
		logger.Error("load OpenAI sidecar timeout", "error", timeoutErr)
		os.Exit(1)
	}
	sidecarToken := os.Getenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN")
	sidecarTimeout := time.Duration(sidecarTimeoutSeconds) * time.Second
	selectedAgent, sidecarErr := shell.NewRequiredSidecarAgentService(sidecarURL, sidecarToken, sidecarTimeout)
	if sidecarErr != nil {
		logger.Error("configure required OpenAI Agents sidecar", "error", sidecarErr)
		os.Exit(1)
	}
	revisionService, revisionErr := revision.NewSidecar(runtimeStore, sidecarURL, sidecarToken, sidecarTimeout)
	if revisionErr != nil {
		logger.Error("configure SDK revision service", "error", revisionErr)
		os.Exit(1)
	}
	imageMode := "openai_agents_sdk"
	core := shell.NewWithAgentService(registry, selectedAgent)

	api := httpapi.NewWithRuntimeRevisionAndTools(
		core, runtimeStore, revisionService, toolRegistry, logger,
	)
	var nativeCleanup *businessruntime.NativeWorkspaceManager
	if nativeEngine != nil {
		nativeCleanup, err = businessruntime.NewNativeWorkspaceManager(runtimeStore, nativeEngine)
		if err != nil {
			logger.Error("configure native workspace lifecycle", "error", err)
			os.Exit(1)
		}
		api.ConfigureNativeWorkspaceEngine(nativeEngine)
	}
	agentRolloutEnabled, rolloutErr := booleanEnvironment("CONTENT_AGENT_SDK_AGENT_ENABLED", true)
	if rolloutErr != nil {
		logger.Error("load SDK Agent rollout", "error", rolloutErr)
		os.Exit(1)
	}
	canaryWorkspaces := splitEnvironmentList("CONTENT_AGENT_SDK_CANARY_WORKSPACES")
	releaseID := environmentOrDefault("CONTENT_AGENT_RELEASE_ID", "dev")
	agentRollout := httpapi.NewAgentRolloutPolicy(agentRolloutEnabled, canaryWorkspaces, releaseID)
	api.ConfigureAgentRollout(agentRollout)
	authConfigPath := strings.TrimSpace(os.Getenv("CONTENT_AGENT_AUTH_CONFIG_PATH"))
	if authConfigPath != "" && !filepath.IsAbs(authConfigPath) {
		authConfigPath = filepath.Join(root, authConfigPath)
	}
	authenticator, authErr := httpapi.LoadAuthenticator(
		authConfigPath, httpapi.IsLoopbackListenAddress(*listen),
	)
	if authErr != nil {
		logger.Error("configure end-user authentication", "error", authErr)
		os.Exit(1)
	}
	if err := api.ConfigureAuthentication(context.Background(), authenticator); err != nil {
		logger.Error("bootstrap end-user identities", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              *listen,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	logger.Info(
		"content agent server starting",
		"listen", *listen,
		"project_root", root,
		"capabilities", len(registry.Entries()),
		"available_capabilities", registry.CountByStatus(capability.Available),
		"main_agent", mainAgentMode,
		"control_mode", controlMode,
		"image_mode", imageMode,
		"deployment_assessments", len(assessments),
		"agent_tools", len(toolRegistry.PublicCatalog().Tools),
		"script_sandbox_available", sandboxStatus.Available,
		"script_sandbox_adapter", sandboxStatus.Adapter,
		"native_workspace_configured", nativeEngine != nil,
		"database", runtimePath,
		"execution_worker", "openai_agents_sdk_sidecar",
		"agent_rollout", agentRollout.Status(),
	)

	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go runRetentionWorker(
		shutdownContext,
		runtimeStore,
		logger,
		*retentionInterval,
		*retentionLeaseSeconds,
		*retentionMaxAttempts,
	)
	go revisionService.Run(shutdownContext, 2*time.Second, logger)
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		logger.Error("listen HTTP", "error", err)
		os.Exit(1)
	}
	if err := api.StartAgentTurnManager(shutdownContext); err != nil {
		_ = listener.Close()
		logger.Error("start Agent turn manager", "error", err)
		os.Exit(1)
	}
	cleanupDone := startNativeWorkspaceCleanup(shutdownContext, nativeCleanup, logger)
	defer func() {
		stop()
		// Let bounded receipt writes finish before Store.Close. A timeout keeps
		// the durable pending state for the next authorized host startup.
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := waitNativeWorkspaceCleanup(ctx, cleanupDone); err != nil {
			logger.Error("native workspace cleanup shutdown remains unconfirmed")
		}
	}()
	defer func() {
		stop()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := api.WaitAgentTurnManager(ctx); err != nil {
			logger.Error("wait for Agent turn shutdown", "error", err)
		}
	}()
	go func() {
		<-shutdownContext.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			logger.Error("shutdown HTTP server", "error", err)
		}
	}()

	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve HTTP", "error", err)
		os.Exit(1)
	}
}

func environmentOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func booleanEnvironment(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return parsed, nil
}

func integerEnvironment(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func splitEnvironmentList(name string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	values := make([]string, 0)
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func countNonEmptyStrings(values []string) int {
	count := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			count++
		}
	}
	return count
}

func runRetentionWorker(
	ctx context.Context,
	store *businessruntime.Store,
	logger *slog.Logger,
	interval time.Duration,
	leaseSeconds int,
	maxAttempts int,
) {
	workerID := fmt.Sprintf("runtime-retention-%d", os.Getpid())
	run := func() {
		result, err := store.RunRetentionSweep(
			ctx,
			workerID,
			20,
			leaseSeconds,
			maxAttempts,
		)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("retention sweep failed", "error", err)
			return
		}
		if result.Claimed > 0 {
			logger.Info(
				"retention sweep completed",
				"claimed", result.Claimed,
				"completed", result.Completed,
				"retried", result.Retried,
				"failed", result.Failed,
			)
		}
		pruned, err := store.PruneSecurityAuditEvents(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("security audit retention sweep failed", "error", err)
			return
		}
		if pruned > 0 {
			logger.Info("security audit retention sweep completed", "pruned", pruned)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func resolveProjectRoot(explicit string) (string, error) {
	if explicit != "" {
		root, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		return root, nil
	}
	if fromEnvironment := os.Getenv("CONTENT_AGENT_PROJECT_ROOT"); fromEnvironment != "" {
		root, err := filepath.Abs(fromEnvironment)
		if err != nil {
			return "", err
		}
		return root, nil
	}

	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for range 8 {
		if info, statErr := os.Stat(filepath.Join(current, "capabilities", "v1")); statErr == nil && info.IsDir() {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", fmt.Errorf("cannot find capabilities/v1; set -project-root or CONTENT_AGENT_PROJECT_ROOT")
}
