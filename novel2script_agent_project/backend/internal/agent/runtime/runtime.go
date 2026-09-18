package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novel2script-agent/backend/internal/agent"
)

type PlannedArtifact struct {
	ArtifactType string
	Status       agent.ArtifactStatus
	Payload      map[string]any
}

type ContentWorker interface {
	PlanStep(ctx context.Context, run agent.Run, source agent.Artifact, existing []agent.Artifact, artifactType string) (PlannedArtifact, *agent.ApprovalRequest, error)
	WriteScriptStep(ctx context.Context, run agent.Run, artifacts []agent.Artifact, artifactType string, note string) (PlannedArtifact, error)
}

type PatchContentWorker interface {
	PatchArtifactStep(ctx context.Context, run agent.Run, target agent.Artifact, existing []agent.Artifact, artifactType string, instruction string) (PlannedArtifact, error)
}

var fullArtifactSequenceByMode = map[agent.SourceMode][]string{
	agent.SourceModeNovel:    {"story_bible", "episode_split", "episode_cards", "script_context", "script_unit", "scripts"},
	agent.SourceModeNonNovel: {"material_bank", "story_seed", "series_blueprint", "episode_cards", "script_context", "script_unit", "scripts"},
}

type Runtime struct {
	mu                 sync.Mutex
	runs               map[string]agent.Run
	events             map[string][]agent.RunEvent
	artifacts          map[string][]agent.Artifact
	approvals          map[string]agent.ApprovalRequest
	counter            int64
	worker             ContentWorker
	runtimeName        string
	statePath          string
	lastPersistenceErr error
	jobs               map[string]runtimeJob
	pausedJobs         map[string]func(context.Context)
	jobCounter         int64
	jobsWG             sync.WaitGroup
}

type runtimeJob struct {
	id     int64
	cancel context.CancelFunc
	start  func(context.Context)
}

var ErrProjectBusy = errors.New("project has an active run")

func normalizeGenerationConfig(config *agent.GenerationConfig) map[string]any {
	if config == nil || config.Empty() {
		return map[string]any{}
	}
	targetScriptChars := config.TargetScriptChars
	if targetScriptChars == 0 && config.EpisodeDurationMinutes > 0 {
		targetScriptChars = int(config.EpisodeDurationMinutes * 333)
	}
	boundaryWindow := config.BoundaryDetectionWindowChars
	if boundaryWindow == 0 {
		boundaryWindow = 800
	}
	return map[string]any{
		"target_episode_count":              config.TargetEpisodeCount,
		"episode_duration_minutes":          config.EpisodeDurationMinutes,
		"target_script_chars":               targetScriptChars,
		"target_source_chars_per_episode":   config.TargetSourceCharsPerEpisode,
		"boundary_detection_window_chars":   boundaryWindow,
		"preserve_existing_episode_marks":   config.PreserveExistingEpisodeMarks,
		"existing_episode_markers_detected": config.ExistingEpisodeMarkersDetected,
		"detected_episode_count":            config.DetectedEpisodeCount,
	}
}

func NewRuntime(worker ContentWorker) *Runtime {
	return NewRuntimeWithStateAndName(worker, "", "native")
}

func NewRuntimeWithState(worker ContentWorker, statePath string) *Runtime {
	return NewRuntimeWithStateAndName(worker, statePath, "native")
}

func NewRuntimeWithStateAndName(worker ContentWorker, statePath string, runtimeName string) *Runtime {
	runtimeName = strings.ToLower(strings.TrimSpace(runtimeName))
	if runtimeName == "" {
		runtimeName = "native"
	}
	runtime := &Runtime{
		runs:        make(map[string]agent.Run),
		events:      make(map[string][]agent.RunEvent),
		artifacts:   make(map[string][]agent.Artifact),
		approvals:   make(map[string]agent.ApprovalRequest),
		worker:      worker,
		runtimeName: runtimeName,
		statePath:   statePath,
		jobs:        make(map[string]runtimeJob),
		pausedJobs:  make(map[string]func(context.Context)),
	}
	if statePath != "" {
		runtime.loadState(statePath)
	}
	return runtime
}

type runtimeState struct {
	Runs      map[string]agent.Run             `json:"runs"`
	Events    map[string][]agent.RunEvent      `json:"events"`
	Artifacts map[string][]agent.Artifact      `json:"artifacts"`
	Approvals map[string]agent.ApprovalRequest `json:"approvals"`
	Counter   int64                            `json:"counter"`
}

func (r *Runtime) loadState(path string) {
	if strings.EqualFold(filepath.Ext(path), ".db") {
		state, found, err := readRuntimeStateSQLite(path)
		if err != nil {
			r.lastPersistenceErr = fmt.Errorf("load runtime database: %w", err)
			return
		}
		if !found {
			legacyPath := filepath.Join(filepath.Dir(path), "mock_runtime_state.json")
			if legacyState, legacyErr := readRuntimeState(legacyPath); legacyErr == nil {
				state = legacyState
				found = true
			}
		}
		if !found {
			return
		}
		r.applyLoadedState(state)
		return
	}
	state, err := readRuntimeState(path)
	if err != nil {
		backupState, backupErr := readRuntimeState(path + ".bak")
		if backupErr != nil {
			if !os.IsNotExist(err) || !os.IsNotExist(backupErr) {
				r.lastPersistenceErr = fmt.Errorf("load runtime state: primary: %v; backup: %v", err, backupErr)
			}
			return
		}
		state = backupState
	}
	r.applyLoadedState(state)
}

func (r *Runtime) applyLoadedState(state runtimeState) {
	if state.Runs != nil {
		r.runs = state.Runs
	}
	if state.Events != nil {
		r.events = state.Events
	}
	if state.Artifacts != nil {
		r.artifacts = state.Artifacts
	}
	if state.Approvals != nil {
		r.approvals = state.Approvals
	}
	r.counter = state.Counter
	r.reconcileGenerationConfigCopies()
	r.reconcileLegacyImmediateInvalidations()
	r.reconcileInterruptedRuns()
	r.reconcileOrphanedRevisionApprovals()
	r.reconcileLoadedApprovals()
	r.saveLocked()
}

func (r *Runtime) reconcileOrphanedRevisionApprovals() {
	for runID, run := range r.runs {
		if run.Status != agent.RunFailed || run.ApprovalRequestID != "" {
			continue
		}
		review := pendingDownstreamReviewFromRun(run)
		if review == nil {
			continue
		}
		artifactType := strings.TrimSpace(fmt.Sprint(review["source_artifact_type"]))
		if artifactType == "" || artifactType == "<nil>" {
			continue
		}
		hasPendingArtifact := false
		for _, artifact := range r.artifacts[runID] {
			if artifact.ArtifactType == artifactType && artifact.Status == agent.ArtifactPendingApproval {
				hasPendingArtifact = true
				break
			}
		}
		if !hasPendingArtifact {
			continue
		}
		r.createDownstreamReviewApprovalLocked(runID, artifactType, stringSliceValue(review["artifact_ids"]), stringSliceValue(review["artifact_types"]))
		run = r.runs[runID]
		run.Metadata = cloneMetadata(run.Metadata)
		delete(run.Metadata, "active_revision")
		delete(run.Metadata, "active_revision_instruction")
		run.Metadata["recovery"] = map[string]any{
			"code":          "ORPHANED_REVISION_APPROVAL_RESTORED",
			"artifact_type": artifactType,
		}
		r.runs[runID] = run
	}
}

func (r *Runtime) reconcileGenerationConfigCopies() {
	for runID, run := range r.runs {
		run.Metadata = cloneMetadata(run.Metadata)
		config := authoritativeGenerationConfigLocked(run)
		if len(config) == 0 {
			for _, artifact := range r.artifacts[runID] {
				if candidate, ok := artifact.Payload["generation_config"].(map[string]any); ok && len(candidate) > 0 {
					config = clonePayload(candidate)
					break
				}
			}
		}
		if len(config) == 0 {
			continue
		}
		version := generationConfigVersion(run)
		run.Metadata["generation_config"] = config
		run.Metadata["generation_config_version"] = version
		r.runs[runID] = run
		for index := range r.artifacts[runID] {
			payload := clonePayload(r.artifacts[runID][index].Payload)
			delete(payload, "generation_config")
			if _, exists := payload["generation_config_ref"]; !exists {
				payload["generation_config_ref"] = configReference(runID, version)
			}
			r.artifacts[runID][index].Payload = payload
		}
	}
}

func readRuntimeState(path string) (runtimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return runtimeState{}, err
	}
	var state runtimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return runtimeState{}, err
	}
	return state, nil
}

func (r *Runtime) reconcileInterruptedRuns() {
	now := time.Now().UTC()
	for runID, run := range r.runs {
		if run.Status != agent.RunRunning {
			continue
		}
		stepID := run.CurrentStepID
		activeTask := activeTaskFromRun(run)
		if taskStepID := taskString(activeTask, "step_id"); taskStepID != "" {
			stepID = taskStepID
		}
		run.Status = agent.RunFailed
		run.CurrentStepID = stepID
		run.EndedAt = &now
		run.Metadata = cloneMetadata(run.Metadata)
		run.Metadata["recovery"] = map[string]any{
			"code":           "SERVICE_RESTART_INTERRUPTED",
			"recoverable":    true,
			"interrupted_at": now,
			"task_cursor":    taskCursorFromRun(run),
		}
		run.NextAction = map[string]any{
			"capability_id": "retry_failed_step",
			"step_id":       stepID,
			"task_cursor":   taskCursorFromRun(run),
			"retryable":     true,
		}
		if artifactType := taskString(activeTask, "artifact_type"); artifactType != "" {
			run.NextAction["artifact_type"] = artifactType
		}
		r.runs[runID] = run
		payload := map[string]any{
			"code":        "SERVICE_RESTART_INTERRUPTED",
			"recoverable": true,
			"retryable":   true,
		}
		if activeTask != nil {
			payload["task"] = activeTask
		}
		r.events[runID] = append(r.events[runID], r.event(runID, stepID, agent.EventStepFailed, "Generation was interrupted by a service restart and can be retried", nil, nil, payload))
	}
}

func (r *Runtime) reconcileLegacyImmediateInvalidations() {
	for runID, run := range r.runs {
		if run.Status != agent.RunWaitingApproval || len(run.Invalidated) > 0 || run.ApprovalRequestID == "" {
			continue
		}
		approval, ok := r.approvals[run.ApprovalRequestID]
		if !ok || approval.Status != "pending" {
			continue
		}
		targetType := approvalArtifactType(approval, run.CurrentStepID)
		if targetType == "" {
			continue
		}
		affectedTypes := map[string]bool{}
		for _, artifactType := range affectedArtifactsAfter(run.SourceMode, targetType) {
			affectedTypes[artifactType] = true
		}
		if targetType == "script_unit" {
			affectedTypes["scripts"] = true
		}
		artifacts := r.artifacts[runID]
		restoredIDs := []string{}
		restoredTypes := []string{}
		episodeIDs := []any{}
		seenTypes := map[string]bool{}
		seenEpisodes := map[int]bool{}
		for index := range artifacts {
			if artifacts[index].Status != agent.ArtifactInvalidated || !affectedTypes[artifacts[index].ArtifactType] {
				continue
			}
			artifacts[index].Status = agent.ArtifactConfirmed
			restoredIDs = append(restoredIDs, artifacts[index].ArtifactID)
			if !seenTypes[artifacts[index].ArtifactType] {
				restoredTypes = append(restoredTypes, artifacts[index].ArtifactType)
				seenTypes[artifacts[index].ArtifactType] = true
			}
			if artifacts[index].ArtifactType == "script_unit" {
				episodeID := episodeNumber(artifacts[index].Payload["episode_id"])
				if episodeID > 0 && !seenEpisodes[episodeID] {
					episodeIDs = append(episodeIDs, episodeID)
					seenEpisodes[episodeID] = true
				}
			}
		}
		if len(restoredIDs) == 0 {
			continue
		}
		r.artifacts[runID] = artifacts
		run.Metadata = cloneMetadata(run.Metadata)
		run.Metadata["pending_downstream_review"] = map[string]any{
			"source_artifact_type": targetType,
			"artifact_ids":         restoredIDs,
			"artifact_types":       restoredTypes,
			"episode_ids":          episodeIDs,
			"revision":             map[string]any{"scope": "artifact", "migrated_from_immediate_invalidation": true},
		}
		r.runs[runID] = run
		approval.Title = "确认" + artifactDisplayName(targetType) + "及后续处理"
		approval.Reason = artifactDisplayName(targetType) + "已生成新版本。已有后续内容可能受到影响，请选择保留现有后续内容，或重新生成受影响内容。"
		approval.AffectedArtifacts = restoredTypes
		approval.Options = []string{"keep_downstream", "regenerate_downstream", "revise_instruction", "pause"}
		if approval.ProposedAction == nil {
			approval.ProposedAction = map[string]any{}
		}
		approval.ProposedAction["approved_artifact"] = targetType
		approval.ProposedAction["downstream_review"] = true
		approval.ProposedAction["affected_artifact_ids"] = restoredIDs
		approval.ProposedAction["affected_artifact_types"] = restoredTypes
		r.approvals[approval.ApprovalRequestID] = approval
	}
}

func (r *Runtime) reconcileLoadedApprovals() {
	now := time.Now().UTC()
	for approvalID, approval := range r.approvals {
		if approval.Status != "pending" {
			continue
		}
		run, ok := r.runs[approval.RunID]
		if ok && run.Status == agent.RunWaitingApproval && run.ApprovalRequestID == approvalID {
			continue
		}
		approval.Status = "expired"
		approval.ResolvedAt = &now
		approval.UserResponse = "expired during runtime state recovery"
		r.approvals[approvalID] = approval
	}
}

func (r *Runtime) saveLocked() {
	if r.statePath == "" {
		r.lastPersistenceErr = nil
		return
	}
	state := runtimeState{
		Runs:      r.runs,
		Events:    r.events,
		Artifacts: r.artifacts,
		Approvals: r.approvals,
		Counter:   r.counter,
	}
	if strings.EqualFold(filepath.Ext(r.statePath), ".db") {
		if err := writeRuntimeStateSQLite(r.statePath, state); err != nil {
			r.lastPersistenceErr = fmt.Errorf("save runtime database: %w", err)
			return
		}
		r.lastPersistenceErr = nil
		return
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		r.lastPersistenceErr = fmt.Errorf("encode runtime state: %w", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.statePath), 0o755); err != nil {
		r.lastPersistenceErr = fmt.Errorf("create runtime state directory: %w", err)
		return
	}
	tmpPath := r.statePath + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		r.lastPersistenceErr = fmt.Errorf("open runtime state temp file: %w", err)
		return
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		r.lastPersistenceErr = fmt.Errorf("write runtime state: %w", err)
		return
	}
	backupPath := r.statePath + ".bak"
	if _, statErr := os.Stat(r.statePath); statErr == nil {
		_ = os.Remove(backupPath)
		if err := os.Rename(r.statePath, backupPath); err != nil {
			r.lastPersistenceErr = fmt.Errorf("rotate runtime state backup: %w", err)
			return
		}
	}
	if err := os.Rename(tmpPath, r.statePath); err != nil {
		_ = os.Rename(backupPath, r.statePath)
		r.lastPersistenceErr = fmt.Errorf("commit runtime state: %w", err)
		return
	}
	r.lastPersistenceErr = nil
}

func (r *Runtime) PersistenceError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastPersistenceErr
}

func (r *Runtime) launchJobLocked(runID string, start func(context.Context)) {
	if current, ok := r.jobs[runID]; ok {
		current.cancel()
	}
	r.jobCounter++
	jobID := r.jobCounter
	ctx, cancel := context.WithCancel(context.Background())
	r.jobs[runID] = runtimeJob{id: jobID, cancel: cancel, start: start}
	delete(r.pausedJobs, runID)
	r.jobsWG.Add(1)
	go func() {
		defer r.jobsWG.Done()
		defer func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if current, ok := r.jobs[runID]; ok && current.id == jobID {
				delete(r.jobs, runID)
			}
		}()
		start(ctx)
	}()
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	for _, job := range r.jobs {
		job.cancel()
	}
	r.mu.Unlock()
	done := make(chan struct{})
	go func() {
		r.jobsWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) PauseRunningRun(runID string, reason string) (agent.Run, []agent.RunEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return agent.Run{}, nil, fmt.Errorf("run not found: %s", runID)
	}
	if run.Status != agent.RunRunning {
		return agent.Run{}, nil, fmt.Errorf("only a running run can be paused")
	}
	job, ok := r.jobs[runID]
	if !ok {
		return agent.Run{}, nil, fmt.Errorf("run has no active model job")
	}
	r.pausedJobs[runID] = job.start
	job.cancel()
	run.Status = agent.RunPaused
	run.Metadata = cloneMetadata(run.Metadata)
	if task := activeTaskFromRun(run); task != nil {
		task["status"] = "paused"
		run.Metadata["active_task"] = task
	}
	run.Metadata["paused_by_user"] = true
	run.NextAction = map[string]any{"capability_id": "resume_paused_run", "step_id": run.CurrentStepID, "task_cursor": taskCursorFromRun(run)}
	r.runs[runID] = run
	event := r.event(runID, run.CurrentStepID, agent.EventRunPaused, "Run paused by user; completed artifacts were kept", nil, nil, map[string]any{"reason": reason, "task_cursor": taskCursorFromRun(run)})
	r.events[runID] = append(r.events[runID], event)
	r.saveLocked()
	return run, []agent.RunEvent{event}, nil
}

func (r *Runtime) ResumePausedRun(runID string) (agent.Run, []agent.RunEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return agent.Run{}, nil, fmt.Errorf("run not found: %s", runID)
	}
	if run.Status != agent.RunPaused {
		return agent.Run{}, nil, fmt.Errorf("only a paused run can be resumed")
	}
	pausedByUser, _ := run.Metadata["paused_by_user"].(bool)
	if !pausedByUser {
		return agent.Run{}, nil, fmt.Errorf("run was not paused by the user")
	}
	if run.ApprovalRequestID != "" {
		run.Status = agent.RunWaitingApproval
		run.Metadata = cloneMetadata(run.Metadata)
		delete(run.Metadata, "paused_by_user")
		delete(run.Metadata, "paused_from_status")
		run.NextAction = map[string]any{"capability_id": "wait_for_approval", "step_id": run.CurrentStepID}
		r.runs[runID] = run
		event := r.event(runID, run.CurrentStepID, agent.EventRunResumed, "Run resumed at pending approval", nil, nil, map[string]any{"approval_request_id": run.ApprovalRequestID})
		r.events[runID] = append(r.events[runID], event)
		r.saveLocked()
		return run, []agent.RunEvent{event}, nil
	}
	start, ok := r.pausedJobs[runID]
	if !ok {
		return agent.Run{}, nil, fmt.Errorf("paused job cannot be resumed after service restart; retry the recorded step instead")
	}
	run.Status = agent.RunRunning
	run.EndedAt = nil
	run.Metadata = cloneMetadata(run.Metadata)
	delete(run.Metadata, "paused_by_user")
	if task := activeTaskFromRun(run); task != nil {
		task["status"] = "running"
		run.Metadata["active_task"] = task
	}
	run.NextAction = map[string]any{"capability_id": "resume_paused_run", "step_id": run.CurrentStepID, "task_cursor": taskCursorFromRun(run)}
	r.runs[runID] = run
	event := r.event(runID, run.CurrentStepID, agent.EventRunResumed, "Run resumed from paused task", nil, nil, map[string]any{"task_cursor": taskCursorFromRun(run)})
	r.events[runID] = append(r.events[runID], event)
	r.saveLocked()
	r.launchJobLocked(runID, start)
	return run, []agent.RunEvent{event}, nil
}

func (r *Runtime) StartRun(req agent.StartRunRequest) (agent.StartRunResponse, error) {
	return r.StartRunAsyncContext(context.Background(), req)
}

func (r *Runtime) StartRunAsyncContext(ctx context.Context, req agent.StartRunRequest) (agent.StartRunResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.worker == nil {
		return agent.StartRunResponse{}, fmt.Errorf("content worker is not configured")
	}

	now := time.Now().UTC()
	sourceText := strings.TrimSpace(req.SourceText)
	if sourceText == "" {
		sourceText = req.UserMessage
	}
	sourceMode := req.SourceMode
	if sourceMode == "" || sourceMode == agent.SourceModeUnknown || sourceMode == agent.SourceModeAuto {
		sourceMode = detectSourceMode(sourceText)
	}

	projectID := req.ProjectID
	if projectID == "" {
		projectID = "project_demo"
	}

	runID := r.nextID("run")
	run := agent.Run{
		RunID:         runID,
		ProjectID:     projectID,
		Intent:        intentForSource(sourceMode),
		SourceMode:    sourceMode,
		Status:        agent.RunRunning,
		CurrentStepID: "step_ingest_source",
		NextAction: map[string]any{
			"capability_id": "ingest_source",
		},
		StartedAt: now,
		Metadata: map[string]interface{}{
			"user_message":              req.UserMessage,
			"runtime":                   r.runtimeName,
			"generation_config":         normalizeGenerationConfig(req.GenerationConfig),
			"generation_config_version": 1,
		},
	}

	events := []agent.RunEvent{
		r.event(runID, "", agent.EventRunStarted, "Agent run started", nil, nil, nil),
		r.event(runID, "step_route", agent.EventStepStarted, "Detecting user intent", nil, nil, map[string]any{
			"source_mode": sourceMode,
		}),
	}

	source := r.artifact(run, "source_input", agent.ArtifactConfirmed, nil, map[string]any{
		"source_mode":           sourceMode,
		"text":                  sourceText,
		"attachments":           req.SourceFiles,
		"target_format":         "mini_short_drama",
		"generation_config_ref": configReference(runID, 1),
		"notes":                 []string{},
	})
	events = append(events, r.event(runID, "step_ingest_source", agent.EventArtifactCreated, "Source input saved", []string{source.ArtifactID}, focus(source), nil))
	run.CreatedArtifacts = []string{source.ArtifactID}

	r.runs[runID] = run
	r.events[runID] = events
	r.artifacts[runID] = []agent.Artifact{source}
	r.saveLocked()

	r.launchJobLocked(runID, func(ctx context.Context) { r.planRun(ctx, runID, source.ArtifactID) })

	return agent.StartRunResponse{
		Run:       run,
		Events:    events,
		Artifacts: []agent.Artifact{source},
	}, nil
}

func (r *Runtime) ContinueRun(runID string, req agent.ContinueRunRequest) (agent.ContinueRunResponse, error) {
	return r.ContinueRunAsyncContext(context.Background(), runID, req)
}

func (r *Runtime) ContinueRunAsyncContext(ctx context.Context, runID string, req agent.ContinueRunRequest) (agent.ContinueRunResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.worker == nil {
		return agent.ContinueRunResponse{}, fmt.Errorf("content worker is not configured")
	}

	run, ok := r.runs[runID]
	if !ok {
		return agent.ContinueRunResponse{}, fmt.Errorf("run not found: %s", runID)
	}
	if run.Status != agent.RunWaitingApproval && !(run.Status == agent.RunPaused && run.ApprovalRequestID != "") {
		return agent.ContinueRunResponse{}, fmt.Errorf("run is not waiting for approval")
	}

	if req.Decision != "" && req.Decision != "approve" && req.Decision != "auto_continue_remaining" && req.Decision != "keep_downstream" && req.Decision != "regenerate_downstream" {
		return r.pauseRun(run, req), nil
	}

	now := time.Now().UTC()
	approval, ok := r.approvals[run.ApprovalRequestID]
	if review := pendingDownstreamReviewFromRun(run); review != nil {
		decision := req.Decision
		if decision == "" || decision == "approve" || decision == "auto_continue_remaining" {
			decision = "keep_downstream"
		}
		if ok {
			approval.Status = "approved"
			approval.UserResponse = decision
			approval.ResolvedAt = &now
			r.approvals[approval.ApprovalRequestID] = approval
		}
		approvedArtifactType := approvalArtifactType(approval, run.CurrentStepID)
		r.confirmArtifactType(runID, approvedArtifactType)
		run.Metadata = cloneMetadata(run.Metadata)
		delete(run.Metadata, "pending_downstream_review")
		event := r.event(runID, run.CurrentStepID, agent.EventApprovalResolved, approvalResolvedMessage(approvedArtifactType), nil, nil, map[string]any{
			"decision":              decision,
			"artifact_type":         approvedArtifactType,
			"affected_artifact_ids": stringSliceValue(review["artifact_ids"]),
		})

		if decision == "keep_downstream" {
			ended := time.Now().UTC()
			run.Status = agent.RunCompleted
			run.CurrentStepID = "step_completed"
			run.NextAction = map[string]any{}
			run.ApprovalRequestID = ""
			run.EndedAt = &ended
			r.runs[runID] = run
			r.events[runID] = append(r.events[runID], event, r.event(runID, "", agent.EventRunCompleted, "Revision completed while keeping existing downstream artifacts", run.CreatedArtifacts, nil, map[string]any{
				"decision": "keep_downstream",
			}))
			r.saveLocked()
			return agent.ContinueRunResponse{Run: run, Events: []agent.RunEvent{event}, Artifacts: r.artifacts[runID]}, nil
		}

		artifactIDs := stringSliceValue(review["artifact_ids"])
		staleIDs := r.markArtifactsStaleLocked(runID, artifactIDs)
		run.Invalidated = appendUniqueStrings(run.Invalidated, staleIDs...)
		run.Metadata["downstream_regeneration"] = review
		affectedTypes := stringSliceValue(review["artifact_types"])
		if len(affectedTypes) == 0 {
			return agent.ContinueRunResponse{}, fmt.Errorf("downstream review has no affected artifact types")
		}
		firstArtifactType := affectedTypes[0]
		run.Status = agent.RunRunning
		run.CurrentStepID = stepIDForArtifact(firstArtifactType)
		run.NextAction = map[string]any{
			"capability_id":         strings.TrimPrefix(stepIDForArtifact(firstArtifactType), "step_"),
			"artifact_type":         firstArtifactType,
			"regenerate_downstream": true,
		}
		run.ApprovalRequestID = ""
		run.EndedAt = nil
		r.runs[runID] = run
		startEvent := r.event(runID, run.CurrentStepID, agent.EventStepStarted, "Regenerating affected downstream artifacts", staleIDs, nil, map[string]any{
			"source_artifact_type":  review["source_artifact_type"],
			"affected_artifact_ids": staleIDs,
		})
		r.events[runID] = append(r.events[runID], event, startEvent)
		r.saveLocked()
		r.launchJobLocked(runID, func(ctx context.Context) { r.generateArtifactAsync(ctx, runID, firstArtifactType, req.Note) })
		return agent.ContinueRunResponse{Run: run, Events: []agent.RunEvent{event, startEvent}, Artifacts: r.artifacts[runID]}, nil
	}
	if ok {
		approval.Status = "approved"
		approval.UserResponse = req.Note
		approval.ResolvedAt = &now
		r.approvals[approval.ApprovalRequestID] = approval
	}

	approvedArtifactType := approvalArtifactType(approval, run.CurrentStepID)
	nextArtifactType, hasNext := nextArtifactAfter(run.SourceMode, approvedArtifactType)
	r.confirmArtifactType(runID, approvedArtifactType)

	event := r.event(runID, run.CurrentStepID, agent.EventApprovalResolved, approvalResolvedMessage(approvedArtifactType), nil, nil, map[string]any{
		"decision":           req.Decision,
		"artifact_type":      approvedArtifactType,
		"next_artifact_type": nextArtifactType,
	})

	if !hasNext {
		ended := time.Now().UTC()
		run.Status = agent.RunCompleted
		run.CurrentStepID = "step_completed"
		run.NextAction = map[string]any{}
		run.ApprovalRequestID = ""
		run.EndedAt = &ended
		r.runs[runID] = run
		r.events[runID] = append(r.events[runID], event, r.event(runID, "", agent.EventRunCompleted, "Run completed", run.CreatedArtifacts, nil, nil))
		r.saveLocked()
		return agent.ContinueRunResponse{
			Run:       run,
			Events:    []agent.RunEvent{event},
			Artifacts: r.artifacts[runID],
		}, nil
	}

	run.Status = agent.RunRunning
	run.CurrentStepID = stepIDForArtifact(nextArtifactType)
	run.NextAction = map[string]any{
		"capability_id": strings.TrimPrefix(stepIDForArtifact(nextArtifactType), "step_"),
		"artifact_type": nextArtifactType,
	}
	run.ApprovalRequestID = ""
	r.runs[runID] = run
	r.events[runID] = append(r.events[runID], event)
	currentArtifacts := append([]agent.Artifact(nil), r.artifacts[runID]...)
	r.saveLocked()

	r.launchJobLocked(runID, func(ctx context.Context) { r.generateArtifactAsync(ctx, runID, nextArtifactType, req.Note) })

	return agent.ContinueRunResponse{
		Run:       run,
		Events:    []agent.RunEvent{event},
		Artifacts: currentArtifacts,
	}, nil
}

func (r *Runtime) markArtifactsStaleLocked(runID string, artifactIDs []string) []string {
	if len(artifactIDs) == 0 {
		return nil
	}
	targets := map[string]bool{}
	for _, artifactID := range artifactIDs {
		targets[artifactID] = true
	}
	artifacts := r.artifacts[runID]
	updated := []string{}
	for index := range artifacts {
		if !targets[artifacts[index].ArtifactID] || artifacts[index].Status == agent.ArtifactSuperseded || artifacts[index].Status == agent.ArtifactInvalidated {
			continue
		}
		artifacts[index].Status = agent.ArtifactStale
		artifacts[index].UpdatedAt = time.Now().UTC()
		updated = append(updated, artifacts[index].ArtifactID)
	}
	r.artifacts[runID] = artifacts
	return updated
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		seen[value] = true
	}
	for _, value := range additions {
		if value == "" || seen[value] {
			continue
		}
		values = append(values, value)
		seen[value] = true
	}
	return values
}

func (r *Runtime) GetRun(runID string) (agent.Run, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	return run, ok
}

func (r *Runtime) LatestRunForProject(projectID string) (agent.Run, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var latest agent.Run
	found := false
	for _, run := range r.runs {
		if run.ProjectID != projectID {
			continue
		}
		if !found || run.StartedAt.After(latest.StartedAt) || (run.StartedAt.Equal(latest.StartedAt) && run.RunID > latest.RunID) {
			latest = run
			found = true
		}
	}
	return latest, found
}

func (r *Runtime) Runs() []agent.Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	runs := make([]agent.Run, 0, len(r.runs))
	for _, run := range r.runs {
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].StartedAt.Equal(runs[j].StartedAt) {
			return runs[i].RunID < runs[j].RunID
		}
		return runs[i].StartedAt.Before(runs[j].StartedAt)
	})
	return runs
}

// DeleteProject removes every runtime record owned by a project. Active model
// jobs are rejected so deletion can never race with artifact persistence.
func (r *Runtime) DeleteProject(projectID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Errorf("project id is required")
	}
	runIDs := map[string]bool{}
	for runID, run := range r.runs {
		if run.ProjectID != projectID {
			continue
		}
		if run.Status == agent.RunPending || run.Status == agent.RunRunning {
			return ErrProjectBusy
		}
		if _, active := r.jobs[runID]; active {
			return ErrProjectBusy
		}
		runIDs[runID] = true
	}
	for runID := range runIDs {
		delete(r.runs, runID)
		delete(r.events, runID)
		delete(r.artifacts, runID)
		delete(r.pausedJobs, runID)
	}
	for approvalID, approval := range r.approvals {
		if runIDs[approval.RunID] {
			delete(r.approvals, approvalID)
		}
	}
	r.saveLocked()
	return r.lastPersistenceErr
}

func (r *Runtime) RerunStep(runID string, stepID string, req agent.RerunStepRequest) (agent.RerunStepResponse, error) {
	r.mu.Lock()
	run, ok := r.runs[runID]
	if !ok {
		r.mu.Unlock()
		return agent.RerunStepResponse{}, fmt.Errorf("run not found: %s", runID)
	}
	if stepID == "" {
		stepID = run.CurrentStepID
	}
	failedStepID := failedStepIDFromRun(run)
	restoredFailure := lastFailedTaskFromRun(run)
	if run.Status != agent.RunFailed && !(run.Status == agent.RunWaitingApproval && restoredFailure != nil) {
		r.mu.Unlock()
		return agent.RerunStepResponse{}, fmt.Errorf("only failed runs or restored failed revisions can be rerun")
	}
	if failedStepID == "" {
		r.mu.Unlock()
		return agent.RerunStepResponse{}, fmt.Errorf("failed step is not recorded")
	}
	if stepID != failedStepID {
		r.mu.Unlock()
		return agent.RerunStepResponse{}, fmt.Errorf("only failed step can be rerun: %s", failedStepID)
	}
	artifactType := artifactTypeFromStepID(stepID)
	if artifactType == "" {
		r.mu.Unlock()
		return agent.RerunStepResponse{}, fmt.Errorf("step cannot be rerun: %s", stepID)
	}
	if r.worker == nil {
		r.mu.Unlock()
		return agent.RerunStepResponse{}, fmt.Errorf("content worker is not configured")
	}

	run.Metadata = cloneMetadata(run.Metadata)
	if restoredFailure != nil {
		restoredFailure = clonePayload(restoredFailure)
		restoredFailure["status"] = "running"
		run.Metadata["active_task"] = restoredFailure
		if revision := mapValue(run.Metadata["last_failed_revision"]); len(revision) > 0 {
			run.Metadata["active_revision"] = clonePayload(revision)
		}
		if instruction := strings.TrimSpace(fmt.Sprint(run.Metadata["last_failed_revision_instruction"])); instruction != "" && instruction != "<nil>" {
			run.Metadata["active_revision_instruction"] = instruction
		}
		if run.ApprovalRequestID != "" {
			run.Metadata["revision_previous_approval_id"] = run.ApprovalRequestID
		}
		delete(run.Metadata, "last_failed_task")
		delete(run.Metadata, "last_failed_revision")
		delete(run.Metadata, "last_failed_revision_instruction")
		delete(run.Metadata, "last_error")
	}
	run.Status = agent.RunRunning
	run.CurrentStepID = stepID
	run.ApprovalRequestID = ""
	run.NextAction = map[string]any{
		"capability_id": strings.TrimPrefix(stepID, "step_"),
		"artifact_type": artifactType,
		"retry":         true,
	}
	run.EndedAt = nil
	r.runs[runID] = run

	event := r.event(runID, stepID, agent.EventStepStarted, "Resuming from failed task", nil, nil, map[string]any{
		"reason":        req.Reason,
		"artifact_type": artifactType,
		"task_cursor":   taskCursorFromRun(run),
	})
	r.events[runID] = append(r.events[runID], event)
	artifacts := append([]agent.Artifact(nil), r.artifacts[runID]...)
	r.saveLocked()
	retryInstruction := req.Reason
	if stored, _ := run.Metadata["active_revision_instruction"].(string); strings.TrimSpace(stored) != "" {
		retryInstruction = stored
	}
	activeRevision := activeRevisionFromRun(run)
	revisionIntent := strings.TrimSpace(fmt.Sprint(activeRevision["revision_intent"]))
	if isPatchRevisionIntent(revisionIntent) {
		r.launchJobLocked(runID, func(ctx context.Context) { r.patchArtifactAsync(ctx, runID, artifactType, retryInstruction) })
	} else if artifactType == "script_unit" && len(activeRevision) > 0 {
		episodes := episodeTasksFromArtifacts(artifacts)
		cursor := episodeCursorFromInstruction(retryInstruction, episodes)
		r.launchJobLocked(runID, func(ctx context.Context) {
			r.generateScriptUnitsRangeAsync(ctx, runID, retryInstruction, cursor, cursor)
		})
	} else if artifactType == "script_unit" {
		r.launchJobLocked(runID, func(ctx context.Context) {
			r.generateScriptUnitsAsync(ctx, runID, retryInstruction, taskCursorFromRun(run))
		})
	} else {
		r.launchJobLocked(runID, func(ctx context.Context) { r.generateArtifactAsync(ctx, runID, artifactType, retryInstruction) })
	}
	r.mu.Unlock()

	return agent.RerunStepResponse{
		Run:       run,
		Events:    []agent.RunEvent{event},
		Artifacts: artifacts,
	}, nil
}

func (r *Runtime) ReviseCheckpoint(runID string, instruction string) (agent.RerunStepResponse, error) {
	r.mu.Lock()
	run, ok := r.runs[runID]
	if !ok {
		r.mu.Unlock()
		return agent.RerunStepResponse{}, fmt.Errorf("run not found: %s", runID)
	}
	replacingFailedRevision := run.Status == agent.RunFailed && len(mapValue(run.Metadata["active_revision"])) > 0
	if run.Status != agent.RunWaitingApproval && run.Status != agent.RunPaused && run.Status != agent.RunCompleted && !replacingFailedRevision {
		r.mu.Unlock()
		return agent.RerunStepResponse{}, fmt.Errorf("run is not waiting for revision")
	}
	if r.worker == nil {
		r.mu.Unlock()
		return agent.RerunStepResponse{}, fmt.Errorf("content worker is not configured")
	}

	revisionContext := parseRevisionContextPack(instruction)
	approval, _ := r.approvals[run.ApprovalRequestID]
	artifactType := approvalArtifactType(approval, run.CurrentStepID)
	if targetType := strings.TrimSpace(revisionContext.Target.ArtifactType); targetType != "" {
		artifactType = targetType
	}
	if artifactType == "" && strings.TrimSpace(revisionContext.Target.ArtifactID) != "" {
		for _, artifact := range r.artifacts[runID] {
			if artifact.ArtifactID == revisionContext.Target.ArtifactID {
				artifactType = artifact.ArtifactType
				break
			}
		}
	}
	if artifactType == "" {
		artifactType = currentArtifactTypeFromRun(run)
	}
	if approval.ApprovalRequestID != "" && approval.Status == "pending" {
		pendingType := approvalArtifactType(approval, run.CurrentStepID)
		if pendingType != "" && pendingType != artifactType {
			r.mu.Unlock()
			return agent.RerunStepResponse{}, fmt.Errorf("%s is waiting for approval; resolve it before revising %s", pendingType, artifactType)
		}
	}
	if artifactType == "" {
		r.mu.Unlock()
		return agent.RerunStepResponse{}, fmt.Errorf("current checkpoint cannot be revised")
	}

	stepID := stepIDForArtifact(artifactType)
	run.Status = agent.RunRunning
	run.CurrentStepID = stepID
	run.ApprovalRequestID = ""
	run.EndedAt = nil
	run.NextAction = map[string]any{
		"capability_id": strings.TrimPrefix(stepID, "step_"),
		"artifact_type": artifactType,
		"revision":      true,
	}
	run.Metadata = cloneMetadata(run.Metadata)
	delete(run.Metadata, "active_task")
	delete(run.Metadata, "last_failed_task")
	if approval.ApprovalRequestID != "" && approval.Status == "pending" {
		run.Metadata["revision_previous_approval_id"] = approval.ApprovalRequestID
	}
	run.Metadata["active_revision"] = map[string]any{
		"revision_intent": revisionContext.RevisionIntent,
		"artifact_type":   artifactType,
		"episode_id":      revisionContext.Target.EpisodeID,
		"scene_id":        firstNonEmpty(revisionContext.Target.SceneID, revisionContext.FocusedContext.SceneID),
		"field_path":      firstNonEmpty(revisionContext.Target.FieldPath, revisionContext.FocusedContext.FieldPath),
		"scope":           revisionContext.Target.Scope,
	}
	run.Metadata["active_revision_instruction"] = instruction
	r.runs[runID] = run
	event := r.event(runID, stepID, agent.EventStepStarted, "Revising checkpoint", nil, nil, map[string]any{
		"artifact_type":   artifactType,
		"revision_intent": revisionContext.RevisionIntent,
		"scope":           revisionContext.Target.Scope,
	})
	r.events[runID] = append(r.events[runID], event)
	artifacts := append([]agent.Artifact(nil), r.artifacts[runID]...)
	r.saveLocked()
	if isPatchRevisionIntent(revisionContext.RevisionIntent) {
		r.launchJobLocked(runID, func(ctx context.Context) { r.patchArtifactAsync(ctx, runID, artifactType, instruction) })
	} else if artifactType == "script_unit" {
		episodes := episodeTasksFromArtifacts(artifacts)
		cursor := episodeCursorFromInstruction(instruction, episodes)
		r.launchJobLocked(runID, func(ctx context.Context) { r.generateScriptUnitsRangeAsync(ctx, runID, instruction, cursor, cursor) })
	} else {
		r.launchJobLocked(runID, func(ctx context.Context) { r.generateArtifactAsync(ctx, runID, artifactType, instruction) })
	}
	r.mu.Unlock()

	return agent.RerunStepResponse{
		Run:       run,
		Events:    []agent.RunEvent{event},
		Artifacts: artifacts,
	}, nil
}

func (r *Runtime) Events(runID string) []agent.RunEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.RunEvent(nil), r.events[runID]...)
}

func (r *Runtime) Artifacts(runID string) []agent.Artifact {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.Artifact(nil), r.artifacts[runID]...)
}

func (r *Runtime) ArtifactByID(artifactID string) (agent.Artifact, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, artifacts := range r.artifacts {
		for _, artifact := range artifacts {
			if artifact.ArtifactID == artifactID {
				return artifact, true
			}
		}
	}
	return agent.Artifact{}, false
}

type ArtifactUpdateOutcome string

const (
	ArtifactUpdateApplied  ArtifactUpdateOutcome = "applied"
	ArtifactUpdateConflict ArtifactUpdateOutcome = "conflict"
	ArtifactUpdateNotFound ArtifactUpdateOutcome = "not_found"
)

func (r *Runtime) UpdateArtifact(artifactID string, payload map[string]any) (agent.Artifact, []agent.RunEvent, []agent.Artifact, bool) {
	updated, events, artifacts, _, outcome := r.UpdateArtifactAtVersion(artifactID, 0, payload)
	return updated, events, artifacts, outcome == ArtifactUpdateApplied
}

func (r *Runtime) UpdateArtifactAtVersion(artifactID string, baseVersion int, payload map[string]any) (agent.Artifact, []agent.RunEvent, []agent.Artifact, agent.Artifact, ArtifactUpdateOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for runID, artifacts := range r.artifacts {
		for index, artifact := range artifacts {
			if artifact.ArtifactID != artifactID {
				continue
			}
			run := r.runs[runID]
			if artifact.Status == agent.ArtifactSuperseded || artifact.Status == agent.ArtifactInvalidated || (baseVersion > 0 && artifact.Version != baseVersion) {
				current := latestCurrentArtifact(artifacts, artifact)
				return agent.Artifact{}, nil, append([]agent.Artifact(nil), artifacts...), current, ArtifactUpdateConflict
			}

			impactRevision := map[string]any{"scope": "artifact", "artifact_type": artifact.ArtifactType}
			affectedIDs, affectedTypes, episodeIDs := existingAffectedDownstream(artifacts, run.SourceMode, artifact.ArtifactType, impactRevision)
			updatedStatus := artifact.Status
			artifact.Status = agent.ArtifactSuperseded
			artifact.UpdatedAt = time.Now().UTC()
			artifacts[index] = artifact

			updatedPayload := clonePayload(payload)
			configVersion := generationConfigVersion(run)
			if config, ok := updatedPayload["generation_config"].(map[string]any); ok && len(config) > 0 {
				run.Metadata = cloneMetadata(run.Metadata)
				if !reflect.DeepEqual(authoritativeGenerationConfigLocked(run), config) {
					configVersion++
					run.Metadata["generation_config"] = clonePayload(config)
					run.Metadata["generation_config_version"] = configVersion
				}
			}
			delete(updatedPayload, "generation_config")
			updatedPayload["generation_config_ref"] = configReference(runID, configVersion)
			updated := r.artifact(run, artifact.ArtifactType, updatedStatus, []string{artifact.ArtifactID}, updatedPayload)
			updated.Version = r.nextArtifactVersionLocked(runID, updated)
			artifacts = append(artifacts, updated)
			r.artifacts[runID] = artifacts

			run.UpdatedArtifacts = append(run.UpdatedArtifacts, updated.ArtifactID)
			if len(affectedIDs) > 0 {
				run.Metadata = cloneMetadata(run.Metadata)
				run.Metadata["pending_downstream_review"] = map[string]any{
					"source_artifact_type": artifact.ArtifactType,
					"artifact_ids":         affectedIDs,
					"artifact_types":       affectedTypes,
					"episode_ids":          episodeIDs,
					"revision":             impactRevision,
				}
			}
			r.runs[runID] = run

			event := r.event(runID, stepIDForArtifact(updated.ArtifactType), agent.EventArtifactUpdated, eventMessageForArtifact(updated.ArtifactType)+" updated", []string{updated.ArtifactID}, focus(updated), nil)
			r.events[runID] = append(r.events[runID], event)
			if updated.ArtifactType == "script_unit" {
				r.refreshScriptsAggregateLocked(runID)
			}
			if len(affectedIDs) > 0 {
				r.createDownstreamReviewApprovalLocked(runID, updated.ArtifactType, affectedIDs, affectedTypes)
			}
			r.saveLocked()
			return updated, append([]agent.RunEvent(nil), r.events[runID]...), append([]agent.Artifact(nil), r.artifacts[runID]...), updated, ArtifactUpdateApplied
		}
	}
	return agent.Artifact{}, nil, nil, agent.Artifact{}, ArtifactUpdateNotFound
}

func latestCurrentArtifact(artifacts []agent.Artifact, target agent.Artifact) agent.Artifact {
	var latest agent.Artifact
	for _, candidate := range artifacts {
		if candidate.ArtifactType != target.ArtifactType || candidate.Status == agent.ArtifactSuperseded || candidate.Status == agent.ArtifactInvalidated {
			continue
		}
		if target.ArtifactType == "script_unit" && !sameEpisodeID(candidate.Payload, target.Payload) {
			continue
		}
		if latest.ArtifactID == "" || candidate.Version > latest.Version || (candidate.Version == latest.Version && candidate.UpdatedAt.After(latest.UpdatedAt)) {
			latest = candidate
		}
	}
	if latest.ArtifactID == "" {
		return target
	}
	return latest
}

func (r *Runtime) createDownstreamReviewApprovalLocked(runID string, artifactType string, affectedIDs []string, affectedTypes []string) {
	run, ok := r.runs[runID]
	if !ok {
		return
	}
	approvalID := r.nextID("approval")
	approval := agent.ApprovalRequest{
		ApprovalRequestID: approvalID,
		RunID:             runID,
		StepID:            "step_approval_" + artifactType,
		Title:             "确认" + artifactDisplayName(artifactType) + "及后续处理",
		Reason:            artifactDisplayName(artifactType) + "已保存新版本。已有后续内容可能受到影响，请选择保留现有后续内容，或重新生成受影响内容。",
		ProposedAction: map[string]any{
			"approved_artifact":       artifactType,
			"downstream_review":       true,
			"affected_artifact_ids":   affectedIDs,
			"affected_artifact_types": affectedTypes,
		},
		AffectedArtifacts: affectedTypes,
		Options:           []string{"keep_downstream", "regenerate_downstream", "pause"},
		Status:            "pending",
		CreatedAt:         time.Now().UTC(),
	}
	run.Status = agent.RunWaitingApproval
	run.CurrentStepID = approval.StepID
	run.ApprovalRequestID = approvalID
	run.NextAction = map[string]any{
		"capability_id":   "review_downstream_impact",
		"artifact_type":   artifactType,
		"approval_policy": "checkpoint",
	}
	r.runs[runID] = run
	r.approvals[approvalID] = approval
	r.events[runID] = append(r.events[runID], r.event(runID, approval.StepID, agent.EventApprovalRequested, "Downstream regeneration decision required", nil, nil, map[string]any{
		"approval_request_id":   approvalID,
		"artifact_type":         artifactType,
		"affected_artifact_ids": affectedIDs,
	}))
}

func (r *Runtime) confirmArtifactType(runID string, artifactType string) {
	if artifactType == "" {
		return
	}
	artifacts := r.artifacts[runID]
	updated := false
	for index := len(artifacts) - 1; index >= 0; index-- {
		if artifacts[index].ArtifactType != artifactType {
			continue
		}
		if artifacts[index].Status == agent.ArtifactPendingApproval {
			artifacts[index].Status = agent.ArtifactConfirmed
			artifacts[index].UpdatedAt = time.Now().UTC()
			updated = true
		}
		if artifactType != "script_unit" {
			break
		}
	}
	if updated {
		r.artifacts[runID] = artifacts
	}
}

func (r *Runtime) CurrentApproval(runID string) (*agent.ApprovalRequest, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok || run.ApprovalRequestID == "" {
		return nil, false
	}
	approval, ok := r.approvals[run.ApprovalRequestID]
	if !ok {
		return nil, false
	}
	return &approval, true
}

func (r *Runtime) Approval(approvalID string) (*agent.ApprovalRequest, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	approval, ok := r.approvals[approvalID]
	if !ok {
		return nil, false
	}
	return &approval, true
}

func (r *Runtime) planRun(ctx context.Context, runID string, sourceArtifactID string) {
	r.mu.Lock()
	run, ok := r.runs[runID]
	if !ok {
		r.mu.Unlock()
		return
	}
	var source agent.Artifact
	for _, artifact := range r.artifacts[runID] {
		if artifact.ArtifactID == sourceArtifactID {
			source = artifact
			break
		}
	}
	worker := r.worker
	if config := authoritativeGenerationConfigLocked(run); len(config) > 0 {
		source.Payload = clonePayload(source.Payload)
		source.Payload["generation_config"] = config
	}
	r.mu.Unlock()

	firstArtifact, ok := firstArtifactForMode(run.SourceMode)
	if !ok {
		r.failRun(runID, run.CurrentStepID, "Planning failed", fmt.Errorf("unsupported source mode: %s", run.SourceMode))
		return
	}
	r.generateArtifactStep(ctx, runID, run, source, firstArtifact, "", worker)
}

func (r *Runtime) generateArtifactAsync(ctx context.Context, runID string, artifactType string, note string) {
	if artifactType == "script_unit" {
		startCursor, endCursor := r.downstreamScriptCursorRange(runID)
		r.generateScriptUnitsRangeAsync(ctx, runID, note, startCursor, endCursor)
		return
	}
	if artifactType == "scripts" {
		r.assembleScriptsAsync(runID)
		return
	}
	if artifactType == "episode_split" {
		if splitWorker, ok := r.worker.(EpisodeSplitContentWorker); ok {
			r.generateEpisodeSplitAsync(ctx, runID, note, splitWorker)
			return
		}
	}
	if artifactType == "episode_cards" {
		if cardsWorker, ok := r.worker.(EpisodeCardsContentWorker); ok {
			r.generateEpisodeCardsAsync(ctx, runID, note, cardsWorker)
			return
		}
	}

	r.mu.Lock()
	run, ok := r.runs[runID]
	worker := r.worker
	source := r.sourceArtifactLocked(runID)
	source.Payload = clonePayload(source.Payload)
	if config := authoritativeGenerationConfigLocked(run); len(config) > 0 {
		source.Payload["generation_config"] = config
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	r.generateArtifactStep(ctx, runID, run, source, artifactType, note, worker)
}

func (r *Runtime) downstreamScriptCursorRange(runID string) (int, int) {
	r.mu.Lock()
	run, ok := r.runs[runID]
	artifacts := append([]agent.Artifact(nil), r.artifacts[runID]...)
	r.mu.Unlock()
	if !ok || run.Metadata == nil {
		return 0, -1
	}
	review, _ := run.Metadata["downstream_regeneration"].(map[string]any)
	if review == nil {
		return 0, -1
	}
	desired := map[int]bool{}
	switch values := review["episode_ids"].(type) {
	case []any:
		for _, value := range values {
			if episodeID := episodeNumber(value); episodeID > 0 {
				desired[episodeID] = true
			}
		}
	case []int:
		for _, value := range values {
			if value > 0 {
				desired[value] = true
			}
		}
	}
	if len(desired) == 0 {
		return 0, -1
	}
	episodes := episodeTasksFromArtifacts(artifacts)
	startCursor := -1
	endCursor := -1
	for cursor, episode := range episodes {
		if !desired[episodeNumber(episode.EpisodeID)] {
			continue
		}
		if startCursor < 0 {
			startCursor = cursor
		}
		endCursor = cursor
	}
	if startCursor < 0 {
		return 0, -1
	}
	return startCursor, endCursor
}

func (r *Runtime) patchArtifactAsync(ctx context.Context, runID string, artifactType string, instruction string) {
	r.mu.Lock()
	run, ok := r.runs[runID]
	worker := r.worker
	r.mu.Unlock()
	if !ok {
		return
	}
	patchWorker, ok := worker.(PatchContentWorker)
	if !ok {
		r.generateArtifactAsync(ctx, runID, artifactType, instruction)
		return
	}

	revisionContext := parseRevisionContextPack(instruction)
	target, ok := r.targetArtifactForRevision(runID, artifactType, revisionContext)
	if !ok {
		r.failRun(runID, stepIDForArtifact(artifactType), "Target artifact for revision was not found", fmt.Errorf("target artifact for revision was not found"))
		return
	}
	if revisionContext.TargetArtifact == nil || len(revisionContext.TargetArtifact.Payload) == 0 {
		revisionContext.TargetArtifact = &struct {
			Payload map[string]any `json:"payload,omitempty"`
		}{Payload: target.Payload}
	}

	r.startArtifactStep(runID, artifactType)
	stepID := stepIDForArtifact(artifactType)
	taskID := "task_" + strings.TrimPrefix(stepID, "step_") + "_patch_request"
	r.startTask(runID, stepID, artifactType, taskID, 0, 1, nil)
	existing := r.artifactSnapshot(runID)
	derivedFrom := derivedFromLatest(existing)

	plannedArtifact, err := patchWorker.PatchArtifactStep(ctx, run, target, existing, artifactType, instruction)
	if err != nil {
		r.failRun(runID, stepID, stepFailureMessageForArtifact(artifactType), err)
		return
	}
	if plannedArtifact.ArtifactType == "" {
		plannedArtifact.ArtifactType = artifactType
	}
	plannedArtifact = applyRevisionPatch(plannedArtifact, revisionContext)
	r.syncGenerationConfigForCollectionRevision(runID, artifactType, plannedArtifact.Payload, revisionContext)
	if revisionContext.TargetArtifact != nil && reflect.DeepEqual(plannedArtifact.Payload, revisionContext.TargetArtifact.Payload) {
		r.failRun(runID, stepID, "The requested change did not match or change the target content", fmt.Errorf("revision patch produced no changes; refresh the target selection and try again"))
		return
	}
	if err := validateRequestedTextLength(plannedArtifact.Payload, revisionContext, instruction); err != nil {
		r.failRun(runID, stepID, "The revised text did not satisfy the requested length", err)
		return
	}
	r.appendPlannedArtifact(runID, run, plannedArtifact, derivedFrom)
	r.completeTask(runID, taskID)
	if artifactRequiresApproval(plannedArtifact.ArtifactType) {
		r.requestArtifactApproval(runID, plannedArtifact.ArtifactType, nil)
		return
	}
	r.completeRun(runID)
}

type episodeTask struct {
	EpisodeID any
	Cursor    int
}

func (r *Runtime) generateScriptUnitsAsync(ctx context.Context, runID string, note string, startCursor int) {
	r.generateScriptUnitsRangeAsync(ctx, runID, note, startCursor, -1)
}

func (r *Runtime) generateScriptUnitsRangeAsync(ctx context.Context, runID string, note string, startCursor int, endCursor int) {
	r.mu.Lock()
	run, ok := r.runs[runID]
	worker := r.worker
	r.mu.Unlock()
	if !ok {
		return
	}
	episodes := episodeTasksFromArtifacts(r.artifactSnapshot(runID))
	if len(episodes) == 0 {
		episodes = []episodeTask{{EpisodeID: 1, Cursor: 0}}
	}
	if startCursor < 0 || startCursor >= len(episodes) {
		startCursor = 0
	}
	if endCursor < 0 || endCursor >= len(episodes) {
		endCursor = len(episodes) - 1
	}
	if endCursor < startCursor {
		endCursor = startCursor
	}
	revisionContext := parseRevisionContextPack(note)

	r.startArtifactStep(runID, "script_unit")
	for cursor := startCursor; cursor <= endCursor; cursor++ {
		task := episodes[cursor]
		taskID := fmt.Sprintf("task_generate_script_episode_%v", task.EpisodeID)
		r.startTask(runID, stepIDForArtifact("script_unit"), "script_unit", taskID, cursor, len(episodes), task.EpisodeID)

		existing := r.artifactSnapshot(runID)
		derivedFrom := derivedFromLatest(existing)
		taskNote := scriptUnitTaskNote(note, task.EpisodeID, cursor, len(episodes))
		plannedArtifact, err := worker.WriteScriptStep(ctx, run, existing, "script_unit", taskNote)
		if err != nil {
			r.failRun(runID, stepIDForArtifact("script_unit"), fmt.Sprintf("Episode %v script generation failed", task.EpisodeID), err)
			return
		}
		if plannedArtifact.ArtifactType == "" {
			plannedArtifact.ArtifactType = "script_unit"
		}
		if plannedArtifact.Payload == nil {
			plannedArtifact.Payload = map[string]any{}
		}
		plannedArtifact.Payload["episode_id"] = task.EpisodeID
		plannedArtifact.Payload["task_id"] = taskID
		plannedArtifact = applyRevisionPatch(plannedArtifact, revisionContext)
		if lengthErr := validateGeneratedScriptLength(run, plannedArtifact.Payload); lengthErr != nil {
			minimum, maximum := generatedScriptLengthBounds(run)
			requestedMaximum := maximum * 95 / 100
			if requestedMaximum < minimum {
				requestedMaximum = maximum
			}
			for attempt := 1; attempt <= 2 && lengthErr != nil; attempt++ {
				actual := len([]rune(strings.TrimSpace(fmt.Sprint(plannedArtifact.Payload["script_text"]))))
				requiredReduction := actual - requestedMaximum
				if requiredReduction < 0 {
					requiredReduction = 0
				}
				correctionNote := fmt.Sprintf("%s\n\nLENGTH CORRECTION ATTEMPT %d: The current payload.script_text is %d Chinese characters. Rewrite the same episode only and return %d-%d Chinese characters. Remove at least %d characters if it is too long, or add enough concrete action/dialogue if it is too short. Count the complete script_text including headings before returning JSON. Preserve the confirmed episode card and script format.", taskNote, attempt, actual, minimum, requestedMaximum, requiredReduction)
				plannedArtifact, err = worker.WriteScriptStep(ctx, run, existing, "script_unit", correctionNote)
				if err != nil {
					r.failRun(runID, stepIDForArtifact("script_unit"), fmt.Sprintf("Episode %v script length correction failed", task.EpisodeID), err)
					return
				}
				if plannedArtifact.ArtifactType == "" {
					plannedArtifact.ArtifactType = "script_unit"
				}
				if plannedArtifact.Payload == nil {
					plannedArtifact.Payload = map[string]any{}
				}
				plannedArtifact.Payload["episode_id"] = task.EpisodeID
				plannedArtifact.Payload["task_id"] = taskID
				plannedArtifact = applyRevisionPatch(plannedArtifact, revisionContext)
				lengthErr = validateCorrectedScriptLength(run, plannedArtifact.Payload)
			}
			if lengthErr != nil {
				r.failRun(runID, stepIDForArtifact("script_unit"), fmt.Sprintf("Episode %v script length is outside the configured range", task.EpisodeID), lengthErr)
				return
			}
		}
		r.appendPlannedArtifact(runID, run, plannedArtifact, derivedFrom)
		r.completeTask(runID, taskID)
	}

	r.requestArtifactApproval(runID, "script_unit", nil)
}

func generatedScriptLengthBounds(run agent.Run) (int, int) {
	if len(run.Metadata) == 0 {
		return 0, 0
	}
	config, _ := run.Metadata["generation_config"].(map[string]any)
	target := intValueOrZero(config["target_script_chars"])
	if target <= 0 {
		return 0, 0
	}
	minimum := target * 60 / 100
	maximum := target * 150 / 100
	if minimum < 1 {
		minimum = 1
	}
	if maximum < minimum {
		maximum = minimum
	}
	return minimum, maximum
}

func validateGeneratedScriptLength(run agent.Run, payload map[string]any) error {
	minimum, maximum := generatedScriptLengthBounds(run)
	if minimum == 0 || maximum == 0 {
		return nil
	}
	text := strings.TrimSpace(fmt.Sprint(payload["script_text"]))
	actual := len([]rune(text))
	if actual < minimum || actual > maximum {
		return fmt.Errorf("script_text length %d is outside configured range %d-%d", actual, minimum, maximum)
	}
	return nil
}

func validateCorrectedScriptLength(run agent.Run, payload map[string]any) error {
	minimum, maximum := generatedScriptLengthBounds(run)
	if minimum == 0 || maximum == 0 {
		return nil
	}
	// The model is prompted against the strict range first. A corrected draft gets
	// a small operational margin so a few formatting characters do not fail a run.
	config, _ := run.Metadata["generation_config"].(map[string]any)
	target := intValueOrZero(config["target_script_chars"])
	minimum = target * 55 / 100
	maximum = target * 165 / 100
	actual := len([]rune(strings.TrimSpace(fmt.Sprint(payload["script_text"]))))
	if actual < minimum || actual > maximum {
		return fmt.Errorf("corrected script_text length %d is outside acceptance range %d-%d", actual, minimum, maximum)
	}
	return nil
}

func (r *Runtime) assembleScriptsAsync(runID string) {
	r.mu.Lock()
	run, ok := r.runs[runID]
	r.mu.Unlock()
	if !ok {
		return
	}
	r.startArtifactStep(runID, "scripts")
	taskID := "task_assemble_scripts"
	r.startTask(runID, stepIDForArtifact("scripts"), "scripts", taskID, 0, 1, nil)
	existing := r.artifactSnapshot(runID)
	payload := aggregateScriptsPayload(run.SourceMode, existing)
	planned := PlannedArtifact{
		ArtifactType: "scripts",
		Status:       agent.ArtifactConfirmed,
		Payload:      payload,
	}
	r.appendPlannedArtifact(runID, run, planned, scriptUnitArtifactIDs(existing))
	r.completeTask(runID, taskID)
	r.completeRun(runID)
}

func (r *Runtime) generateArtifactStep(ctx context.Context, runID string, run agent.Run, source agent.Artifact, artifactType string, note string, worker ContentWorker) {
	r.startArtifactStep(runID, artifactType)
	stepID := stepIDForArtifact(artifactType)
	taskID := "task_" + strings.TrimPrefix(stepID, "step_") + "_model_request"
	r.startTask(runID, stepID, artifactType, taskID, 0, 1, nil)
	existing := r.artifactSnapshot(runID)
	derivedFrom := derivedFromLatest(existing)

	var plannedArtifact PlannedArtifact
	var approval *agent.ApprovalRequest
	var err error
	if isScriptArtifact(artifactType) {
		plannedArtifact, err = worker.WriteScriptStep(ctx, run, existing, artifactType, note)
	} else {
		if strings.TrimSpace(note) != "" {
			source.Payload = clonePayload(source.Payload)
			source.Payload["user_revision_note"] = note
		}
		plannedArtifact, approval, err = worker.PlanStep(ctx, run, source, existing, artifactType)
	}
	if err != nil {
		r.failRun(runID, stepID, stepFailureMessageForArtifact(artifactType), err)
		return
	}
	if plannedArtifact.ArtifactType == "" {
		plannedArtifact.ArtifactType = artifactType
	}
	plannedArtifact = applyRevisionPatch(plannedArtifact, parseRevisionContextPack(note))
	r.appendPlannedArtifact(runID, run, plannedArtifact, derivedFrom)
	r.completeTask(runID, taskID)
	if artifactRequiresApproval(plannedArtifact.ArtifactType) {
		r.requestArtifactApproval(runID, plannedArtifact.ArtifactType, approval)
		return
	}
	if nextArtifactType, ok := nextArtifactAfter(run.SourceMode, plannedArtifact.ArtifactType); ok {
		r.generateArtifactAsync(ctx, runID, nextArtifactType, note)
		return
	}
	r.completeRun(runID)
}

func (r *Runtime) artifactSnapshot(runID string) []agent.Artifact {
	r.mu.Lock()
	defer r.mu.Unlock()
	artifacts := r.artifacts[runID]
	out := make([]agent.Artifact, len(artifacts))
	copy(out, artifacts)
	config := authoritativeGenerationConfigLocked(r.runs[runID])
	if len(config) > 0 {
		for index := range out {
			out[index].Payload = clonePayload(out[index].Payload)
			out[index].Payload["generation_config_ref"] = configReference(runID, generationConfigVersion(r.runs[runID]))
			if out[index].ArtifactType != "scripts" {
				out[index].Payload["generation_config"] = clonePayload(config)
			}
		}
	}
	return out
}

func generationConfigVersion(run agent.Run) int {
	if run.Metadata != nil {
		switch value := run.Metadata["generation_config_version"].(type) {
		case int:
			if value > 0 {
				return value
			}
		case float64:
			if value > 0 {
				return int(value)
			}
		}
	}
	return 1
}

func configReference(runID string, version int) map[string]any {
	return map[string]any{"scope": "run", "run_id": runID, "version": version}
}

func authoritativeGenerationConfigLocked(run agent.Run) map[string]any {
	if len(run.Metadata) == 0 {
		return nil
	}
	if config, ok := run.Metadata["generation_config"].(map[string]any); ok {
		return clonePayload(config)
	}
	return nil
}

func (r *Runtime) targetArtifactForRevision(runID string, artifactType string, pack revisionContextPack) (agent.Artifact, bool) {
	artifacts := r.artifactSnapshot(runID)
	targetID := strings.TrimSpace(pack.Target.ArtifactID)
	if targetID != "" {
		for index := len(artifacts) - 1; index >= 0; index-- {
			if artifacts[index].ArtifactID == targetID && artifacts[index].ArtifactType == artifactType && artifacts[index].Status != agent.ArtifactSuperseded && artifacts[index].Status != agent.ArtifactInvalidated {
				return artifacts[index], true
			}
		}
	}
	for index := len(artifacts) - 1; index >= 0; index-- {
		artifact := artifacts[index]
		if artifact.ArtifactType != artifactType {
			continue
		}
		if artifact.Status == agent.ArtifactSuperseded || artifact.Status == agent.ArtifactInvalidated {
			continue
		}
		if artifactType == "script_unit" && episodeNumber(pack.Target.EpisodeID) > 0 && !sameEpisodeID(artifact.Payload, map[string]any{"episode_id": pack.Target.EpisodeID}) {
			continue
		}
		return artifact, true
	}
	return agent.Artifact{}, false
}

func (r *Runtime) sourceArtifactLocked(runID string) agent.Artifact {
	for _, artifact := range r.artifacts[runID] {
		if artifact.ArtifactType == "source_input" {
			return artifact
		}
	}
	return agent.Artifact{}
}

func (r *Runtime) startArtifactStep(runID string, artifactType string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	run, ok := r.runs[runID]
	if !ok || run.Status != agent.RunRunning {
		return
	}
	stepID := stepIDForArtifact(artifactType)
	run.CurrentStepID = stepID
	run.NextAction = map[string]any{"capability_id": strings.TrimPrefix(stepID, "step_")}
	r.runs[runID] = run
	r.events[runID] = append(r.events[runID], r.event(runID, stepID, agent.EventStepStarted, stepStartMessageForArtifact(artifactType), nil, nil, map[string]any{
		"artifact_type": artifactType,
	}))
	r.saveLocked()
}

func (r *Runtime) startTask(runID string, stepID string, artifactType string, taskID string, cursor int, total int, episodeID any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	run, ok := r.runs[runID]
	if !ok || run.Status != agent.RunRunning {
		return
	}
	task := map[string]any{
		"task_id":       taskID,
		"step_id":       stepID,
		"artifact_type": artifactType,
		"task_cursor":   cursor,
		"task_index":    cursor + 1,
		"task_total":    total,
		"status":        "running",
	}
	if episodeID != nil {
		task["episode_id"] = episodeID
	}
	run.Metadata = cloneMetadata(run.Metadata)
	run.Metadata["active_task"] = task
	run.NextAction = map[string]any{
		"capability_id": strings.TrimPrefix(stepID, "step_"),
		"artifact_type": artifactType,
		"task_id":       taskID,
		"task_cursor":   cursor,
		"task_total":    total,
	}
	if episodeID != nil {
		run.NextAction["episode_id"] = episodeID
	}
	r.runs[runID] = run
	r.events[runID] = append(r.events[runID], r.event(runID, stepID, agent.EventStepStarted, fmt.Sprintf("Running task %s", taskID), nil, nil, task))
	r.saveLocked()
}

func (r *Runtime) completeTask(runID string, taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	run, ok := r.runs[runID]
	if !ok {
		return
	}
	task := activeTaskFromRun(run)
	if task == nil || taskString(task, "task_id") != taskID {
		return
	}
	task["status"] = "completed"
	run.Metadata = cloneMetadata(run.Metadata)
	run.Metadata["last_completed_task"] = task
	delete(run.Metadata, "active_task")
	r.runs[runID] = run
	r.events[runID] = append(r.events[runID], r.event(runID, taskString(task, "step_id"), agent.EventStepCompleted, fmt.Sprintf("Task %s completed", taskID), nil, nil, task))
	r.saveLocked()
}

func (r *Runtime) appendPlannedArtifact(runID string, run agent.Run, plannedArtifact PlannedArtifact, derivedFrom []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	currentRun, ok := r.runs[runID]
	if !ok || currentRun.Status != agent.RunRunning {
		return
	}

	status := plannedArtifact.Status
	if status == "" {
		status = agent.ArtifactConfirmed
	}
	if artifactRequiresApproval(plannedArtifact.ArtifactType) {
		status = agent.ArtifactPendingApproval
	}
	plannedPayload := clonePayload(plannedArtifact.Payload)
	delete(plannedPayload, "generation_config")
	plannedPayload["generation_config_ref"] = configReference(runID, generationConfigVersion(currentRun))
	applyDeterministicSourceMetrics(plannedArtifact.ArtifactType, plannedPayload, r.artifacts[runID])
	artifact := r.artifact(run, plannedArtifact.ArtifactType, status, derivedFrom, plannedPayload)
	artifact.Version = r.nextArtifactVersionLocked(runID, artifact)
	if revision := activeRevisionFromRun(currentRun); revision != nil {
		ids, types, episodeIDs := existingAffectedDownstream(r.artifacts[runID], artifact.SourceMode, artifact.ArtifactType, revision)
		currentRun.Metadata = cloneMetadata(currentRun.Metadata)
		if len(ids) > 0 {
			currentRun.Metadata["pending_downstream_review"] = map[string]any{
				"source_artifact_type": artifact.ArtifactType,
				"artifact_ids":         ids,
				"artifact_types":       types,
				"episode_ids":          episodeIDs,
				"revision":             clonePayload(revision),
			}
		} else {
			delete(currentRun.Metadata, "pending_downstream_review")
		}
	}
	r.supersedeComparableArtifactsLocked(runID, artifact, currentRun)
	eventType := agent.EventArtifactCreated
	if plannedArtifact.ArtifactType == "script_unit" {
		eventType = agent.EventScriptBatchInserted
	}
	event := r.event(runID, stepIDForArtifact(plannedArtifact.ArtifactType), eventType, eventMessageForArtifact(plannedArtifact.ArtifactType), []string{artifact.ArtifactID}, focus(artifact), nil)

	currentRun.CurrentStepID = event.StepID
	currentRun.CreatedArtifacts = append(currentRun.CreatedArtifacts, artifact.ArtifactID)
	currentRun.NextAction = map[string]any{"capability_id": strings.TrimPrefix(event.StepID, "step_")}
	currentRun.Metadata = cloneMetadata(currentRun.Metadata)
	delete(currentRun.Metadata, "active_revision")
	delete(currentRun.Metadata, "active_revision_instruction")
	r.runs[runID] = currentRun
	r.artifacts[runID] = append(r.artifacts[runID], artifact)
	r.events[runID] = append(r.events[runID], event)
	if artifact.ArtifactType == "script_unit" && artifact.Status == agent.ArtifactConfirmed {
		r.refreshScriptsAggregateLocked(runID)
	}
	r.saveLocked()
}

func (r *Runtime) refreshScriptsAggregateLocked(runID string) (agent.Artifact, bool) {
	run, ok := r.runs[runID]
	if !ok {
		return agent.Artifact{}, false
	}
	hasAggregate := false
	for _, artifact := range r.artifacts[runID] {
		if artifact.ArtifactType == "scripts" {
			hasAggregate = true
			break
		}
	}
	if !hasAggregate {
		return agent.Artifact{}, false
	}
	payload := aggregateScriptsPayload(run.SourceMode, r.artifacts[runID])
	aggregate := r.artifact(run, "scripts", agent.ArtifactConfirmed, scriptUnitArtifactIDs(r.artifacts[runID]), payload)
	aggregate.Version = r.nextArtifactVersionLocked(runID, aggregate)
	r.supersedeComparableArtifactsLocked(runID, aggregate, run)
	r.artifacts[runID] = append(r.artifacts[runID], aggregate)
	run.UpdatedArtifacts = append(run.UpdatedArtifacts, aggregate.ArtifactID)
	r.runs[runID] = run
	r.events[runID] = append(r.events[runID], r.event(runID, stepIDForArtifact("scripts"), agent.EventArtifactUpdated, "Script collection synchronized", []string{aggregate.ArtifactID}, focus(aggregate), map[string]any{"derived": true}))
	return aggregate, true
}

func (r *Runtime) supersedeComparableArtifactsLocked(runID string, next agent.Artifact, run agent.Run) {
	if next.ArtifactType == "" {
		return
	}
	artifacts := r.artifacts[runID]
	changed := false
	for index := range artifacts {
		current := &artifacts[index]
		if current.ArtifactType != next.ArtifactType {
			continue
		}
		if current.Status == agent.ArtifactSuperseded || current.Status == agent.ArtifactInvalidated {
			continue
		}
		if next.ArtifactType == "script_unit" && !sameEpisodeID(current.Payload, next.Payload) {
			continue
		}
		current.Status = agent.ArtifactSuperseded
		current.UpdatedAt = time.Now().UTC()
		changed = true
	}
	if changed {
		r.artifacts[runID] = artifacts
	}
}

func activeRevisionFromRun(run agent.Run) map[string]any {
	if run.Metadata == nil {
		return nil
	}
	revision, _ := run.Metadata["active_revision"].(map[string]any)
	return revision
}

func pendingDownstreamReviewFromRun(run agent.Run) map[string]any {
	if run.Metadata == nil {
		return nil
	}
	review, _ := run.Metadata["pending_downstream_review"].(map[string]any)
	return review
}

func existingAffectedDownstream(artifacts []agent.Artifact, sourceMode agent.SourceMode, artifactType string, revision map[string]any) ([]string, []string, []any) {
	if artifactType == "script_unit" {
		return nil, nil, nil
	}
	if !revisionChangesDownstream(revision) {
		return nil, nil, nil
	}
	affectedTypes := map[string]bool{}
	for _, current := range affectedArtifactsAfter(sourceMode, artifactType) {
		affectedTypes[current] = true
	}
	if len(affectedTypes) == 0 {
		return nil, nil, nil
	}

	targetEpisode := 0
	if revision != nil {
		targetEpisode = episodeNumber(revision["episode_id"])
	}
	ids := []string{}
	types := []string{}
	episodeIDs := []any{}
	seenTypes := map[string]bool{}
	seenEpisodes := map[int]bool{}
	for _, current := range artifacts {
		if !affectedTypes[current.ArtifactType] || current.Status == agent.ArtifactSuperseded || current.Status == agent.ArtifactInvalidated {
			continue
		}
		if targetEpisode > 0 && current.ArtifactType == "script_unit" {
			currentEpisode := episodeNumber(current.Payload["episode_id"])
			if artifactType == "episode_cards" && currentEpisode != targetEpisode {
				continue
			}
			if artifactType == "episode_split" && currentEpisode < targetEpisode {
				continue
			}
		}
		ids = append(ids, current.ArtifactID)
		if !seenTypes[current.ArtifactType] {
			types = append(types, current.ArtifactType)
			seenTypes[current.ArtifactType] = true
		}
		if current.ArtifactType == "script_unit" {
			episodeID := episodeNumber(current.Payload["episode_id"])
			if episodeID > 0 && !seenEpisodes[episodeID] {
				episodeIDs = append(episodeIDs, episodeID)
				seenEpisodes[episodeID] = true
			}
		}
	}
	return ids, types, episodeIDs
}

func invalidateDownstreamArtifactsScoped(artifacts []agent.Artifact, sourceMode agent.SourceMode, artifactType string, revision map[string]any) []agent.Artifact {
	if !revisionChangesDownstream(revision) {
		return artifacts
	}
	targetEpisode := 0
	if revision != nil {
		targetEpisode = episodeNumber(revision["episode_id"])
	}
	affected := map[string]bool{}
	for _, current := range affectedArtifactsAfter(sourceMode, artifactType) {
		affected[current] = true
	}
	if artifactType == "script_unit" {
		affected["scripts"] = true
	}
	for index := range artifacts {
		current := &artifacts[index]
		if !affected[current.ArtifactType] || current.Status == agent.ArtifactSuperseded || current.Status == agent.ArtifactInvalidated {
			continue
		}
		if targetEpisode > 0 && current.ArtifactType == "script_unit" {
			currentEpisode := episodeNumber(current.Payload["episode_id"])
			if artifactType == "episode_cards" && currentEpisode != targetEpisode {
				continue
			}
			if artifactType == "episode_split" && currentEpisode < targetEpisode {
				continue
			}
		}
		current.Status = agent.ArtifactInvalidated
		current.UpdatedAt = time.Now().UTC()
	}
	return artifacts
}

func revisionChangesDownstream(revision map[string]any) bool {
	if len(revision) == 0 {
		return true
	}
	fieldPath := strings.ToLower(strings.TrimSpace(fmt.Sprint(revision["field_path"])))
	if fieldPath == "" || fieldPath == "<nil>" {
		return true
	}
	for _, metadataField := range []string{"source_trace", "source_evidence", "risk_notes", "adaptation_risks", "self_check", "generation_config_ref"} {
		if fieldPath == metadataField || strings.HasPrefix(fieldPath, metadataField+".") || strings.HasPrefix(fieldPath, metadataField+"[") {
			return false
		}
	}
	return true
}

func applyDeterministicSourceMetrics(artifactType string, payload map[string]any, artifacts []agent.Artifact) {
	if artifactType != "episode_split" {
		return
	}
	var sourceText string
	for index := len(artifacts) - 1; index >= 0; index-- {
		artifact := artifacts[index]
		if artifact.ArtifactType == "source_input" && artifact.Status != agent.ArtifactSuperseded && artifact.Status != agent.ArtifactInvalidated {
			sourceText = strings.TrimSpace(fmt.Sprint(artifact.Payload["text"]))
			break
		}
	}
	if sourceText == "" || sourceText == "<nil>" {
		return
	}
	assessment, _ := payload["source_volume_assessment"].(map[string]any)
	if assessment == nil {
		assessment = map[string]any{}
		payload["source_volume_assessment"] = assessment
	}
	assessment["source_chars"] = len([]rune(sourceText))
}

func invalidateDownstreamArtifacts(artifacts []agent.Artifact, sourceMode agent.SourceMode, artifactType string) []agent.Artifact {
	affected := map[string]bool{}
	for _, current := range affectedArtifactsAfter(sourceMode, artifactType) {
		affected[current] = true
	}
	if artifactType == "script_unit" {
		affected["scripts"] = true
	}
	if len(affected) == 0 {
		return artifacts
	}
	for index := range artifacts {
		if !affected[artifacts[index].ArtifactType] {
			continue
		}
		if artifacts[index].Status == agent.ArtifactSuperseded || artifacts[index].Status == agent.ArtifactInvalidated {
			continue
		}
		artifacts[index].Status = agent.ArtifactInvalidated
		artifacts[index].UpdatedAt = time.Now().UTC()
	}
	return artifacts
}

func (r *Runtime) requestArtifactApproval(runID string, artifactType string, approval *agent.ApprovalRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()

	run, ok := r.runs[runID]
	if !ok || run.Status != agent.RunRunning {
		return
	}
	if !artifactRequiresApproval(artifactType) {
		r.completeRunLocked(runID)
		return
	}
	if approval == nil {
		approval = &agent.ApprovalRequest{}
	}
	if previousApprovalID := strings.TrimSpace(fmt.Sprint(run.Metadata["revision_previous_approval_id"])); previousApprovalID != "" && previousApprovalID != "<nil>" {
		if previous, exists := r.approvals[previousApprovalID]; exists && previous.Status == "pending" {
			resolvedAt := time.Now().UTC()
			previous.Status = "revised"
			previous.UserResponse = "replaced by successful artifact revision"
			previous.ResolvedAt = &resolvedAt
			r.approvals[previousApprovalID] = previous
			r.events[runID] = append(r.events[runID], r.event(runID, previous.StepID, agent.EventApprovalResolved, "Approval replaced by successful artifact revision", nil, nil, map[string]any{
				"approval_request_id": previousApprovalID,
				"artifact_type":       artifactType,
				"decision":            "revised",
			}))
		}
		run.Metadata = cloneMetadata(run.Metadata)
		delete(run.Metadata, "revision_previous_approval_id")
	}
	nextArtifactType, _ := nextArtifactAfter(run.SourceMode, artifactType)
	approvalID := r.nextID("approval")
	approval.ApprovalRequestID = approvalID
	approval.RunID = runID
	approval.StepID = "step_approval_" + artifactType
	if approval.Title == "" {
		approval.Title = approvalTitleForArtifact(artifactType)
	}
	if approval.Reason == "" {
		approval.Reason = approvalReasonForArtifact(artifactType, nextArtifactType)
	}
	if approval.ProposedAction == nil {
		approval.ProposedAction = map[string]any{
			"approved_artifact":  artifactType,
			"next_artifact_type": nextArtifactType,
		}
	} else {
		approval.ProposedAction["approved_artifact"] = artifactType
		approval.ProposedAction["next_artifact_type"] = nextArtifactType
	}
	if review := pendingDownstreamReviewFromRun(run); review != nil {
		affectedTypes := stringSliceValue(review["artifact_types"])
		approval.Title = "确认" + artifactDisplayName(artifactType) + "及后续处理"
		approval.Reason = artifactDisplayName(artifactType) + "已生成新版本。已有后续内容可能受到影响，请选择保留现有后续内容，或重新生成受影响内容。"
		approval.AffectedArtifacts = affectedTypes
		approval.Options = []string{"keep_downstream", "regenerate_downstream", "revise_instruction", "pause"}
		approval.ProposedAction["downstream_review"] = true
		approval.ProposedAction["affected_artifact_ids"] = stringSliceValue(review["artifact_ids"])
		approval.ProposedAction["affected_artifact_types"] = affectedTypes
	} else {
		if len(approval.AffectedArtifacts) == 0 {
			approval.AffectedArtifacts = affectedArtifactsAfter(run.SourceMode, artifactType)
		}
		if len(approval.Options) == 0 {
			approval.Options = []string{"approve", "revise_instruction", "pause"}
		}
	}
	approval.Status = "pending"
	approval.CreatedAt = time.Now().UTC()

	run.Status = agent.RunWaitingApproval
	run.CurrentStepID = approval.StepID
	run.NextAction = map[string]any{
		"capability_id":      "approve_" + artifactType,
		"approval_policy":    "checkpoint",
		"artifact_type":      artifactType,
		"next_artifact_type": nextArtifactType,
	}
	run.ApprovalRequestID = approvalID
	r.runs[runID] = run
	r.approvals[approvalID] = *approval
	r.events[runID] = append(r.events[runID], r.event(runID, approval.StepID, agent.EventApprovalRequested, approvalRequestedMessage(artifactType), nil, nil, map[string]any{
		"approval_request_id": approvalID,
		"artifact_type":       artifactType,
		"next_artifact_type":  nextArtifactType,
	}))
	r.saveLocked()
}

func stringSliceValue(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text := strings.TrimSpace(fmt.Sprint(item))
			if text != "" && text != "<nil>" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func (r *Runtime) completeRun(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completeRunLocked(runID)
}

func (r *Runtime) completeRunLocked(runID string) {
	run, ok := r.runs[runID]
	if !ok {
		return
	}
	ended := time.Now().UTC()
	run.Status = agent.RunCompleted
	run.CurrentStepID = "step_completed"
	run.NextAction = map[string]any{}
	run.EndedAt = &ended
	r.runs[runID] = run
	r.events[runID] = append(r.events[runID], r.event(runID, "", agent.EventRunCompleted, "Run completed", run.CreatedArtifacts, nil, nil))
	r.saveLocked()
}

func (r *Runtime) failRun(runID string, stepID string, message string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	run, ok := r.runs[runID]
	if !ok || run.Status != agent.RunRunning {
		return
	}
	if previousApprovalID := strings.TrimSpace(fmt.Sprint(run.Metadata["revision_previous_approval_id"])); previousApprovalID != "" && previousApprovalID != "<nil>" {
		if approval, exists := r.approvals[previousApprovalID]; exists && approval.Status == "pending" {
			activeTask := activeTaskFromRun(run)
			activeRevision := activeRevisionFromRun(run)
			activeInstruction := strings.TrimSpace(fmt.Sprint(run.Metadata["active_revision_instruction"]))
			run.Status = agent.RunWaitingApproval
			run.CurrentStepID = approval.StepID
			run.ApprovalRequestID = previousApprovalID
			run.NextAction = map[string]any{
				"capability_id":   "approve_" + approvalArtifactType(approval, stepID),
				"approval_policy": "checkpoint",
				"artifact_type":   approvalArtifactType(approval, stepID),
			}
			run.EndedAt = nil
			run.Metadata = cloneMetadata(run.Metadata)
			if activeTask != nil {
				failedTask := clonePayload(activeTask)
				failedTask["status"] = "failed"
				run.Metadata["last_failed_task"] = failedTask
			}
			if len(activeRevision) > 0 {
				run.Metadata["last_failed_revision"] = clonePayload(activeRevision)
			}
			if activeInstruction != "" && activeInstruction != "<nil>" {
				run.Metadata["last_failed_revision_instruction"] = activeInstruction
			}
			run.Metadata["last_error"] = safeFailureMessage(err)
			delete(run.Metadata, "revision_previous_approval_id")
			delete(run.Metadata, "active_revision")
			delete(run.Metadata, "active_revision_instruction")
			delete(run.Metadata, "active_task")
			r.runs[runID] = run
			payload := failureEventPayload(err)
			payload["original_approval_restored"] = true
			if activeTask != nil {
				payload["task"] = activeTask
			}
			r.events[runID] = append(r.events[runID], r.event(runID, stepID, agent.EventStepFailed, message, nil, nil, payload))
			r.saveLocked()
			return
		}
	}
	ended := time.Now().UTC()
	run.Status = agent.RunFailed
	run.CurrentStepID = stepID
	activeTask := activeTaskFromRun(run)
	run.NextAction = map[string]any{
		"capability_id": "retry",
		"step_id":       stepID,
	}
	if activeTask != nil {
		run.NextAction["task_id"] = taskString(activeTask, "task_id")
		run.NextAction["task_cursor"] = taskInt(activeTask, "task_cursor")
		run.NextAction["task_total"] = taskInt(activeTask, "task_total")
		if episodeID, ok := activeTask["episode_id"]; ok {
			run.NextAction["episode_id"] = episodeID
		}
	}
	run.EndedAt = &ended
	r.runs[runID] = run
	payload := failureEventPayload(err)
	if activeTask != nil {
		payload["task"] = activeTask
	}
	r.events[runID] = append(r.events[runID], r.event(runID, stepID, agent.EventStepFailed, message, nil, nil, payload))
	r.saveLocked()
}

func failureEventPayload(err error) map[string]any {
	payload := map[string]any{"error": safeFailureMessage(err)}
	var incomplete *episodeCardsIncompleteError
	if errors.As(err, &incomplete) {
		payload["error_code"] = "EPISODE_CARDS_INCOMPLETE"
		payload["user_message"] = fmt.Sprintf("模型返回的第 %d-%d 集分集卡不完整，本批次未保存。已保留此前完成的批次，可以从当前批次重试。", incomplete.BatchStart, incomplete.BatchEnd)
		return payload
	}
	if isModelTimeoutError(err) {
		payload["error_code"] = "MODEL_TIMEOUT"
		payload["user_message"] = "模型服务响应超时，本次未生成有效内容。已保留当前进度，你可以稍后从失败位置重试。"
	}
	return payload
}

func safeFailureMessage(err error) string {
	if err == nil {
		return "任务执行失败"
	}
	if isModelTimeoutError(err) {
		return "模型服务响应超时"
	}
	var incomplete *episodeCardsIncompleteError
	if errors.As(err, &incomplete) {
		return "模型返回的分集卡批次不完整"
	}
	return "任务执行失败"
}

func isModelTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"context deadline exceeded", "client.timeout exceeded", "timeout awaiting response headers", "status=504", "status_code=504", "status_code\":504"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (r *Runtime) pauseRun(run agent.Run, req agent.ContinueRunRequest) agent.ContinueRunResponse {
	previousStatus := run.Status
	run.Status = agent.RunPaused
	run.Metadata = cloneMetadata(run.Metadata)
	run.Metadata["paused_by_user"] = true
	run.Metadata["paused_from_status"] = string(previousStatus)
	run.NextAction = map[string]any{"capability_id": "wait_for_user"}
	r.runs[run.RunID] = run
	event := r.event(run.RunID, run.CurrentStepID, agent.EventApprovalResolved, "Run paused for revised instruction", nil, nil, map[string]any{
		"decision": req.Decision,
		"note":     req.Note,
	})
	r.events[run.RunID] = append(r.events[run.RunID], event)
	r.saveLocked()
	return agent.ContinueRunResponse{
		Run:       run,
		Events:    []agent.RunEvent{event},
		Artifacts: r.artifacts[run.RunID],
	}
}

func (r *Runtime) artifact(run agent.Run, artifactType string, status agent.ArtifactStatus, derivedFrom []string, payload map[string]any) agent.Artifact {
	now := time.Now().UTC()
	return agent.Artifact{
		ArtifactID:   r.nextID("artifact"),
		ArtifactType: artifactType,
		ProjectID:    run.ProjectID,
		RunID:        run.RunID,
		Version:      1,
		Status:       status,
		SourceMode:   run.SourceMode,
		DerivedFrom:  derivedFrom,
		Payload:      payload,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

func (r *Runtime) nextArtifactVersionLocked(runID string, next agent.Artifact) int {
	version := 1
	for _, current := range r.artifacts[runID] {
		if current.ArtifactType != next.ArtifactType {
			continue
		}
		if next.ArtifactType == "script_unit" && !sameEpisodeID(current.Payload, next.Payload) {
			continue
		}
		if current.Version >= version {
			version = current.Version + 1
		}
	}
	return version
}

func (r *Runtime) event(runID string, stepID string, eventType agent.EventType, message string, artifactRefs []string, focusArtifact map[string]any, payload map[string]any) agent.RunEvent {
	return agent.RunEvent{
		EventID:       r.nextID("event"),
		RunID:         runID,
		StepID:        stepID,
		Type:          eventType,
		Message:       message,
		ArtifactRefs:  artifactRefs,
		FocusArtifact: focusArtifact,
		Payload:       payload,
		CreatedAt:     time.Now().UTC(),
	}
}

func (r *Runtime) nextID(prefix string) string {
	r.counter++
	return fmt.Sprintf("%s_%06d", prefix, r.counter)
}

func detectSourceMode(text string) agent.SourceMode {
	lower := strings.ToLower(text)
	novelHints := []string{"chapter", "novel", "source text", "小说", "原文", "章节"}
	nonNovelHints := []string{"idea", "synopsis", "material", "灵感", "梗概", "素材", "想法", "人设", "短剧"}
	for _, hint := range novelHints {
		if strings.Contains(lower, strings.ToLower(hint)) {
			return agent.SourceModeNovel
		}
	}
	for _, hint := range nonNovelHints {
		if strings.Contains(lower, strings.ToLower(hint)) {
			return agent.SourceModeNonNovel
		}
	}
	if len([]rune(text)) > 3000 {
		return agent.SourceModeNovel
	}
	return agent.SourceModeNonNovel
}
func intentForSource(sourceMode agent.SourceMode) string {
	if sourceMode == agent.SourceModeNovel {
		return "generate_novel"
	}
	return "generate_non_novel"
}

func focus(artifact agent.Artifact) map[string]any {
	return map[string]any{
		"artifact_id":   artifact.ArtifactID,
		"artifact_type": artifact.ArtifactType,
		"version":       artifact.Version,
	}
}

func artifactIDs(artifacts []agent.Artifact) []string {
	ids := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		ids = append(ids, artifact.ArtifactID)
	}
	return ids
}

func derivedFromLatest(artifacts []agent.Artifact) []string {
	if len(artifacts) == 0 {
		return nil
	}
	return []string{artifacts[len(artifacts)-1].ArtifactID}
}

func cloneMetadata(metadata map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

func clonePayload(payload map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range payload {
		out[key] = value
	}
	return out
}

func activeTaskFromRun(run agent.Run) map[string]any {
	if run.Metadata == nil {
		return nil
	}
	if task, ok := run.Metadata["active_task"].(map[string]any); ok {
		return task
	}
	if task, ok := run.Metadata["active_task"].(map[string]interface{}); ok {
		out := map[string]any{}
		for key, value := range task {
			out[key] = value
		}
		return out
	}
	return nil
}

func lastFailedTaskFromRun(run agent.Run) map[string]any {
	if run.Metadata == nil {
		return nil
	}
	if task, ok := run.Metadata["last_failed_task"].(map[string]any); ok {
		return task
	}
	if task, ok := run.Metadata["last_failed_task"].(map[string]interface{}); ok {
		out := map[string]any{}
		for key, value := range task {
			out[key] = value
		}
		return out
	}
	return nil
}

func taskCursorFromRun(run agent.Run) int {
	task := activeTaskFromRun(run)
	if task == nil {
		task = lastFailedTaskFromRun(run)
	}
	if task == nil {
		return 0
	}
	return taskInt(task, "task_cursor")
}

func failedStepIDFromRun(run agent.Run) string {
	if task := lastFailedTaskFromRun(run); task != nil {
		if stepID := taskString(task, "step_id"); stepID != "" {
			return stepID
		}
	}
	if value, ok := run.NextAction["step_id"].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return run.CurrentStepID
}

func taskString(task map[string]any, key string) string {
	if value, ok := task[key].(string); ok {
		return value
	}
	return ""
}

func taskInt(task map[string]any, key string) int {
	switch value := task[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case float32:
		return int(value)
	default:
		return 0
	}
}

func episodeTasksFromArtifacts(artifacts []agent.Artifact) []episodeTask {
	var latestCards map[string]any
	latestVersion := 0
	for _, artifact := range artifacts {
		if artifact.ArtifactType == "episode_cards" && artifact.Status != agent.ArtifactSuperseded && artifact.Status != agent.ArtifactInvalidated && artifact.Version >= latestVersion {
			latestCards = artifact.Payload
			latestVersion = artifact.Version
		}
	}
	if latestCards == nil {
		return nil
	}
	rawEpisodes, ok := latestCards["episodes"].([]any)
	if !ok {
		if typed, ok := latestCards["episodes"].([]map[string]any); ok {
			rawEpisodes = make([]any, 0, len(typed))
			for _, item := range typed {
				rawEpisodes = append(rawEpisodes, item)
			}
		}
	}
	tasks := make([]episodeTask, 0, len(rawEpisodes))
	for index, raw := range rawEpisodes {
		episodeID := any(index + 1)
		if item, ok := raw.(map[string]any); ok {
			if value, exists := item["episode_id"]; exists {
				episodeID = value
			}
		}
		tasks = append(tasks, episodeTask{EpisodeID: episodeID, Cursor: index})
	}
	return tasks
}

func episodeCursorFromInstruction(instruction string, episodes []episodeTask) int {
	if len(episodes) == 0 {
		return 0
	}
	target := episodeNumberFromRevisionContext(instruction)
	if target <= 0 {
		target = episodeNumberFromText(instruction)
	}
	if target <= 0 {
		return 0
	}
	for index, task := range episodes {
		if episodeNumber(task.EpisodeID) == target {
			return index
		}
	}
	if target <= len(episodes) {
		return target - 1
	}
	return 0
}

func episodeNumberFromRevisionContext(instruction string) int {
	pack := parseRevisionContextPack(instruction)
	return episodeNumber(pack.Target.EpisodeID)
}

type revisionContextPack struct {
	RevisionIntent string `json:"revision_intent,omitempty"`
	Target         struct {
		ArtifactID   string   `json:"artifact_id,omitempty"`
		ArtifactType string   `json:"artifact_type,omitempty"`
		FieldPath    string   `json:"field_path,omitempty"`
		EpisodeID    any      `json:"episode_id,omitempty"`
		SceneID      string   `json:"scene_id,omitempty"`
		NodeID       string   `json:"node_id,omitempty"`
		StartSceneID string   `json:"start_scene_id,omitempty"`
		EndSceneID   string   `json:"end_scene_id,omitempty"`
		StartLineID  string   `json:"start_line_id,omitempty"`
		EndLineID    string   `json:"end_line_id,omitempty"`
		LineIDs      []string `json:"line_ids,omitempty"`
		Scope        string   `json:"scope,omitempty"`
	} `json:"target,omitempty"`
	FocusedContext struct {
		FieldPath      string   `json:"field_path,omitempty"`
		EpisodeID      string   `json:"episode_id,omitempty"`
		SceneID        string   `json:"scene_id,omitempty"`
		NodeID         string   `json:"node_id,omitempty"`
		StartSceneID   string   `json:"start_scene_id,omitempty"`
		EndSceneID     string   `json:"end_scene_id,omitempty"`
		StartLineID    string   `json:"start_line_id,omitempty"`
		EndLineID      string   `json:"end_line_id,omitempty"`
		LineIDs        []string `json:"line_ids,omitempty"`
		SelectionStart int      `json:"selection_start,omitempty"`
		SelectionEnd   int      `json:"selection_end,omitempty"`
		SelectedText   string   `json:"selected_text,omitempty"`
	} `json:"focused_context,omitempty"`
	TargetArtifact *struct {
		Payload map[string]any `json:"payload,omitempty"`
	} `json:"target_artifact,omitempty"`
}

func parseRevisionContextPack(instruction string) revisionContextPack {
	const marker = "REVISION_CONTEXT_PACK_JSON:"
	var pack revisionContextPack
	index := strings.Index(instruction, marker)
	if index < 0 {
		return pack
	}
	raw := strings.TrimSpace(instruction[index+len(marker):])
	if raw == "" {
		return pack
	}
	_ = json.Unmarshal([]byte(raw), &pack)
	return pack
}

func applyRevisionPatch(planned PlannedArtifact, pack revisionContextPack) PlannedArtifact {
	if planned.Payload == nil || pack.TargetArtifact == nil || len(pack.TargetArtifact.Payload) == 0 {
		return planned
	}
	intent := strings.TrimSpace(pack.RevisionIntent)
	if intent == "" {
		return planned
	}
	base := deepClonePayload(pack.TargetArtifact.Payload)
	fieldPath := strings.TrimSpace(pack.Target.FieldPath)
	if fieldPath == "" {
		fieldPath = strings.TrimSpace(pack.FocusedContext.FieldPath)
	}

	switch intent {
	case "patch_artifact_field":
		patchPayloadField(base, planned.Payload, fieldPath)
		planned.Payload = base
	case "patch_artifact_entity":
		if !applyArtifactEntityPatch(base, planned.Payload, fieldPath) {
			patchPayloadEntity(base, planned.Payload, fieldPath)
			if fieldPath == "" {
				patchEpisodeInPayload(base, planned.Payload, pack.Target.EpisodeID)
				patchSceneInPayload(base, planned.Payload, firstNonEmpty(pack.Target.SceneID, pack.FocusedContext.SceneID))
			}
		}
		planned.Payload = base
	case "patch_artifact_collection":
		if applyArtifactCollectionPatch(base, planned.Payload, fieldPath) {
			planned.Payload = base
		}
	case "patch_artifact_section":
		patchPayloadField(base, planned.Payload, fieldPath)
		planned.Payload = base
	case "patch_script_span":
		applyScriptSpanPatch(base, planned.Payload, pack)
		planned.Payload = base
	case "regenerate_script_scene":
		applyScriptScenePatch(base, planned.Payload, firstNonEmpty(pack.Target.SceneID, pack.FocusedContext.SceneID))
		planned.Payload = base
	case "regenerate_artifact":
		if patchEpisodeInPayload(base, planned.Payload, pack.Target.EpisodeID) {
			planned.Payload = base
		}
	}
	return planned
}

var requestedTextLengthPattern = regexp.MustCompile(`(?i)(\d{1,5})\s*(?:个)?(?:字|字符|chars?)`)

func validateRequestedTextLength(payload map[string]any, pack revisionContextPack, instruction string) error {
	matches := requestedTextLengthPattern.FindStringSubmatch(instruction)
	if len(matches) < 2 {
		return nil
	}
	target, err := strconv.Atoi(matches[1])
	if err != nil || target <= 0 {
		return nil
	}
	fieldPath := firstNonEmpty(pack.Target.FieldPath, pack.FocusedContext.FieldPath)
	if fieldPath == "" {
		return nil
	}
	value, ok := payloadValueAt(payload, parseFieldPath(resolveNamedFieldSelectors(fieldPath, payload)))
	if !ok {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return nil
	}
	actual := len([]rune(strings.TrimSpace(text)))
	tolerance := target / 10
	if tolerance < 10 {
		tolerance = 10
	}
	if actual > target+tolerance {
		return fmt.Errorf("requested about %d characters, but the revised field contains %d", target, actual)
	}
	return nil
}

func applyArtifactEntityPatch(base map[string]any, candidate map[string]any, targetPath string) bool {
	patch, ok := candidate["__artifact_entity_patch"].(map[string]any)
	if !ok {
		return false
	}
	fieldPath := strings.TrimSpace(fmt.Sprint(patch["field_path"]))
	if fieldPath == "" || (strings.TrimSpace(targetPath) != "" && fieldPath != strings.TrimSpace(targetPath)) {
		return false
	}
	fieldPath = resolveNamedFieldSelectors(fieldPath, base)
	tokens := parseFieldPath(fieldPath)
	if len(tokens) == 0 {
		return false
	}
	operation := strings.TrimSpace(fmt.Sprint(patch["operation"]))
	switch operation {
	case "append_entity":
		collectionTokens := tokens
		if _, ok := tokens[len(tokens)-1].(int); ok {
			collectionTokens = tokens[:len(tokens)-1]
		}
		value, ok := payloadValueAt(base, collectionTokens)
		if !ok {
			return false
		}
		items, ok := value.([]any)
		if !ok {
			return false
		}
		items = append(items, patch["patch_value"])
		return setPayloadValueAt(base, collectionTokens, items)
	case "delete_entity":
		return deletePayloadValueAt(base, tokens)
	default:
		return false
	}
}

func deletePayloadValueAt(payload map[string]any, tokens []any) bool {
	if len(tokens) < 2 {
		return false
	}
	index, ok := tokens[len(tokens)-1].(int)
	if !ok {
		return false
	}
	parentTokens := tokens[:len(tokens)-1]
	value, ok := payloadValueAt(payload, parentTokens)
	if !ok {
		return false
	}
	items, ok := value.([]any)
	if !ok || index < 0 || index >= len(items) {
		return false
	}
	items = append(items[:index], items[index+1:]...)
	return setPayloadValueAt(payload, parentTokens, items)
}

func isPatchRevisionIntent(intent string) bool {
	switch strings.TrimSpace(intent) {
	case "patch_artifact_field", "patch_artifact_entity", "patch_artifact_collection", "patch_artifact_section", "patch_script_span", "regenerate_script_scene":
		return true
	default:
		return false
	}
}

func applyScriptSpanPatch(base map[string]any, candidate map[string]any, pack revisionContextPack) bool {
	patch, ok := candidate["__script_span_patch"].(map[string]any)
	if !ok {
		return false
	}
	oldText := strings.TrimSpace(fmt.Sprint(patch["old_text"]))
	if oldText == "" || oldText == "<nil>" {
		oldText = strings.TrimSpace(pack.FocusedContext.SelectedText)
	}
	if oldText == "" {
		return false
	}
	lines := scriptBlockRefs(base)
	if len(lines) == 0 {
		return false
	}
	lineIDs := stringListValue(patch["line_ids"])
	if len(lineIDs) == 0 {
		lineIDs = append([]string(nil), pack.FocusedContext.LineIDs...)
	}
	if len(lineIDs) > 1 {
		return applyMultiLineScriptPatch(base, lines, lineIDs, oldText, patch)
	}

	nodeID := firstNonEmpty(strings.TrimSpace(fmt.Sprint(patch["node_id"])), pack.Target.NodeID, pack.FocusedContext.NodeID)
	newText := fmt.Sprint(patch["new_text"])
	if newText == "<nil>" {
		return false
	}
	for _, line := range lines {
		if nodeID != "" && line.LineID != nodeID {
			continue
		}
		lineOldText := scriptTextWithoutSpeakerPrefix(oldText, line.Block)
		lineNewText := scriptTextWithoutSpeakerPrefix(newText, line.Block)
		start := intValueOrZero(patch["selection_start"])
		end := intValueOrZero(patch["selection_end"])
		startByte, startOK := byteIndexAtUTF16Offset(line.Text, start)
		endByte, endOK := byteIndexAtUTF16Offset(line.Text, end)
		if lineOldText == oldText && startOK && endOK && endByte > startByte {
			if line.Text[startByte:endByte] == lineOldText {
				updated := line.Text[:startByte] + lineNewText + line.Text[endByte:]
				line.Block["text"] = updated
				syncScriptTextLines(base, []scriptTextChange{{Before: line.Text, After: updated}})
				return true
			}
		}
		if index := strings.Index(line.Text, lineOldText); index >= 0 {
			updated := line.Text[:index] + lineNewText + line.Text[index+len(lineOldText):]
			line.Block["text"] = updated
			syncScriptTextLines(base, []scriptTextChange{{Before: line.Text, After: updated}})
			return true
		}
	}
	return false
}

func scriptTextWithoutSpeakerPrefix(text string, block map[string]any) string {
	trimmed := strings.TrimSpace(text)
	speaker := strings.TrimSpace(fmt.Sprint(block["speaker"]))
	if speaker == "" || speaker == "<nil>" {
		return trimmed
	}
	for _, separator := range []string{":", "："} {
		prefix := speaker + separator
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		}
	}
	return trimmed
}

type scriptBlockRef struct {
	Block   map[string]any
	LineID  string
	SceneID string
	Text    string
}

type scriptTextChange struct{ Before, After string }

func scriptBlockRefs(payload map[string]any) []scriptBlockRef {
	var result []scriptBlockRef
	scenes, _ := payload["scenes"].([]any)
	for _, sceneValue := range scenes {
		scene, ok := sceneValue.(map[string]any)
		if !ok {
			continue
		}
		sceneID := strings.TrimSpace(fmt.Sprint(scene["scene_id"]))
		blocks, _ := scene["blocks"].([]any)
		for index, blockValue := range blocks {
			block, ok := blockValue.(map[string]any)
			if !ok {
				continue
			}
			lineID := strings.TrimSpace(fmt.Sprint(block["line_id"]))
			if lineID == "" || lineID == "<nil>" {
				lineID = fmt.Sprintf("%s-line-%d", sceneID, index+1)
			}
			result = append(result, scriptBlockRef{Block: block, LineID: lineID, SceneID: sceneID, Text: fmt.Sprint(block["text"])})
		}
	}
	return result
}

func applyMultiLineScriptPatch(base map[string]any, lines []scriptBlockRef, lineIDs []string, oldText string, patch map[string]any) bool {
	byID := map[string]scriptBlockRef{}
	for _, line := range lines {
		byID[line.LineID] = line
	}
	replacements := replacementLineValues(patch["replacement_lines"])
	if len(replacements) != len(lineIDs) {
		return false
	}
	selected := make([]string, 0, len(lineIDs))
	selectedLines := make([]scriptBlockRef, 0, len(lineIDs))
	start := intValueOrZero(patch["selection_start"])
	end := intValueOrZero(patch["selection_end"])
	_, hasEnd := patch["selection_end"]
	for index, lineID := range lineIDs {
		line, ok := byID[lineID]
		if !ok || replacements[index].LineID != lineID {
			return false
		}
		from, to := 0, len(line.Text)
		if index == 0 {
			var ok bool
			from, ok = byteIndexAtUTF16Offset(line.Text, start)
			if !ok {
				return false
			}
		}
		if index == len(lineIDs)-1 {
			if hasEnd {
				var ok bool
				to, ok = byteIndexAtUTF16Offset(line.Text, end)
				if !ok {
					return false
				}
			}
		}
		if from < 0 || to < from || to > len(line.Text) {
			return false
		}
		selected = append(selected, line.Text[from:to])
		selectedLines = append(selectedLines, line)
	}
	if strings.TrimSpace(strings.Join(selected, "\n")) != strings.TrimSpace(oldText) {
		return false
	}
	changes := make([]scriptTextChange, 0, len(selectedLines))
	for index, line := range selectedLines {
		from, to := 0, len(line.Text)
		if index == 0 {
			from, _ = byteIndexAtUTF16Offset(line.Text, start)
		}
		if index == len(selectedLines)-1 {
			if hasEnd {
				to, _ = byteIndexAtUTF16Offset(line.Text, end)
			}
		}
		updated := line.Text[:from] + replacements[index].NewText + line.Text[to:]
		line.Block["text"] = updated
		changes = append(changes, scriptTextChange{Before: line.Text, After: updated})
	}
	syncScriptTextLines(base, changes)
	return true
}

type replacementLine struct{ LineID, NewText string }

func replacementLineValues(value any) []replacementLine {
	items, _ := value.([]any)
	result := make([]replacementLine, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		result = append(result, replacementLine{LineID: strings.TrimSpace(fmt.Sprint(entry["line_id"])), NewText: fmt.Sprint(entry["new_text"])})
	}
	return result
}

func stringListValue(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func intValueOrZero(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := strconv.Atoi(string(typed))
		return parsed
	default:
		parsed, _ := strconv.Atoi(strings.TrimSpace(fmt.Sprint(value)))
		return parsed
	}
}

func byteIndexAtUTF16Offset(text string, offset int) (int, bool) {
	if offset < 0 {
		return 0, false
	}
	units := 0
	for index, char := range text {
		if units == offset {
			return index, true
		}
		width := 1
		if char > 0xFFFF {
			width = 2
		}
		if units+width > offset {
			return 0, false
		}
		units += width
	}
	if units == offset {
		return len(text), true
	}
	return 0, false
}

func syncScriptTextLines(payload map[string]any, changes []scriptTextChange) {
	scriptText, ok := payload["script_text"].(string)
	if !ok {
		return
	}
	for _, change := range changes {
		if change.Before != change.After {
			scriptText = strings.Replace(scriptText, change.Before, change.After, 1)
		}
	}
	payload["script_text"] = scriptText
}

func applyScriptScenePatch(base map[string]any, candidate map[string]any, sceneID string) bool {
	patchScene, ok := candidate["__script_scene_patch"].(map[string]any)
	if !ok || sceneID == "" {
		return false
	}
	if !patchSceneInPayload(base, map[string]any{"scenes": []any{patchScene}}, sceneID) {
		return false
	}
	rebuildScriptTextFromScenes(base)
	return true
}

func rebuildScriptTextFromScenes(payload map[string]any) {
	lines := make([]string, 0)
	if title := strings.TrimSpace(fmt.Sprint(payload["title"])); title != "" && title != "<nil>" {
		lines = append(lines, title)
	}
	scenes, _ := payload["scenes"].([]any)
	for _, value := range scenes {
		scene, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if heading := strings.TrimSpace(fmt.Sprint(scene["heading"])); heading != "" && heading != "<nil>" {
			lines = append(lines, heading)
		}
		blocks, _ := scene["blocks"].([]any)
		for _, blockValue := range blocks {
			block, ok := blockValue.(map[string]any)
			if !ok {
				continue
			}
			text := strings.TrimSpace(fmt.Sprint(block["text"]))
			if text == "" || text == "<nil>" {
				continue
			}
			speaker := strings.TrimSpace(fmt.Sprint(block["speaker"]))
			if speaker != "" && speaker != "<nil>" && !strings.HasPrefix(text, speaker+":") && !strings.HasPrefix(text, speaker+"：") {
				text = speaker + ": " + text
			}
			lines = append(lines, text)
		}
	}
	payload["script_text"] = strings.Join(lines, "\n")
}

func patchPayloadEntity(base map[string]any, candidate map[string]any, fieldPath string) bool {
	if applyArtifactValuePatch(base, candidate, fieldPath) {
		return true
	}
	resolvedPath := resolveNamedFieldSelectors(fieldPath, base)
	tokens := entityPathTokens(parseFieldPath(resolvedPath))
	if len(tokens) == 0 {
		return false
	}
	value, ok := payloadValueAt(candidate, tokens)
	if !ok {
		return false
	}
	return setPayloadValueAt(base, tokens, value)
}

func patchPayloadField(base map[string]any, candidate map[string]any, fieldPath string) bool {
	if applyArtifactValuePatch(base, candidate, fieldPath) {
		return true
	}
	resolvedPath := resolveNamedFieldSelectors(fieldPath, base)
	tokens := parseFieldPath(resolvedPath)
	if len(tokens) == 0 {
		return false
	}
	value, ok := payloadValueAt(candidate, tokens)
	if !ok {
		return false
	}
	return setPayloadValueAt(base, tokens, value)
}

func applyArtifactValuePatch(base map[string]any, candidate map[string]any, targetPath string) bool {
	patch, ok := candidate["__artifact_value_patch"].(map[string]any)
	if !ok {
		return false
	}
	fieldPath := strings.TrimSpace(fmt.Sprint(patch["field_path"]))
	if fieldPath == "" || (strings.TrimSpace(targetPath) != "" && fieldPath != strings.TrimSpace(targetPath)) {
		return false
	}
	fieldPath = resolveNamedFieldSelectors(fieldPath, base)
	return setPayloadValueAt(base, parseFieldPath(fieldPath), patch["patch_value"])
}

func resolveNamedFieldSelectors(fieldPath string, payload map[string]any) string {
	resolved := fieldPath
	searchFrom := 0
	for searchFrom < len(resolved) {
		relativeOpen := strings.Index(resolved[searchFrom:], "[")
		if relativeOpen < 0 {
			break
		}
		open := searchFrom + relativeOpen
		relativeClose := strings.Index(resolved[open+1:], "]")
		if relativeClose < 0 {
			break
		}
		close := open + 1 + relativeClose
		selector := strings.TrimSpace(resolved[open+1 : close])
		if _, err := strconv.Atoi(selector); err == nil {
			searchFrom = close + 1
			continue
		}
		collectionPath := strings.TrimSuffix(strings.TrimSpace(resolved[:open]), ".")
		collection, ok := payloadValueAt(payload, parseFieldPath(collectionPath))
		if !ok {
			return fieldPath
		}
		items, ok := collection.([]any)
		if !ok {
			return fieldPath
		}
		selectorKey := ""
		selectorValue := selector
		if equals := strings.Index(selector, "="); equals > 0 {
			selectorKey = strings.TrimSpace(selector[:equals])
			selectorValue = strings.Trim(strings.TrimSpace(selector[equals+1:]), "\"'")
		}
		matched := -1
		for index, item := range items {
			object, ok := item.(map[string]any)
			if !ok {
				continue
			}
			keys := []string{selectorKey}
			if selectorKey == "" {
				keys = []string{"name", "id", "character_id", "source_unit_id", "scene_id", "line_id", "role"}
			}
			for _, key := range keys {
				if key != "" && strings.TrimSpace(fmt.Sprint(object[key])) == selectorValue {
					matched = index
					break
				}
			}
			if matched >= 0 {
				break
			}
		}
		if matched < 0 {
			return fieldPath
		}
		replacement := fmt.Sprintf("[%d]", matched)
		resolved = resolved[:open] + replacement + resolved[close+1:]
		searchFrom = open + len(replacement)
	}
	return resolved
}

func entityPathTokens(tokens []any) []any {
	if len(tokens) == 0 {
		return nil
	}
	for index, token := range tokens {
		if _, ok := token.(int); ok {
			return append([]any(nil), tokens[:index+1]...)
		}
	}
	if len(tokens) > 1 {
		return append([]any(nil), tokens[:1]...)
	}
	return append([]any(nil), tokens...)
}

func patchEpisodeInPayload(base map[string]any, candidate map[string]any, episodeID any) bool {
	target := episodeNumber(episodeID)
	if target == 0 {
		return false
	}
	baseEpisodes, ok := base["episodes"].([]any)
	if !ok {
		return false
	}
	candidateEpisodes, ok := candidate["episodes"].([]any)
	if !ok {
		return false
	}
	var replacement any
	for _, item := range candidateEpisodes {
		if episodeNumberFromPayloadItem(item) == target {
			replacement = item
			break
		}
	}
	if replacement == nil {
		return false
	}
	for index, item := range baseEpisodes {
		if episodeNumberFromPayloadItem(item) == target {
			baseEpisodes[index] = replacement
			base["episodes"] = baseEpisodes
			return true
		}
	}
	return false
}

func patchSceneInPayload(base map[string]any, candidate map[string]any, sceneID string) bool {
	sceneID = strings.TrimSpace(sceneID)
	if sceneID == "" {
		return false
	}
	baseScenes, ok := base["scenes"].([]any)
	if !ok {
		return false
	}
	candidateScenes, ok := candidate["scenes"].([]any)
	if !ok {
		return false
	}
	var replacement any
	for _, item := range candidateScenes {
		if payloadItemString(item, "scene_id") == sceneID || payloadItemString(item, "id") == sceneID {
			replacement = item
			break
		}
	}
	if replacement == nil {
		return false
	}
	for index, item := range baseScenes {
		if payloadItemString(item, "scene_id") == sceneID || payloadItemString(item, "id") == sceneID {
			baseScenes[index] = replacement
			base["scenes"] = baseScenes
			return true
		}
	}
	return false
}

func deepClonePayload(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return clonePayload(payload)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return clonePayload(payload)
	}
	return out
}

func parseFieldPath(path string) []any {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	parts := strings.Split(path, ".")
	tokens := make([]any, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		for part != "" {
			bracket := strings.Index(part, "[")
			if bracket < 0 {
				tokens = append(tokens, part)
				break
			}
			if bracket > 0 {
				tokens = append(tokens, part[:bracket])
			}
			closeBracket := strings.Index(part[bracket:], "]")
			if closeBracket < 0 {
				break
			}
			indexText := part[bracket+1 : bracket+closeBracket]
			if index, err := strconv.Atoi(indexText); err == nil {
				tokens = append(tokens, index)
			}
			part = part[bracket+closeBracket+1:]
		}
	}
	return tokens
}

func payloadValueAt(payload map[string]any, tokens []any) (any, bool) {
	var current any = payload
	for _, token := range tokens {
		switch typed := token.(type) {
		case string:
			node, ok := current.(map[string]any)
			if !ok {
				return nil, false
			}
			current, ok = node[typed]
			if !ok {
				return nil, false
			}
		case int:
			node, ok := current.([]any)
			if !ok || typed < 0 || typed >= len(node) {
				return nil, false
			}
			current = node[typed]
		}
	}
	return current, true
}

func setPayloadValueAt(payload map[string]any, tokens []any, value any) bool {
	if len(tokens) == 0 {
		return false
	}
	var current any = payload
	for index, token := range tokens {
		last := index == len(tokens)-1
		switch typed := token.(type) {
		case string:
			node, ok := current.(map[string]any)
			if !ok {
				return false
			}
			if last {
				node[typed] = value
				return true
			}
			current = node[typed]
		case int:
			node, ok := current.([]any)
			if !ok || typed < 0 || typed >= len(node) {
				return false
			}
			if last {
				node[typed] = value
				return true
			}
			current = node[typed]
		}
	}
	return false
}

func episodeNumberFromPayloadItem(item any) int {
	node, ok := item.(map[string]any)
	if !ok {
		return 0
	}
	if number := episodeNumber(node["episode_id"]); number != 0 {
		return number
	}
	return episodeNumber(node["episode_no"])
}

func payloadItemString(item any, key string) string {
	node, ok := item.(map[string]any)
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(node[key]))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func episodeNumberFromText(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	patterns := []string{
		`第\s*([0-9]+)\s*集`,
		`([0-9]+)\s*集`,
		`episode\s*([0-9]+)`,
	}
	lower := strings.ToLower(text)
	for _, pattern := range patterns {
		matches := regexp.MustCompile(pattern).FindStringSubmatch(lower)
		if len(matches) == 2 {
			if value, err := strconv.Atoi(matches[1]); err == nil {
				return value
			}
		}
	}
	chineseNumbers := []struct {
		Token string
		Value int
	}{
		{"第十", 10}, {"第九", 9}, {"第八", 8}, {"第七", 7}, {"第六", 6},
		{"第五", 5}, {"第四", 4}, {"第三", 3}, {"第二", 2}, {"第一", 1},
		{"十", 10}, {"九", 9}, {"八", 8}, {"七", 7}, {"六", 6},
		{"五", 5}, {"四", 4}, {"三", 3}, {"二", 2}, {"一", 1},
	}
	for _, item := range chineseNumbers {
		if strings.Contains(text, item.Token+"集") || strings.Contains(text, item.Token) {
			return item.Value
		}
	}
	return 0
}

func episodeNumber(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case string:
		trimmed := strings.TrimSpace(typed)
		if number, err := strconv.Atoi(trimmed); err == nil {
			return number
		}
		return episodeNumberFromText(trimmed)
	default:
		return 0
	}
}

func sameEpisodeID(a map[string]any, b map[string]any) bool {
	left := episodeNumber(a["episode_id"])
	right := episodeNumber(b["episode_id"])
	if left > 0 && right > 0 {
		return left == right
	}
	return fmt.Sprint(a["episode_id"]) == fmt.Sprint(b["episode_id"])
}

func scriptUnitArtifactIDs(artifacts []agent.Artifact) []string {
	ids := []string{}
	for _, artifact := range latestScriptUnitsByEpisode(artifacts) {
		ids = append(ids, artifact.ArtifactID)
	}
	return ids
}

func latestScriptUnitsByEpisode(artifacts []agent.Artifact) []agent.Artifact {
	byEpisode := map[int]agent.Artifact{}
	fallback := []agent.Artifact{}
	for _, artifact := range artifacts {
		if artifact.ArtifactType != "script_unit" {
			continue
		}
		if artifact.Status == agent.ArtifactSuperseded || artifact.Status == agent.ArtifactInvalidated {
			continue
		}
		episode := episodeNumber(artifact.Payload["episode_id"])
		if episode <= 0 {
			fallback = append(fallback, artifact)
			continue
		}
		byEpisode[episode] = artifact
	}
	if len(byEpisode) == 0 {
		return fallback
	}
	episodeIDs := make([]int, 0, len(byEpisode))
	for episode := range byEpisode {
		episodeIDs = append(episodeIDs, episode)
	}
	sort.Ints(episodeIDs)
	out := make([]agent.Artifact, 0, len(byEpisode))
	for _, episode := range episodeIDs {
		out = append(out, byEpisode[episode])
	}
	out = append(out, fallback...)
	return out
}

func aggregateScriptsPayload(sourceMode agent.SourceMode, artifacts []agent.Artifact) map[string]any {
	units := latestScriptUnitsByEpisode(artifacts)
	scriptUnits := make([]any, 0, len(units))
	qualityFlags := []any{}
	for _, artifact := range units {
		unit := map[string]any{
			"episode_id":              artifact.Payload["episode_id"],
			"script_unit_artifact_id": artifact.ArtifactID,
			"version":                 artifact.Version,
			"status":                  string(artifact.Status),
		}
		for _, key := range []string{"title", "script_text", "scenes", "source_refs", "source_basis", "risk_notes", "continuity_delta", "self_check"} {
			if value, ok := artifact.Payload[key]; ok {
				unit[key] = value
			}
		}
		scriptUnits = append(scriptUnits, unit)
	}
	if len(scriptUnits) == 0 {
		qualityFlags = append(qualityFlags, map[string]any{
			"level":   "error",
			"message": "No script_unit artifacts were available when assembling scripts.",
		})
	}
	return map[string]any{
		"source_mode":             string(sourceMode),
		"episode_count":           len(scriptUnits),
		"script_units":            scriptUnits,
		"global_continuity_state": map[string]any{},
		"quality_flags":           qualityFlags,
	}
}

func scriptUnitTaskNote(note string, episodeID any, cursor int, total int) string {
	var builder strings.Builder
	if strings.TrimSpace(note) != "" {
		builder.WriteString(note)
		builder.WriteString("\n\n")
	}
	builder.WriteString(fmt.Sprintf("TASK: Generate only episode_id=%v. This is script episode task %d of %d. Do not generate other episodes in this request. If previous script_unit artifacts exist, preserve their continuity and continue from them.", episodeID, cursor+1, total))
	return builder.String()
}

func sequenceForMode(sourceMode agent.SourceMode) []string {
	if sequence, ok := fullArtifactSequenceByMode[sourceMode]; ok {
		return sequence
	}
	return fullArtifactSequenceByMode[agent.SourceModeNonNovel]
}

func firstArtifactForMode(sourceMode agent.SourceMode) (string, bool) {
	sequence := sequenceForMode(sourceMode)
	if len(sequence) == 0 {
		return "", false
	}
	return sequence[0], true
}

func nextArtifactAfter(sourceMode agent.SourceMode, artifactType string) (string, bool) {
	sequence := sequenceForMode(sourceMode)
	for index, current := range sequence {
		if current == artifactType && index+1 < len(sequence) {
			return sequence[index+1], true
		}
	}
	return "", false
}

func artifactRequiresApproval(artifactType string) bool {
	return artifactType != "" && artifactType != "source_input" && artifactType != "script_context" && artifactType != "scripts"
}

func isScriptArtifact(artifactType string) bool {
	return artifactType == "script_context" || artifactType == "script_unit" || artifactType == "scripts"
}

func stepIDForArtifact(artifactType string) string {
	switch artifactType {
	case "story_bible":
		return "step_build_story_bible"
	case "episode_split":
		return "step_split_episodes"
	case "material_bank":
		return "step_build_material_bank"
	case "story_seed":
		return "step_build_story_seed"
	case "series_blueprint":
		return "step_build_series_blueprint"
	case "episode_cards":
		return "step_plan_episode_cards"
	case "script_context":
		return "step_build_script_context"
	case "script_unit":
		return "step_generate_script_unit"
	case "scripts":
		return "step_build_scripts"
	default:
		return "step_build_artifact"
	}
}

func planArtifactSequence(sourceMode agent.SourceMode) []string {
	if sourceMode == agent.SourceModeNovel {
		return []string{"story_bible", "episode_split", "episode_cards"}
	}
	return []string{"material_bank", "story_seed", "series_blueprint", "episode_cards"}
}

func stepStartMessageForArtifact(artifactType string) string {
	switch artifactType {
	case "story_bible":
		return "Generating story bible"
	case "episode_split":
		return "Splitting source episodes"
	case "material_bank":
		return "Generating material bank"
	case "story_seed":
		return "Generating story seed"
	case "series_blueprint":
		return "Generating series blueprint"
	case "episode_cards":
		return "Planning episode cards"
	case "script_context":
		return "Preparing script context"
	case "script_unit":
		return "Writing script unit"
	case "scripts":
		return "Assembling scripts"
	default:
		return "Running step"
	}
}

func eventMessageForArtifact(artifactType string) string {
	switch artifactType {
	case "story_bible":
		return "Story bible generated"
	case "episode_split":
		return "Episode split generated"
	case "material_bank":
		return "Material bank generated"
	case "story_seed":
		return "Story seed generated"
	case "series_blueprint":
		return "Series blueprint generated"
	case "episode_cards":
		return "Episode cards generated"
	case "script_context":
		return "Script context prepared"
	case "script_unit":
		return "Script unit generated"
	case "scripts":
		return "Scripts artifact completed"
	default:
		return "Artifact generated"
	}
}

func stepFailureMessageForArtifact(artifactType string) string {
	if isScriptArtifact(artifactType) {
		return "Script generation failed"
	}
	return "Planning failed"
}

func approvalArtifactType(approval agent.ApprovalRequest, fallbackStepID string) string {
	if approval.ProposedAction != nil {
		if value, ok := approval.ProposedAction["approved_artifact"].(string); ok && value != "" {
			return value
		}
	}
	return artifactTypeFromApprovalStep(fallbackStepID)
}

func artifactTypeFromApprovalStep(stepID string) string {
	const prefix = "step_approval_"
	if strings.HasPrefix(stepID, prefix) {
		return strings.TrimPrefix(stepID, prefix)
	}
	return ""
}

func artifactTypeFromStepID(stepID string) string {
	switch stepID {
	case "step_build_story_bible":
		return "story_bible"
	case "step_split_episodes":
		return "episode_split"
	case "step_build_material_bank":
		return "material_bank"
	case "step_build_story_seed":
		return "story_seed"
	case "step_build_series_blueprint":
		return "series_blueprint"
	case "step_plan_episode_cards":
		return "episode_cards"
	case "step_build_script_context":
		return "script_context"
	case "step_generate_script_unit":
		return "script_unit"
	case "step_build_scripts":
		return "scripts"
	default:
		return ""
	}
}

func currentArtifactTypeFromRun(run agent.Run) string {
	if value, ok := run.NextAction["artifact_type"].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	if value, ok := run.NextAction["target_artifact"].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return artifactTypeFromStepID(run.CurrentStepID)
}

func artifactDisplayName(artifactType string) string {
	switch artifactType {
	case "story_bible":
		return "故事圣经"
	case "episode_split":
		return "原文拆集"
	case "material_bank":
		return "素材库"
	case "story_seed":
		return "故事种子"
	case "series_blueprint":
		return "剧集蓝图"
	case "episode_cards":
		return "分集卡"
	case "script_context":
		return "剧本上下文"
	case "script_unit":
		return "分集剧本"
	case "scripts":
		return "剧本"
	default:
		return "当前产物"
	}
}

func approvalTitleForArtifact(artifactType string) string {
	return "确认" + artifactDisplayName(artifactType) + "后继续"
}

func approvalReasonForArtifact(artifactType string, nextArtifactType string) string {
	if nextArtifactType == "" {
		return "请检查" + artifactDisplayName(artifactType) + "是否符合预期。确认后 Agent 会完成本次流程；如果需要调整，请先补充说明。"
	}
	return "请检查" + artifactDisplayName(artifactType) + "是否符合预期。确认后 Agent 会继续生成" + artifactDisplayName(nextArtifactType) + "；如果需要调整，请先补充说明。"
}

func approvalRequestedMessage(artifactType string) string {
	return artifactDisplayName(artifactType) + "需要确认后再进入下一步"
}

func approvalResolvedMessage(artifactType string) string {
	return artifactDisplayName(artifactType) + "已确认"
}
func affectedArtifactsAfter(sourceMode agent.SourceMode, artifactType string) []string {
	sequence := sequenceForMode(sourceMode)
	if artifactType == "source_input" {
		out := make([]string, len(sequence))
		copy(out, sequence)
		return out
	}
	for index, current := range sequence {
		if current == artifactType && index+1 < len(sequence) {
			out := make([]string, len(sequence[index+1:]))
			copy(out, sequence[index+1:])
			return out
		}
	}
	return nil
}
