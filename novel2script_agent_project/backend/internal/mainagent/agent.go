package mainagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"novel2script-agent/backend/internal/agent"
	"novel2script-agent/backend/internal/llm"
)

const attachmentContextLimit = 2400

type Agent struct {
	llm                  llm.ChatClient
	controlModel         string
	controlFallbackModel string
}

func New(chatClient llm.ChatClient, controlModel string) *Agent {
	return NewWithFallback(chatClient, controlModel, "")
}

func NewWithFallback(chatClient llm.ChatClient, controlModel string, fallbackModel string) *Agent {
	return &Agent{
		llm:                  chatClient,
		controlModel:         strings.TrimSpace(controlModel),
		controlFallbackModel: strings.TrimSpace(fallbackModel),
	}
}

func (a *Agent) Decide(ctx context.Context, input Context) Decision {
	modelDecision, applyGuards := a.decideModel(ctx, input)
	guardedDecision := modelDecision
	if applyGuards {
		guardedDecision = normalizeDecision(modelDecision, input)
	}
	guardedDecision.Orchestrator = "native"
	return withDecisionTrace(modelDecision, guardedDecision)
}

func (a *Agent) decideModel(ctx context.Context, input Context) (Decision, bool) {
	if decision, ok := preemptiveRevisionDecision(input); ok {
		return decision, true
	}
	if a.llm != nil && a.llm.Configured() {
		decision, err := a.decideWithModel(ctx, input, a.controlModel)
		if err == nil {
			if inspectReplyDefersWork(decision) {
				corrected, correctionErr := a.correctDeferredInspectReply(ctx, input, decision, a.controlModel)
				if correctionErr == nil && !inspectReplyDefersWork(corrected) {
					decision = corrected
				} else {
					decision.NextAction = ActionReply
					decision.AgentReply = incompleteInspectReply(input)
					decision.Reason = "inspect_reply_deferred_without_executor"
				}
			}
			decision.Runtime = "model"
			return decision, true
		}

		if fallbackModel := a.usableFallbackModel(); fallbackModel != "" {
			fallbackDecision, fallbackErr := a.decideWithModel(ctx, input, fallbackModel)
			if fallbackErr == nil {
				fallbackDecision.Runtime = "model_fallback"
				fallbackDecision.Warning = "primary_control_model_failed:" + summarizeError(err)
				return fallbackDecision, true
			}
			err = fmt.Errorf("primary control model failed: %w; fallback control model failed: %v", err, fallbackErr)
		}

		errorSummary := summarizeError(err)
		fallback := fallbackDecision(input)
		fallback.Intent = IntentUnsupported
		fallback.Reason = "control_model_" + errorSummary
		fallback.Warning = "模型控制调用失败，已停止语义判断：" + errorSummary
		fallback.AgentReply = controlModelFailureReply(errorSummary)
		return fallback, false
	}

	return fallbackDecision(input), false
}

func withDecisionTrace(model Decision, guarded Decision) Decision {
	model.Trace = nil
	guarded.Trace = &DecisionTrace{
		ModelDecision: snapshotDecision(model), GuardedDecision: snapshotDecision(guarded),
		GuardApplied: decisionGuardApplied(model, guarded),
	}
	return guarded
}

func decisionGuardApplied(model Decision, guarded Decision) bool {
	return model.Intent != guarded.Intent || model.NextAction != guarded.NextAction ||
		model.AgentReply != guarded.AgentReply || model.RevisionIntent != guarded.RevisionIntent ||
		(validSourceMode(model.SourceMode) && model.SourceMode != guarded.SourceMode)
}

func validSourceMode(mode agent.SourceMode) bool {
	return mode == agent.SourceModeNovel || mode == agent.SourceModeNonNovel || mode == agent.SourceModeAuto
}

func snapshotDecision(decision Decision) DecisionSnapshot {
	return DecisionSnapshot{Intent: decision.Intent, NextAction: decision.NextAction, SourceMode: decision.SourceMode, AgentReply: decision.AgentReply, RevisionIntent: decision.RevisionIntent}
}

func (a *Agent) usableFallbackModel() string {
	fallbackModel := strings.TrimSpace(a.controlFallbackModel)
	if fallbackModel == "" {
		return ""
	}
	if strings.EqualFold(fallbackModel, strings.TrimSpace(a.controlModel)) {
		return ""
	}
	return fallbackModel
}

func (a *Agent) decideWithModel(ctx context.Context, input Context, model string) (Decision, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return Decision{}, errors.New("control model is not configured")
	}
	decisionInput := input
	decisionInput.Request.Attachments = append([]FileAttachment(nil), input.Request.Attachments...)
	for index := range decisionInput.Request.Attachments {
		attachment := &decisionInput.Request.Attachments[index]
		attachment.ContentBase64 = ""
		attachment.TextContent = truncateRunes(attachment.TextContent, attachmentContextLimit)
	}
	decisionInput = compactDecisionInput(decisionInput)

	payload, err := json.Marshal(decisionInput)
	if err != nil {
		return Decision{}, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()

	resp, err := a.llm.Complete(reqCtx, llm.ChatRequest{
		Model:        model,
		Temperature:  llm.Temperature(0.1),
		TraceContext: controlTraceContext(input, "decision"),
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: decisionSystemPrompt() + "\n\n" + contextPackSystemInstruction()},
			{Role: llm.RoleUser, Content: string(payload)},
		},
	})
	if err != nil {
		return Decision{}, err
	}

	var decision Decision
	if err := json.Unmarshal([]byte(extractJSONObject(resp.Content)), &decision); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

func (a *Agent) correctDeferredInspectReply(ctx context.Context, input Context, previous Decision, model string) (Decision, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return Decision{}, errors.New("control model is not configured")
	}
	correctionInput := map[string]any{
		"context":           compactDecisionInput(input),
		"previous_decision": previous,
		"correction": "The previous inspect_artifact reply deferred the answer, but inspect_artifact has no later executor. " +
			"Return the complete analysis in agent_reply now. Do not say wait, later, or that you will inspect it. " +
			"Do not modify content. If context is insufficient, ask one precise clarification question instead.",
	}
	payload, err := json.Marshal(correctionInput)
	if err != nil {
		return Decision{}, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	resp, err := a.llm.Complete(reqCtx, llm.ChatRequest{
		Model:        model,
		Temperature:  llm.Temperature(0.1),
		TraceContext: controlTraceContext(input, "inspect_reply_correction"),
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: decisionSystemPrompt() + "\n\n" + contextPackSystemInstruction()},
			{Role: llm.RoleUser, Content: string(payload)},
		},
	})
	if err != nil {
		return Decision{}, err
	}
	var decision Decision
	if err := json.Unmarshal([]byte(extractJSONObject(resp.Content)), &decision); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

func normalizeDecision(decision Decision, input Context) Decision {
	if decision.Confidence <= 0 || decision.Confidence > 1 {
		decision.Confidence = 0.7
	}
	if decision.NextAction == "" {
		decision.NextAction = ActionReply
	}
	if decision.Intent == "" {
		decision.Intent = intentForAction(decision.NextAction)
	}
	if decision.SourceMode == "" || decision.SourceMode == agent.SourceModeUnknown {
		decision.SourceMode = normalizedSourceMode(input.Request.SourceMode)
	}
	if decision.SourceMode == agent.SourceModeUnknown {
		decision.SourceMode = agent.SourceModeAuto
	}
	if (decision.GenerationConfig == nil || decision.GenerationConfig.Empty()) && input.Request.GenerationConfig != nil && !input.Request.GenerationConfig.Empty() {
		decision.GenerationConfig = input.Request.GenerationConfig
	}
	if strings.TrimSpace(decision.AgentReply) == "" {
		decision.AgentReply = defaultReplyForDecision(decision, input)
	}

	if shouldForceFocusedRevision(decision, input) {
		decision = focusedRevisionDecision(decision, input, input.FocusedContext, false)
	} else if shouldForceRecentSelectionRevision(decision, input) {
		decision = focusedRevisionDecision(decision, input, recentFocusedContext(input), true)
	}
	if shouldForceCheckpointRevision(decision, input) {
		decision = checkpointRevisionDecision(decision, input)
	}
	if decision.NextAction == ActionReviseCheckpoint {
		if casualGreetingText(input.Request.Message) && !isRevisionContinuation(decision, input) {
			decision = guardedReply(decision, input, decision.AgentReply)
		} else if bareRevisionRequest(input.Request.Message) && !isRevisionContinuation(decision, input) {
			decision = guardedReply(decision, input, "我已经定位到你选中的内容，但还不知道希望改成什么方向。请补充要改成怎样，确认后我再执行。")
		}
	}
	if decision.NextAction == ActionReviseCheckpoint {
		revisionInput := input
		if isRevisionContinuation(decision, input) && revisionInput.FocusedContext == nil {
			revisionInput.FocusedContext = recentFocusedContext(input)
		}
		decision.RevisionTarget = defaultRevisionTarget(decision, revisionInput)
		if decision.RevisionTarget != nil {
			decision.RevisionIntent = normalizeRevisionIntentForResolvedTarget(decision.RevisionIntent, revisionInput, decision.RevisionTarget)
		} else {
			decision.RevisionIntent = normalizeRevisionIntent(decision.RevisionIntent, revisionInput)
		}
		if decision.RevisionIntent == RevisionIntentClarifyRevisionTarget || decision.RevisionIntent == RevisionIntentExplainOrLocate {
			message := strings.TrimSpace(decision.AgentReply)
			if message == "" {
				message = "我还不能唯一确定要修改的对象或范围，请先确认具体目标。"
			}
			decision = guardedReply(decision, input, message)
		} else if issue := revisionDecisionIssue(decision); issue != "" {
			decision = guardedReply(decision, input, issue)
		} else if agentReplyAsksQuestion(decision.AgentReply) {
			decision = guardedReply(decision, input, decision.AgentReply)
		}
	}

	if shouldForceGenerationIntent(decision, input) {
		decision = generationIntentDecision(decision, input)
	}

	if shouldForceGenerationConfigPrompt(decision, input) {
		decision = generationConfigRequiredReply(decision, input, generationConfigMissingReply(effectiveSourceMode(decision, input)))
	}
	if executableAction(decision.NextAction) && agentReplyRequiresUserInput(decision.AgentReply) {
		decision = guardedReply(decision, input, decision.AgentReply)
	}
	if decision.NextAction == ActionReply && agentReplyPromisesExecution(decision.AgentReply) {
		decision = guardedReply(decision, input, "这次还没有执行修改。我已经保留当前上下文，请明确修改方向后再提交。")
	}

	decision = guardDecision(decision, input)
	if strings.TrimSpace(decision.AgentReply) == "" {
		decision.AgentReply = defaultReplyForDecision(decision, input)
	}
	return decision
}

func preemptiveRevisionDecision(input Context) (Decision, bool) {
	if input.Run == nil {
		return Decision{}, false
	}
	validStatus := input.Run.Status == agent.RunWaitingApproval || input.Run.Status == agent.RunPaused || input.Run.Status == agent.RunCompleted || failedRevisionCanBeReplaced(input)
	if !validStatus {
		return Decision{}, false
	}
	message := strings.TrimSpace(input.Request.Message)
	if explicitNoRevisionRequest(message) || revisionAdviceRequest(message) {
		return Decision{}, false
	}
	base := Decision{Confidence: 1, Runtime: "rule", SourceMode: input.Run.SourceMode}
	if input.FocusedContext != nil && revisionRequestText(message) && !revisionInquiryText(message) && !bareRevisionRequest(message) {
		return focusedRevisionDecision(base, input, input.FocusedContext, false), true
	}
	if retrySelectionRequest(message) && lastAgentControlFailure(input) {
		if focused := recentFocusedContext(input); focused != nil && recentSelectedRevisionRequest(input) != "" {
			decision := focusedRevisionDecision(base, input, focused, true)
			decision.Reason = "forced_revision_retry_after_control_failure"
			return decision, true
		}
	}
	if revisionConfirmationText(message) && hasPendingRevisionClarification(input) {
		if focused := recentFocusedContext(input); focused != nil && recentSelectedRevisionRequest(input) != "" {
			decision := focusedRevisionDecision(base, input, focused, true)
			decision.Reason = "forced_revision_from_conversation_continuation"
			return decision, true
		}
	}
	return Decision{}, false
}

func retrySelectionRequest(message string) bool {
	normalized := strings.ToLower(strings.Trim(strings.TrimSpace(message), "。.!！?？,， "))
	switch normalized {
	case "重试", "再试", "再试一次", "重试刚才请求", "重试刚才的请求", "retry", "try again":
		return true
	default:
		return false
	}
}

func lastAgentControlFailure(input Context) bool {
	if input.Conversation == nil {
		return false
	}
	for index := len(input.Conversation.RecentTurns) - 1; index >= 0; index-- {
		turn := input.Conversation.RecentTurns[index]
		if turn.Role != "agent" {
			continue
		}
		content := strings.TrimSpace(turn.Content)
		return Intent(strings.TrimSpace(turn.Intent)) == IntentUnsupported || containsAny(content,
			"控制模型请求超时", "控制模型暂时不可用", "主控流程暂时无法完成判断",
		)
	}
	return false
}

func executableAction(action NextAction) bool {
	switch action {
	case ActionStartRun, ActionApproveRun, ActionPauseRun, ActionResumeRun, ActionReviseCheckpoint, ActionRerunStep:
		return true
	default:
		return false
	}
}

func agentReplyPromisesExecution(reply string) bool {
	normalized := strings.TrimSpace(reply)
	return containsAny(normalized,
		"我会按你的要求修订", "生成新版后再让你确认", "我现在就实际修改", "我会修改", "我来修改", "开始修改", "执行修改", "我会继续推进后续生成",
	)
}

func inspectReplyDefersWork(decision Decision) bool {
	if decision.NextAction != ActionInspectArtifact {
		return false
	}
	reply := strings.ToLower(strings.TrimSpace(decision.AgentReply))
	return reply == "" || containsAny(reply,
		"稍等", "等我", "稍后", "我来看看", "我先看看", "我会查看", "我先分析", "分析后再", "马上给你",
		"please wait", "give me a moment", "i will inspect", "i'll inspect", "i will analyze", "i'll analyze",
	)
}

func incompleteInspectReply(input Context) string {
	if selected := strings.TrimSpace(input.Request.SelectedText); selected != "" {
		return "这次不会修改你选中的内容。当前分析没有完整返回，请直接说明想重点检查情绪、动作、台词还是节奏，我会在本轮给出结论。"
	}
	return "这次没有修改任何产物。当前分析没有完整返回，请明确要查看的产物和问题，我会在本轮直接回答。"
}

func agentReplyRequiresUserInput(reply string) bool {
	if agentReplyAsksQuestion(reply) {
		return true
	}
	normalized := strings.TrimSpace(reply)
	return containsAny(normalized, "请问", "请告诉我", "请补充", "请提供", "你希望", "需要你确认", "需要先确认")
}

func casualGreetingText(message string) bool {
	normalized := strings.ToLower(strings.Trim(strings.TrimSpace(message), "。.!！?？,， "))
	switch normalized {
	case "你好", "你好呀", "您好", "嗨", "hi", "hello", "在吗":
		return true
	default:
		return false
	}
}

func bareRevisionRequest(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	replacer := strings.NewReplacer(
		"修改", "", "调整", "", "改一下", "", "改下", "", "改", "", "重写", "", "重新写", "",
		"这句话", "", "这句", "", "这一句", "", "这里", "", "这个", "", "选中内容", "", "选中的内容", "",
		"吧", "", "呢", "", "啊", "", "呀", "", "。", "", "！", "", "!", "", "？", "", "?", "", "，", "", ",", "", " ", "",
	)
	return revisionRequestText(normalized) && replacer.Replace(normalized) == ""
}

func revisionDecisionIssue(decision Decision) string {
	target := decision.RevisionTarget
	if target == nil || strings.TrimSpace(target.ArtifactType) == "" {
		return "我还不能确定要修改哪一个产物，请先指定当前产物或选中要修改的内容。"
	}
	artifactType := strings.TrimSpace(target.ArtifactType)
	scope := strings.TrimSpace(target.Scope)
	fieldPath := strings.TrimSpace(target.FieldPath)
	switch decision.RevisionIntent {
	case RevisionIntentPatchArtifactField:
		if fieldPath == "" {
			return "我已经识别到字段级修改，但还不能定位具体字段，请先说明要修改哪一项。"
		}
		if strings.HasPrefix(fieldPath, "generation_config.") && strings.TrimSpace(target.EpisodeID) != "" {
			return "当前目标同时指向单集和全局生成配置。请确认是只调整这一集的规划，还是修改后续所有集共用的生成配置。"
		}
	case RevisionIntentPatchArtifactEntity:
		if fieldPath == "" && strings.TrimSpace(target.EpisodeID) == "" && strings.TrimSpace(target.SceneID) == "" {
			return "我已经识别到实体级修改，但还不能定位具体人物、分集或场景，请先确认目标。"
		}
	case RevisionIntentPatchArtifactCollection:
		if fieldPath == "" {
			return "我已经识别到列表结构修改，但还不能定位要拆分、合并、增删或移动的列表，请先确认目标。"
		}
	case RevisionIntentPatchArtifactSection:
		if fieldPath == "" {
			return "我已经识别到区块级修改，但还不能定位具体区块，请先确认目标。"
		}
	case RevisionIntentPatchScriptSpan:
		if artifactType != "script_unit" && artifactType != "scripts" {
			return "当前选区不是剧本正文，不能按剧本句子修改执行。"
		}
		if strings.TrimSpace(target.NodeID) == "" && strings.TrimSpace(target.SceneID) == "" {
			return "我还不能定位选中的剧本节点，请重新选中要修改的句子。"
		}
		if scope != "" && scope != "selection" {
			return "当前修改范围与剧本选区不一致，请重新确认修改范围。"
		}
	case RevisionIntentRegenerateScriptScene:
		if strings.TrimSpace(target.SceneID) == "" {
			return "请先指定要重写的场景。"
		}
	case RevisionIntentRegenerateScriptEpisode:
		if strings.TrimSpace(target.EpisodeID) == "" {
			return "请先指定要重写第几集。"
		}
	}
	return ""
}

func guardDecision(decision Decision, input Context) Decision {
	switch decision.NextAction {
	case ActionReply, ActionUnsupported:
		return decision
	case ActionStartRun:
		if input.Run != nil && (input.Run.Status == agent.RunRunning || input.Run.Status == agent.RunWaitingApproval || input.Run.Status == agent.RunPaused) {
			return guardedReply(decision, input, "当前已有流程在进行中，需要先继续、暂停或处理当前确认点，不能直接启动新流程。")
		}
		sourceMode := effectiveSourceMode(decision, input)
		if sourceMode == agent.SourceModeNovel || sourceMode == agent.SourceModeNonNovel {
			decision.SourceMode = sourceMode
			if !hasSufficientGenerationConfig(decision.GenerationConfig) {
				return generationConfigRequiredReply(decision, input, generationConfigMissingReply(sourceMode))
			}
			return decision
		}
		return guardedReply(decision, input, "我理解你想开始生成，但还不能稳定判断这是小说改编还是非小说素材生成。请先在左下角选择小说或非小说，或补充素材类型。")
	case ActionApproveRun:
		if input.Run == nil {
			return guardedReply(decision, input, "当前没有正在等待处理的流程，不能执行确认。")
		}
		if input.Run.Status != agent.RunWaitingApproval && !(input.Run.Status == agent.RunPaused && input.Run.ApprovalRequestID != "") {
			return guardedReply(decision, input, "当前流程不在确认或暂停状态，不能执行确认。")
		}
		decision.SourceMode = input.Run.SourceMode
		return decision
	case ActionPauseRun:
		if input.Run == nil {
			return guardedReply(decision, input, "当前没有正在执行或等待确认的流程，不能执行暂停。")
		}
		if input.Run.Status == agent.RunPaused {
			return guardedReply(decision, input, "当前流程已经暂停。")
		}
		if input.Run.Status != agent.RunRunning && input.Run.Status != agent.RunWaitingApproval {
			return guardedReply(decision, input, "当前流程不在运行或等待确认状态，不能执行暂停。")
		}
		decision.SourceMode = input.Run.SourceMode
		return decision
	case ActionResumeRun:
		if input.Run == nil {
			return guardedReply(decision, input, "当前没有可恢复的流程。")
		}
		if input.Run.Status != agent.RunPaused || !runPausedByUser(input.Run) {
			return guardedReply(decision, input, "当前流程不是用户主动暂停的生成任务，不能直接恢复。")
		}
		decision.SourceMode = input.Run.SourceMode
		return decision
	case ActionReviseCheckpoint:
		if input.Run == nil {
			return guardedReply(decision, input, "当前没有可修改的运行或产物。")
		}
		if input.Run.Status != agent.RunWaitingApproval && input.Run.Status != agent.RunPaused && input.Run.Status != agent.RunCompleted && !failedRevisionCanBeReplaced(input) {
			return guardedReply(decision, input, "当前流程正在生成或处于失败状态，请等待当前任务结束或先处理失败任务。")
		}
		if input.ApprovalRequest != nil && input.ApprovalRequest.Status == "pending" && decision.RevisionTarget != nil {
			pendingType := strings.TrimSpace(fmt.Sprint(input.ApprovalRequest.ProposedAction["approved_artifact"]))
			targetType := strings.TrimSpace(decision.RevisionTarget.ArtifactType)
			if pendingType != "" && targetType != "" && pendingType != targetType {
				return guardedReply(decision, input, "当前还有"+artifactTypeDisplayName(pendingType)+"等待确认。请先完成这项确认，再修改其他产物，避免两个版本状态互相覆盖。")
			}
		}
		decision.SourceMode = input.Run.SourceMode
		return decision
	case ActionInspectArtifact:
		if input.Run == nil && input.Request.SelectedArtifactID == "" {
			return guardedReply(decision, input, "当前没有可查看的产物。")
		}
		return decision
	case ActionInspectSource:
		if len(input.Request.Attachments) == 0 && (input.Project == nil || len(input.Project.Files) == 0) {
			return guardedReply(decision, input, "当前作品还没有可读取的附件。")
		}
		return decision
	case ActionRerunStep:
		if input.Run == nil {
			return guardedReply(decision, input, "当前没有可重跑的流程。")
		}
		if input.Run.Status != agent.RunFailed && !hasRestoredFailedTask(input.Run) {
			return guardedReply(decision, input, "当前流程没有停在失败状态，不能按失败任务重跑。")
		}
		if !explicitlyRequestsFailedTaskRetry(input.Request.Message) {
			return guardedReply(decision, input, "是的，当前流程停在失败步骤。我不会仅凭询问或质疑自动重试；你可以明确说“继续”或“重试失败步骤”。")
		}
		decision.SourceMode = input.Run.SourceMode
		decision.TargetArtifact = failedStepTarget(input.Run)
		return decision
	default:
		return guardedReply(decision, input, "我没有拿到可执行的下一步动作，需要你补充说明。")
	}
}

func hasRestoredFailedTask(run *agent.Run) bool {
	if run == nil || run.Metadata == nil {
		return false
	}
	task, _ := run.Metadata["last_failed_task"].(map[string]any)
	return len(task) > 0
}

func runPausedByUser(run *agent.Run) bool {
	if run == nil || run.Metadata == nil {
		return false
	}
	paused, _ := run.Metadata["paused_by_user"].(bool)
	return paused
}

// Retrying a failed model task can consume time and money. The model may infer
// intent, but execution still requires an explicit user command.
func explicitlyRequestsFailedTaskRetry(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	if containsAny(normalized,
		"继续", "接着", "重试", "再试", "重新试", "重新执行", "从失败处", "从失败的地方", "恢复生成", "继续生成",
		"retry", "try again", "continue", "resume", "rerun",
	) {
		return true
	}
	return false
}

func artifactTypeDisplayName(artifactType string) string {
	switch artifactType {
	case "source_input":
		return "输入材料"
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
		return "剧本单元"
	case "scripts":
		return "最终剧本"
	default:
		return "当前产物"
	}
}

func hasSufficientGenerationConfig(config *agent.GenerationConfig) bool {
	return config != nil && config.TargetEpisodeCount > 0 && config.EpisodeDurationMinutes > 0
}

func shouldForceGenerationIntent(decision Decision, input Context) bool {
	if input.Run != nil {
		return false
	}
	if !generationRequestText(input.Request.Message) {
		return false
	}
	if decision.NextAction == ActionStartRun || decision.Intent == IntentGenerateFromNovel || decision.Intent == IntentGenerateFromMaterial {
		return false
	}
	if decision.NextAction != ActionReply && decision.NextAction != ActionUnsupported {
		return false
	}
	return true
}

func generationIntentDecision(decision Decision, input Context) Decision {
	sourceMode := effectiveSourceMode(decision, input)
	intent := IntentGenerateFromMaterial
	if sourceMode == agent.SourceModeNovel {
		intent = IntentGenerateFromNovel
	}
	config := decision.GenerationConfig
	if (config == nil || config.Empty()) && input.Request.GenerationConfig != nil && !input.Request.GenerationConfig.Empty() {
		config = input.Request.GenerationConfig
	}
	return Decision{
		Intent:                   intent,
		Confidence:               maxConfidence(decision.Confidence, 0.65),
		NextAction:               ActionStartRun,
		SourceMode:               sourceMode,
		AgentReply:               defaultReplyForDecision(Decision{NextAction: ActionStartRun}, input),
		RequiresApproval:         false,
		RequiresGenerationConfig: decision.RequiresGenerationConfig,
		Reason:                   "forced_generation_intent_from_user_request",
		TargetArtifact:           decision.TargetArtifact,
		Runtime:                  decision.Runtime,
		Warning:                  decision.Warning,
		GenerationConfig:         config,
	}
}

func shouldForceCheckpointRevision(decision Decision, input Context) bool {
	if decision.NextAction != ActionReply {
		return false
	}
	if input.Run == nil {
		return false
	}
	if input.Run.Status != agent.RunWaitingApproval && input.Run.Status != agent.RunPaused && input.Run.Status != agent.RunCompleted {
		return false
	}
	if strings.TrimSpace(input.Request.Message) == "" {
		return false
	}
	if explicitNoRevisionRequest(input.Request.Message) || revisionAdviceRequest(input.Request.Message) {
		return false
	}
	if decision.Intent == IntentReviseCheckpoint {
		return true
	}
	if decision.Intent != IntentChatIdle || revisionInquiryText(input.Request.Message) {
		return false
	}
	if revisionRequestText(input.Request.Message) {
		return true
	}
	return isRevisionContinuation(decision, input)
}

func shouldForceFocusedRevision(decision Decision, input Context) bool {
	if input.Run == nil || input.FocusedContext == nil || strings.TrimSpace(input.FocusedContext.ArtifactID) == "" || strings.TrimSpace(input.FocusedContext.SelectedText) == "" {
		return false
	}
	if input.Run.Status != agent.RunWaitingApproval && input.Run.Status != agent.RunPaused && input.Run.Status != agent.RunCompleted && !failedRevisionCanBeReplaced(input) {
		return false
	}
	if explicitNoRevisionRequest(input.Request.Message) || revisionAdviceRequest(input.Request.Message) {
		return false
	}
	if revisionInquiryText(input.Request.Message) || bareRevisionRequest(input.Request.Message) {
		return false
	}
	return revisionRequestText(input.Request.Message) || decision.NextAction == ActionReviseCheckpoint
}

func shouldForceRecentSelectionRevision(decision Decision, input Context) bool {
	if input.Run == nil || input.FocusedContext != nil || decision.NextAction != ActionReply {
		return false
	}
	if input.Run.Status != agent.RunWaitingApproval && input.Run.Status != agent.RunPaused && input.Run.Status != agent.RunCompleted && !failedRevisionCanBeReplaced(input) {
		return false
	}
	if explicitNoRevisionRequest(input.Request.Message) || revisionAdviceRequest(input.Request.Message) {
		return false
	}
	if revisionInquiryText(input.Request.Message) || !revisionRequestText(input.Request.Message) {
		return false
	}
	focused := recentFocusedContext(input)
	return focused != nil && recentSelectedRevisionRequest(input) != ""
}

func focusedRevisionDecision(decision Decision, input Context, focused *FocusedContext, recovered bool) Decision {
	if focused == nil {
		return decision
	}
	target := &RevisionTarget{
		ArtifactID: focused.ArtifactID, ArtifactType: focused.ArtifactType, FieldPath: focused.FieldPath,
		EpisodeID: focused.EpisodeID, SceneID: focused.SceneID, NodeID: focused.NodeID,
		StartSceneID: focused.StartSceneID, EndSceneID: focused.EndSceneID,
		StartLineID: focused.StartLineID, EndLineID: focused.EndLineID,
		LineIDs: append([]string(nil), focused.LineIDs...),
	}
	intent := normalizeRevisionIntentForResolvedTarget(decision.RevisionIntent, input, target)
	if focused.ArtifactType == "script_unit" || focused.ArtifactType == "scripts" {
		intent = RevisionIntentPatchScriptSpan
		target.Scope = "selection"
	} else {
		target.Scope = strings.TrimSpace(focused.SelectionScope)
		if target.Scope == "" {
			target.Scope = "field"
		}
	}
	reason := "forced_revision_from_current_selection"
	if recovered {
		reason = "forced_revision_from_recent_selection"
	}
	reply := strings.TrimSpace(decision.AgentReply)
	if reply == "" || agentReplyRequiresUserInput(reply) {
		reply = focusedRevisionReply(focused, input.Run.Status == agent.RunFailed)
	}
	return Decision{
		Intent: IntentReviseCheckpoint, Confidence: maxConfidence(decision.Confidence, 0.9),
		NextAction: ActionReviseCheckpoint, SourceMode: input.Run.SourceMode, AgentReply: reply,
		RequiresApproval: false, Reason: reason, TargetArtifact: focused.ArtifactID,
		Runtime: decision.Runtime, Warning: decision.Warning, RevisionIntent: intent, RevisionTarget: target,
	}
}

func focusedRevisionReply(focused *FocusedContext, replacesFailure bool) string {
	prefix := ""
	if replacesFailure {
		prefix = "我会放弃上一次失败的局部修改，"
	}
	if focused != nil && (focused.ArtifactType == "script_unit" || focused.ArtifactType == "scripts") {
		episode := ""
		if strings.TrimSpace(focused.EpisodeID) != "" {
			episode = "第" + strings.TrimSpace(focused.EpisodeID) + "集"
		}
		return prefix + "只修改" + episode + "当前选中的内容，其他剧本保持不变；完成后生成新版本。"
	}
	return prefix + "只修改当前选中的内容，其他内容保持不变；完成后生成新版本。"
}

func recentSelectedRevisionRequest(input Context) string {
	if input.Conversation == nil {
		return ""
	}
	turns := input.Conversation.RecentTurns
	for index := len(turns) - 1; index >= 0; index-- {
		turn := turns[index]
		if turn.Role == "agent" && selectionConsumingIntent(Intent(strings.TrimSpace(turn.Intent))) {
			return ""
		}
		if turn.Role == "user" && len(turn.SelectionContext) > 0 && revisionRequestText(turn.Content) {
			return strings.TrimSpace(turn.Content)
		}
	}
	return ""
}

func failedRevisionCanBeReplaced(input Context) bool {
	if input.Run == nil || input.Run.Status != agent.RunFailed || input.FocusedContext == nil || input.Run.Metadata == nil {
		return false
	}
	active, _ := input.Run.Metadata["active_revision"].(map[string]any)
	return len(active) > 0 && strings.TrimSpace(input.FocusedContext.ArtifactID) != "" && revisionRequestText(input.Request.Message)
}

func revisionInquiryText(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	return strings.HasSuffix(normalized, "?") || strings.HasSuffix(normalized, "？") || containsAny(normalized,
		"修改了吗", "改了吗", "是否修改", "有没有修改", "修改结果", "修改完成", "为什么修改", "怎么修改",
		"怎么改", "如何改", "如何修改", "怎样改", "该怎么", "你觉得", "建议怎么", "能不能改", "可以改吗",
	)
}

func revisionAdviceRequest(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	return containsAny(normalized,
		"怎么改", "如何改", "如何修改", "怎样改", "该怎么", "你觉得", "建议怎么", "给个建议", "先分析", "先看看", "先说说",
	)
}

func explicitNoRevisionRequest(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	return containsAny(normalized,
		"不要修改", "不用修改", "不需要修改", "先不修改", "暂时不修改", "不要改", "不用改", "先别改", "暂时别改",
		"只分析", "只解释", "只看看", "先回答", "别执行", "不要执行", "do not revise", "don't revise", "do not change",
	)
}

func checkpointRevisionDecision(decision Decision, input Context) Decision {
	revisionInput := input
	continuation := isRevisionContinuation(decision, input)
	if continuation && revisionInput.FocusedContext == nil {
		revisionInput.FocusedContext = recentFocusedContext(input)
	}
	if revisionInput.FocusedContext == nil && revisionRequestText(input.Request.Message) {
		revisionInput.FocusedContext = recentFocusedContext(input)
	}
	sourceMode := input.Run.SourceMode
	if sourceMode != agent.SourceModeNovel && sourceMode != agent.SourceModeNonNovel {
		sourceMode = effectiveSourceMode(decision, input)
	}
	target := strings.TrimSpace(decision.TargetArtifact)
	if target == "" && input.CurrentStepContext != nil {
		target = strings.TrimSpace(input.CurrentStepContext.ArtifactID)
	}
	if target == "" && input.Run != nil {
		target = strings.TrimSpace(input.Run.CurrentStepID)
	}
	confidence := decision.Confidence
	if confidence < 0.65 {
		confidence = 0.65
	}
	reply := "我会按你的要求修订当前待确认产物，生成新版后再让你确认。"
	reason := "forced_checkpoint_revision_from_waiting_approval_context"
	if continuation {
		reason = "forced_revision_from_conversation_continuation"
		if strings.TrimSpace(decision.AgentReply) != "" {
			reply = decision.AgentReply
		}
	}
	revisionTarget := defaultRevisionTarget(decision, revisionInput)
	revisionIntent := normalizeRevisionIntentForResolvedTarget(decision.RevisionIntent, revisionInput, revisionTarget)
	return Decision{
		Intent:           IntentReviseCheckpoint,
		Confidence:       confidence,
		NextAction:       ActionReviseCheckpoint,
		SourceMode:       sourceMode,
		AgentReply:       reply,
		RequiresApproval: false,
		Reason:           reason,
		TargetArtifact:   target,
		RevisionIntent:   revisionIntent,
		Runtime:          decision.Runtime,
		Warning:          decision.Warning,
		RevisionTarget:   revisionTarget,
	}
}

func isRevisionContinuation(decision Decision, input Context) bool {
	if input.FocusedContext != nil || decision.Intent == IntentExplainState {
		return false
	}
	message := strings.TrimSpace(input.Request.Message)
	if !revisionConfirmationText(message) {
		return false
	}
	if !hasPendingRevisionClarification(input) {
		return false
	}
	return recentFocusedContext(input) != nil
}

func revisionConfirmationText(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	normalized = strings.Trim(normalized, "。.!！?？,， ")
	switch normalized {
	case "ok", "okay", "好", "好的", "可以", "确认", "确定", "是", "对", "继续", "就这样", "按这个改", "按你说的改":
		return true
	default:
		return false
	}
}

func hasPendingRevisionClarification(input Context) bool {
	if input.Conversation == nil {
		return false
	}
	for index := len(input.Conversation.RecentTurns) - 1; index >= 0; index-- {
		turn := input.Conversation.RecentTurns[index]
		if turn.Role != "agent" {
			continue
		}
		if Intent(strings.TrimSpace(turn.Intent)) != IntentExplainState {
			return false
		}
		content := strings.TrimSpace(turn.Content)
		return revisionRequestText(content) && (agentReplyAsksQuestion(content) || containsAny(content, "确认", "是否", "要不要", "需要我"))
	}
	return false
}

func agentReplyAsksQuestion(reply string) bool {
	quoteDepth := 0
	for _, current := range strings.TrimSpace(reply) {
		switch current {
		case '「', '『', '“', '‘', '《', '〈':
			quoteDepth++
		case '」', '』', '”', '’', '》', '〉':
			if quoteDepth > 0 {
				quoteDepth--
			}
		case '?', '？':
			if quoteDepth == 0 {
				return true
			}
		}
	}
	return false
}

func recentFocusedContext(input Context) *FocusedContext {
	if input.Conversation == nil {
		return nil
	}
	for index := len(input.Conversation.RecentTurns) - 1; index >= 0; index-- {
		turn := input.Conversation.RecentTurns[index]
		if turn.Role == "agent" && selectionConsumingIntent(Intent(strings.TrimSpace(turn.Intent))) {
			return nil
		}
		selection := turn.SelectionContext
		artifactID := contextString(selection, "artifact_id", "selected_artifact_id", "artifactId")
		selectedText := contextString(selection, "selected_text", "selection_text", "selectedText", "text")
		if artifactID == "" || selectedText == "" {
			continue
		}
		return &FocusedContext{
			ArtifactID:      artifactID,
			ArtifactType:    contextString(selection, "artifact_type", "artifactType"),
			FieldPath:       contextString(selection, "field_path", "fieldPath"),
			EpisodeID:       contextString(selection, "episode_id", "episodeId"),
			SceneID:         contextString(selection, "scene_id", "sceneId"),
			NodeID:          contextString(selection, "node_id", "nodeId", "line_id", "lineId"),
			StartSceneID:    contextString(selection, "start_scene_id", "startSceneId"),
			EndSceneID:      contextString(selection, "end_scene_id", "endSceneId"),
			StartLineID:     contextString(selection, "start_line_id", "startLineId"),
			EndLineID:       contextString(selection, "end_line_id", "endLineId"),
			LineIDs:         contextStringSlice(selection, "line_ids", "lineIds"),
			SelectionScope:  contextString(selection, "selection_scope", "selectionScope"),
			SelectedText:    selectedText,
			BeforeText:      contextString(selection, "before_context", "before_text", "beforeText"),
			AfterText:       contextString(selection, "after_context", "after_text", "afterText"),
			SelectionSource: "recent_conversation_selection",
		}
	}
	return nil
}

func selectionConsumingIntent(intent Intent) bool {
	switch intent {
	case IntentReviseCheckpoint, IntentApproveCheckpoint, IntentGenerateFromNovel, IntentGenerateFromMaterial:
		return true
	default:
		return false
	}
}

func contextString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func contextStringSlice(values map[string]any, keys ...string) []string {
	for _, key := range keys {
		switch items := values[key].(type) {
		case []string:
			return append([]string(nil), items...)
		case []any:
			out := make([]string, 0, len(items))
			for _, item := range items {
				if text := strings.TrimSpace(fmt.Sprint(item)); text != "" && text != "<nil>" {
					out = append(out, text)
				}
			}
			return out
		}
	}
	return nil
}

func normalizeRevisionIntent(intent RevisionIntent, input Context) RevisionIntent {
	targetType := ""
	if input.FocusedContext != nil {
		targetType = input.FocusedContext.ArtifactType
	}
	if targetType == "" && input.CurrentStepContext != nil {
		targetType = input.CurrentStepContext.ArtifactType
	}
	return normalizeRevisionIntentForTarget(intent, input, targetType)
}

func normalizeRevisionIntentForTarget(intent RevisionIntent, input Context, targetArtifactType string) RevisionIntent {
	if targetArtifactType != "script_unit" && targetArtifactType != "scripts" {
		switch intent {
		case RevisionIntentPatchScriptSpan:
			return RevisionIntentPatchArtifactSection
		case RevisionIntentRegenerateScriptScene,
			RevisionIntentRegenerateScriptEpisode,
			RevisionIntentRegenerateScriptRange:
			return RevisionIntentRegenerateArtifact
		}
	}
	switch intent {
	case RevisionIntentExplainOrLocate,
		RevisionIntentClarifyRevisionTarget,
		RevisionIntentAddRequirement,
		RevisionIntentPatchArtifactField,
		RevisionIntentPatchArtifactEntity,
		RevisionIntentPatchArtifactCollection,
		RevisionIntentPatchArtifactSection,
		RevisionIntentRegenerateArtifact,
		RevisionIntentPatchScriptSpan,
		RevisionIntentRegenerateScriptScene,
		RevisionIntentRegenerateScriptEpisode,
		RevisionIntentRegenerateScriptRange,
		RevisionIntentRerunFailedTask,
		RevisionIntentResumeAfterRevision,
		RevisionIntentReplaceSourceInput:
		return intent
	}
	if input.FocusedContext != nil && strings.TrimSpace(input.FocusedContext.SelectedText) != "" {
		if strings.TrimSpace(input.FocusedContext.FieldPath) != "" && targetArtifactType != "script_unit" && targetArtifactType != "scripts" {
			return RevisionIntentPatchArtifactField
		}
		if input.FocusedContext.ArtifactType == "script_unit" || input.FocusedContext.ArtifactType == "scripts" {
			return RevisionIntentPatchScriptSpan
		}
		return RevisionIntentPatchArtifactSection
	}
	if input.CurrentStepContext != nil && input.CurrentStepContext.ArtifactType == "script_unit" {
		return RevisionIntentRegenerateScriptEpisode
	}
	return RevisionIntentPatchArtifactSection
}

func defaultRevisionTarget(decision Decision, input Context) *RevisionTarget {
	target := &RevisionTarget{}
	if decision.RevisionTarget != nil {
		*target = *decision.RevisionTarget
	}
	if target.ArtifactType == "" && input.FocusedContext != nil {
		target.ArtifactType = input.FocusedContext.ArtifactType
	}
	if target.ArtifactType == "" && input.CurrentStepContext != nil {
		target.ArtifactType = input.CurrentStepContext.ArtifactType
	}
	if target.ArtifactType == "" {
		target.ArtifactType = strings.TrimSpace(decision.TargetArtifact)
	}
	if input.FocusedContext != nil {
		focusedMatchesTarget := target.ArtifactType == "" || input.FocusedContext.ArtifactType == "" || target.ArtifactType == input.FocusedContext.ArtifactType
		if target.ArtifactID == "" && focusedMatchesTarget {
			target.ArtifactID = input.FocusedContext.ArtifactID
		}
		if target.FieldPath == "" && focusedMatchesTarget {
			target.FieldPath = input.FocusedContext.FieldPath
		}
		if target.EpisodeID == "" && focusedMatchesTarget {
			target.EpisodeID = input.FocusedContext.EpisodeID
		}
		if target.SceneID == "" && focusedMatchesTarget {
			target.SceneID = input.FocusedContext.SceneID
		}
		if target.NodeID == "" && focusedMatchesTarget {
			target.NodeID = input.FocusedContext.NodeID
		}
	}
	if input.CurrentStepContext != nil {
		currentMatchesTarget := target.ArtifactType == "" || input.CurrentStepContext.ArtifactType == "" || target.ArtifactType == input.CurrentStepContext.ArtifactType
		if target.ArtifactID == "" && currentMatchesTarget {
			target.ArtifactID = input.CurrentStepContext.ArtifactID
		}
	}
	if structuralCollectionRequest(input.Request.Message) && target.FieldPath == "" && target.EpisodeID != "" && artifactHasEpisodeCollection(target.ArtifactType) {
		target.FieldPath = "episodes"
	}
	normalizedIntent := normalizeRevisionIntentForResolvedTarget(decision.RevisionIntent, input, target)
	switch normalizedIntent {
	case RevisionIntentPatchArtifactField:
		target.Scope = "field"
	case RevisionIntentPatchArtifactEntity:
		target.Scope = "entity"
	case RevisionIntentPatchArtifactCollection:
		target.Scope = "collection"
	case RevisionIntentPatchScriptSpan:
		target.Scope = "selection"
	}
	if target.Scope == "" {
		switch normalizedIntent {
		case RevisionIntentRegenerateArtifact:
			if target.EpisodeID != "" {
				target.Scope = "episode"
			} else {
				target.Scope = "artifact"
			}
		case RevisionIntentRegenerateScriptEpisode:
			target.Scope = "episode"
		default:
			target.Scope = "section"
		}
	}
	return target
}

func normalizeRevisionIntentForResolvedTarget(intent RevisionIntent, input Context, target *RevisionTarget) RevisionIntent {
	if target == nil {
		return normalizeRevisionIntent(intent, input)
	}
	if target.ArtifactType != "script_unit" && target.ArtifactType != "scripts" && target.EpisodeID != "" {
		switch intent {
		case RevisionIntentRegenerateScriptEpisode, RevisionIntentRegenerateScriptScene:
			return RevisionIntentPatchArtifactEntity
		}
	}
	normalizedIntent := normalizeRevisionIntentForTarget(intent, input, target.ArtifactType)
	if target.ArtifactType != "script_unit" && target.ArtifactType != "scripts" && structuralCollectionRequest(input.Request.Message) {
		if target.FieldPath != "" || target.EpisodeID != "" {
			return RevisionIntentPatchArtifactCollection
		}
	}
	explicitScope := strings.TrimSpace(target.Scope)
	if normalizedIntent == RevisionIntentPatchArtifactEntity && explicitScope == "entity" {
		return normalizedIntent
	}
	if (normalizedIntent == RevisionIntentPatchArtifactEntity || normalizedIntent == RevisionIntentPatchArtifactSection) && scalarFieldPath(target.FieldPath) {
		return RevisionIntentPatchArtifactField
	}
	return normalizedIntent
}

func structuralCollectionRequest(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if containsAny(normalized,
		"拆成", "拆分", "合并", "插入", "调换顺序", "调整顺序", "重新排序", "前移", "后移",
		"split into", "split", "merge", "insert", "reorder", "move before", "move after",
	) {
		return true
	}
	if !containsAny(normalized, "新增", "增加", "添加", "删除", "移除", "append", "delete", "remove") {
		return false
	}
	return containsAny(normalized,
		"一集", "一话", "一章", "一个人物", "一名人物", "一个角色", "一名角色", "一个阶段", "一条素材", "一个事件", "一条事件", "一项", "一条", "列表项", "整个人物", "整个角色", "整集", "整章",
		"episode", "chapter", "character", "list item", "phase", "material item", "event item",
	)
}

func artifactHasEpisodeCollection(artifactType string) bool {
	switch strings.TrimSpace(artifactType) {
	case "episode_split", "episode_cards", "scripts":
		return true
	default:
		return false
	}
}

func scalarFieldPath(fieldPath string) bool {
	leaf := strings.TrimSpace(fieldPathLeaf(fieldPath))
	if leaf == "" {
		return false
	}
	switch leaf {
	case "goal", "name", "role", "relationship_position", "speech_profile", "base_style", "regional_speech",
		"usage_boundary", "summary", "hook", "main_conflict", "opening_pressure", "ending_hook", "risk_type",
		"risk_note", "reason", "impact", "title", "premise", "theme", "tone", "duration_minutes":
		return true
	}
	return false
}

func fieldPathLeaf(fieldPath string) string {
	fieldPath = strings.TrimSpace(fieldPath)
	if fieldPath == "" {
		return ""
	}
	if index := strings.LastIndex(fieldPath, "."); index >= 0 && index < len(fieldPath)-1 {
		return strings.TrimSpace(fieldPath[index+1:])
	}
	if index := strings.LastIndex(fieldPath, "]"); index >= 0 && index < len(fieldPath)-1 {
		return strings.Trim(strings.TrimSpace(fieldPath[index+1:]), ".")
	}
	if strings.Contains(fieldPath, "[") {
		return ""
	}
	return fieldPath
}

func revisionRequestText(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	return containsAny(normalized,
		"修改", "调整", "改成", "改为", "改得", "改一下", "重写", "重新写",
		"改呀", "改啊", "改吧", "改掉", "赶紧改", "你倒是改",
		"优化", "补充", "替换", "删除", "删掉", "加上", "加一", "增加", "增大", "提升", "减少",
		"更清晰", "更明确", "更强", "更弱", "更丰富", "丰富一些", "情绪增加", "情绪夸大", "动作夸大", "夸大一些",
		"变大", "变小", "放大", "缩小", "加强", "强化", "弱化", "不对", "不符合", "不满意",
		"revise", "revision", "rewrite", "update", "change", "replace", "remove", "add", "improve", "regenerate",
	)
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func shouldForceGenerationConfigPrompt(decision Decision, input Context) bool {
	if decision.NextAction != ActionReply {
		return false
	}
	if input.Run != nil {
		return false
	}
	sourceMode := effectiveSourceMode(decision, input)
	if sourceMode != agent.SourceModeNovel && sourceMode != agent.SourceModeNonNovel {
		return false
	}
	if hasSufficientGenerationConfig(decision.GenerationConfig) || hasSufficientGenerationConfig(input.Request.GenerationConfig) {
		return false
	}
	if decision.RequiresGenerationConfig {
		return true
	}
	if decision.Intent == IntentGenerateFromNovel || decision.Intent == IntentGenerateFromMaterial {
		return true
	}
	if generationRequestText(input.Request.Message) {
		return true
	}
	if configQuestionReply(decision.AgentReply) && generationRequestText(input.Request.Message) {
		return true
	}
	return false
}

func effectiveSourceMode(decision Decision, input Context) agent.SourceMode {
	if input.Request.SourceMode == agent.SourceModeNovel || input.Request.SourceMode == agent.SourceModeNonNovel {
		return input.Request.SourceMode
	}
	if input.Project != nil && (input.Project.SourceMode == agent.SourceModeNovel || input.Project.SourceMode == agent.SourceModeNonNovel) {
		return input.Project.SourceMode
	}
	if decision.SourceMode == agent.SourceModeNovel || decision.SourceMode == agent.SourceModeNonNovel {
		return decision.SourceMode
	}
	if inferred := sourceModeFromRecentGenerationIntent(input); inferred == agent.SourceModeNovel || inferred == agent.SourceModeNonNovel {
		return inferred
	}
	return agent.SourceModeAuto
}

func sourceModeFromRecentGenerationIntent(input Context) agent.SourceMode {
	if input.Conversation == nil {
		return agent.SourceModeAuto
	}
	for index := len(input.Conversation.RecentTurns) - 1; index >= 0; index-- {
		switch Intent(strings.TrimSpace(input.Conversation.RecentTurns[index].Intent)) {
		case IntentGenerateFromNovel:
			return agent.SourceModeNovel
		case IntentGenerateFromMaterial:
			return agent.SourceModeNonNovel
		}
	}
	return agent.SourceModeAuto
}

func configQuestionReply(reply string) bool {
	normalized := strings.ToLower(strings.TrimSpace(reply))
	if normalized == "" {
		return false
	}
	hasEpisode := strings.Contains(normalized, "集") || strings.Contains(normalized, "episode")
	hasDuration := strings.Contains(normalized, "分钟") || strings.Contains(normalized, "时长") || strings.Contains(normalized, "多久") || strings.Contains(normalized, "minute")
	return hasEpisode && hasDuration
}

func generationRequestText(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	if explicitlyRejectsGeneration(normalized) {
		return false
	}
	hasCreate := strings.Contains(normalized, "生成") ||
		strings.Contains(normalized, "改编") ||
		strings.Contains(normalized, "写成") ||
		strings.Contains(normalized, "帮我写") ||
		strings.Contains(normalized, "替我写") ||
		strings.Contains(normalized, "转成") ||
		strings.Contains(normalized, "制作成") ||
		strings.Contains(normalized, "generate") ||
		strings.Contains(normalized, "adapt") ||
		strings.Contains(normalized, "convert to") ||
		strings.Contains(normalized, "write a script") ||
		strings.Contains(normalized, "write the script")
	return hasCreate
}

func explicitlyRejectsGeneration(message string) bool {
	return containsAny(message,
		"不要开始生成", "不要生成", "不要启动", "先不生成", "暂不生成", "暂时不生成", "别生成", "不用生成", "无需生成",
		"不需要生成", "不想生成", "并非要生成", "不是要生成", "不是生成请求", "没有生成需求",
		"不要改编", "先不改编", "别改编", "不需要改编", "不想改编", "只介绍", "仅介绍", "只解释", "仅解释",
		"do not generate", "don't generate", "dont generate", "do not start", "don't start", "just explain", "only explain",
	)
}

func generationConfigMissingReply(sourceMode agent.SourceMode) string {
	if sourceMode == agent.SourceModeNovel {
		return "我理解你要开始小说改编，但开始前还需要确认目标集数和单集时长。请补充例如：生成 2 集，每集 1.5 分钟。"
	}
	if sourceMode == agent.SourceModeNonNovel {
		return "我理解你要从素材生成短剧，但开始前还需要确认目标集数和单集时长。请补充例如：生成 2 集，每集 1.5 分钟。"
	}
	return "我理解你要开始生成，但开始前还需要确认素材类型、目标集数和单集时长。"
}

func guardedReply(decision Decision, input Context, message string) Decision {
	sourceMode := normalizedSourceMode(input.Request.SourceMode)
	if input.Run != nil {
		sourceMode = input.Run.SourceMode
	}
	return Decision{
		Intent:           IntentChatIdle,
		Confidence:       minConfidence(decision.Confidence),
		NextAction:       ActionReply,
		SourceMode:       sourceMode,
		AgentReply:       message,
		RequiresApproval: false,
		Reason:           "guarded_invalid_action:" + string(decision.NextAction),
		TargetArtifact:   decision.TargetArtifact,
		Runtime:          decision.Runtime,
		Warning:          decision.Warning,
	}
}

func generationConfigRequiredReply(decision Decision, input Context, message string) Decision {
	reply := guardedReply(decision, input, message)
	reply.RequiresGenerationConfig = true
	reply.Intent = decision.Intent
	if reply.Intent == "" || reply.Intent == IntentChatIdle {
		if decision.SourceMode == agent.SourceModeNovel {
			reply.Intent = IntentGenerateFromNovel
		} else {
			reply.Intent = IntentGenerateFromMaterial
		}
	}
	if decision.SourceMode == agent.SourceModeNovel || decision.SourceMode == agent.SourceModeNonNovel {
		reply.SourceMode = decision.SourceMode
	}
	reply.GenerationConfig = decision.GenerationConfig
	reply.Reason = "generation_config_required"
	return reply
}

func fallbackDecision(input Context) Decision {
	sourceMode := normalizedSourceMode(input.Request.SourceMode)
	if input.Run != nil {
		sourceMode = input.Run.SourceMode
	}
	return Decision{
		Intent:           IntentChatIdle,
		Confidence:       0.45,
		NextAction:       ActionReply,
		SourceMode:       sourceMode,
		AgentReply:       fallbackReply(input),
		RequiresApproval: false,
		Reason:           "model_unavailable_or_failed_no_semantic_routing",
		Runtime:          "fallback",
	}
}

func fallbackReply(input Context) string {
	if input.Run != nil && input.Run.Status == agent.RunFailed {
		return "控制模型暂时不可用，所以我不能可靠判断你是要查看失败原因、补充要求，还是从失败任务继续重跑。请稍后重试，或使用明确的重试按钮。"
	}
	if input.Run != nil && (input.Run.Status == agent.RunWaitingApproval || input.Run.Status == agent.RunPaused) {
		return "控制模型暂时不可用，所以我不能可靠判断你是要确认继续、暂停，还是修改当前确认点。请稍后重试，或使用当前确认卡片上的操作。"
	}
	return "我在，但控制模型暂时不可用，所以不会仅凭本地关键词启动生成流程。请稍后重试。"
}

func failedStepTarget(run *agent.Run) string {
	if run == nil {
		return ""
	}
	if stepID, ok := run.NextAction["step_id"].(string); ok && strings.TrimSpace(stepID) != "" {
		return stepID
	}
	if run.Metadata != nil {
		if task, ok := run.Metadata["last_failed_task"].(map[string]any); ok {
			if stepID := strings.TrimSpace(fmt.Sprint(task["step_id"])); stepID != "" && stepID != "<nil>" {
				return stepID
			}
		}
	}
	return run.CurrentStepID
}

func intentForAction(action NextAction) Intent {
	switch action {
	case ActionStartRun:
		return IntentGenerateFromMaterial
	case ActionApproveRun:
		return IntentApproveCheckpoint
	case ActionPauseRun:
		return IntentPauseRun
	case ActionResumeRun:
		return IntentResumeRun
	case ActionReviseCheckpoint:
		return IntentReviseCheckpoint
	case ActionInspectArtifact:
		return IntentInspectArtifact
	case ActionInspectSource:
		return IntentInspectSource
	case ActionRerunStep:
		return IntentRerunStep
	case ActionUnsupported:
		return IntentUnsupported
	default:
		return IntentChatIdle
	}
}

func defaultReplyForDecision(decision Decision, input Context) string {
	switch decision.NextAction {
	case ActionStartRun:
		return "我会启动剧本生成流程，并在每个关键产物完成后先停下来让你确认。"
	case ActionApproveRun:
		return "收到确认，我会继续执行下一步。"
	case ActionPauseRun:
		return "已暂停当前流程。"
	case ActionResumeRun:
		return "已恢复当前流程，将从暂停的任务继续。"
	case ActionReviseCheckpoint:
		return "我会先把你的补充要求作用到当前确认点，不会直接跳过确认继续生成。"
	case ActionRerunStep:
		return "我会从失败的具体任务继续重跑，成功后再接着跑后续任务。"
	case ActionInspectArtifact:
		return "我会查看当前产物和运行状态。"
	case ActionInspectSource:
		return "我会读取你指向的作品附件并按要求分析。"
	case ActionUnsupported:
		return "这个请求暂时超出当前能力范围。"
	default:
		if strings.TrimSpace(input.Request.Message) == "" {
			return "我在。你可以补充需求，或发送小说原文、梗概、短剧灵感。"
		}
		return "收到。"
	}
}

func decisionSystemPrompt() string {
	return `你是 Novel2Script Main Agent，负责理解用户输入并输出下一步控制决策。你不是普通聊天机器人，也不是剧本文本生成器。

一、Agent 定义
- 名称：Novel2Script Main Agent。
- 角色：用户入口、流程总控、状态管理者、任务调度者、checkpoint 守门人。
- 目标：把用户的自然语言、附件、选区和当前运行上下文，转成安全、可恢复、可解释的剧本生产动作。
- 边界：不直接创作剧本正文；不绕过 prompt / rule / artifact schema；不在关键产物未确认时继续下游生成；不在失败后只口头承诺重跑而不触发 runtime 动作。

二、最重要原则
- 用户表达是开放集合，禁止把中文或英文短语枚举成意图词典。
- 你必须根据上下文整体理解用户意图：用户消息、当前附件、project.files 文件索引、选区、当前 run、active_task、approval、event_digest、artifacts 都要一起看。
- 代码里的 next_action 是有限动作合同；用户自然语言不是有限集合。
- 如果意图不清楚，选择 reply 并追问，不要擅自启动流程或改写产物。

三、每轮决策循环
1. Observe：读取 request、attachments、selection、run、event_digest、artifacts、run.metadata.active_task、run.next_action。
2. Decide：判断用户真实目的、source_mode、当前状态允许的最小下一步动作。
3. Plan：如果需要执行，选择一个 next_action；不要一次承诺多个不可恢复动作。
4. Act：只输出控制 JSON，由后端 runtime 执行。
5. Checkpoint：关键产物完成后必须停下等待用户确认或修改。

四、上下文判断规则
- 普通问候、闲聊、问产品怎么用、问当前状态，只回复，不启动 run。
- 上传文件、粘贴原文或发送素材本身，不等于生成请求；只有用户语义上要求“生成 / 改编 / 写成 / 拆成 / 转成 / 继续完成某类产物”时，才可 start_run。
- project.files 是当前作品长期保留的附件索引，不代表本轮必须读取正文。用户明确要求分析、总结、解释、评价某个附件，或用“这个 / 刚才的文件”等方式明确指向附件时，输出 inspect_source。
- 只有一个作品附件时，“这个文件 / 这个小说 / 刚才发的”默认指向该附件；有多个附件且无法唯一定位时用 reply 追问，不读取正文。
- 不要把分析附件输出成 inspect_artifact；inspect_artifact 只用于已生成 artifact 和运行记录。
- source_mode 明确由用户选择时优先尊重；自动识别时，根据附件和文本内容判断小说原文还是非小说素材。不能判断时追问。
- waiting_approval 状态下，用户可能是在确认继续、要求暂停、询问状态、或提出修改。必须结合当前 approval 和用户语义判断；不能把任意短回复都当确认。
- 普通问候在任何状态下都只能 reply；不得因为历史选区、旧修改记录或 agent_reply 里出现“修改”而触发 revise_checkpoint。
- 历史选区只有在上一轮明确向用户追问修改方向、当前轮是对该追问的肯定确认时才能继承；已经成功修改、确认继续或启动新任务的选区视为已消费。
- 用户只说“修改这句话 / 修改这里”但没有说明改成什么方向时，即使已定位选区也必须 reply 追问，不得自行推测修改目标。
- 当前存在 pending approval 时，该确认点优先。只允许修改同一待确认产物；若用户要求修改其他产物，先提示完成当前确认，不能替换或取消原确认卡。
- paused 状态下，用户补充说明不等于继续执行；只有语义上要求继续执行时才继续。
- failed 状态下，用户可能是问失败原因、要求重试、补充要求后重试、或质疑为什么没真正重跑。询问、质疑、确认是否失败只能 reply；只有用户明确要求继续、重试或重新执行失败部分时才能输出 rerun_step。
- rerun_step 必须指向失败的具体任务所在 step。runtime 会根据 active_task 从失败任务继续，而不是从整个 step 开头重跑。
- 局部修改必须依赖 selected_artifact_id、selected_text、当前 artifact 或明确上下文；没有定位时先追问。
- 对 artifact 的修改必须判断修改粒度：局部字段、局部段落、整份产物重写、剧本选区、剧本单场、剧本单集、失败任务续跑。不要把所有修改都等同为整步重跑。
- 如果用户要求修改等待确认、暂停或已完成流程中的现有产物，输出 revise_checkpoint，并给出 revision_intent 与 revision_target。revision_target 要尽量包含 artifact_type、field_path、episode_id、scene_id、scope。
- 修改上游产物时，已有下游内容默认保留。agent_reply 不得承诺“下游会同步更新”；完成修改后，如确有下游内容，再让用户选择保留或重新生成受影响内容。
- agent_reply 只能使用用户能理解的产物名称和范围，不得暴露 artifact_id、run_id、step_id、field_path 或内部模型名。
- 如果用户只是问“哪里不对 / 当前是什么 / 为什么失败”，不要改写产物，输出 reply 或 inspect_artifact。
- inspect_artifact 没有后续异步执行器；选择它时必须在本轮 agent_reply 直接给出完整分析或提出一个精确澄清问题。不得回复“稍等”“我来看看”“稍后给你结论”等空承诺。

五、可选 intent
- chat_idle
- generate_from_novel
- generate_from_material
- approve_checkpoint
- pause_run
- resume_run
- continue_with_note
- revise_checkpoint
- inspect_artifact
- inspect_source
- rerun_step
- explain_current_state
- unsupported

六、可选 next_action
- reply
- start_run
- approve_run
- pause_run
- resume_run
- revise_checkpoint
- inspect_artifact
- inspect_source
- rerun_step
- unsupported

七、动作选择要求
- start_run：只在用户语义上明确要求生成/改编/写成剧本或过程产物时使用；source_mode 必须是 novel 或 non_novel。若生成要使用 project.files 中已上传的文件，必须在 target_file_ids 中明确返回对应 file_id；多个文件无法唯一判断时先 reply 追问，不能默认读取全部文件。
- approve_run：只在 run 等待产物确认，或暂停但仍保留确认点，且用户语义上确认产物并继续时使用。
- pause_run：只在用户语义上要求暂停、先别继续、等等时使用。
- resume_run：只在 run 是用户主动暂停的生成任务、当前没有待确认产物，且用户明确要求恢复或继续执行时使用。
- revise_checkpoint：run 等待确认、暂停或已完成时，用户提出对已有产物的补充、调整或修改要求。
- inspect_artifact：用户要求查看、解释、定位当前产物或运行记录。agent_reply 必须当场完成查看或解释，不会再有第二次模型调用替你补充结果。
- inspect_source：用户要求分析、总结、解释或评价已上传附件。尽量在 target_file_ids 中返回 project.files 里的 file_id；无法唯一定位多个附件时先 reply 追问。
- rerun_step：run failed 且用户明确命令继续、重试或重新执行失败部分。不得从“又失败了？”“为什么失败？”等疑问句推断执行意图。
- reply：闲聊、解释状态、追问澄清、模型不确定、动作不被当前状态允许。
- unsupported：当前系统无法完成且不能安全降级。

八、输出要求
只输出一个 JSON 对象，不要 Markdown，不要解释推理过程：
{
  "intent": "chat_idle",
  "confidence": 0.0,
  "next_action": "reply",
  "source_mode": "auto",
  "agent_reply": "给用户看的简短回复",
  "requires_approval": false,
  "requires_generation_config": false,
  "reason": "内部短原因",
  "target_artifact": "",
  "target_file_ids": [],
  "revision_intent": "",
  "revision_target": {
    "artifact_id": "",
    "artifact_type": "",
    "field_path": "",
    "episode_id": "",
    "scene_id": "",
    "node_id": "",
    "scope": ""
  },
  "generation_config": {
    "target_episode_count": 0,
    "episode_duration_minutes": 0,
    "target_script_chars": 0,
    "boundary_detection_window_chars": 800,
    "preserve_existing_episode_marks": false,
    "existing_episode_markers_detected": false
  }
}`
}

func extractJSONObject(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSuffix(trimmed, "```")
		trimmed = strings.TrimSpace(trimmed)
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		return trimmed[start : end+1]
	}
	return trimmed
}

func normalizedSourceMode(mode agent.SourceMode) agent.SourceMode {
	if mode == agent.SourceModeNovel || mode == agent.SourceModeNonNovel {
		return mode
	}
	return agent.SourceModeAuto
}

func truncateRunes(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "\n...[truncated]"
}

func minConfidence(value float64) float64 {
	if value <= 0 || value > 1 {
		return 0.4
	}
	if value > 0.55 {
		return 0.55
	}
	return value
}

func maxConfidence(value float64, minimum float64) float64 {
	if value < minimum {
		return minimum
	}
	return value
}

func summarizeError(err error) string {
	if err == nil {
		return ""
	}
	var httpErr *llm.HTTPError
	if errors.As(err, &httpErr) && isRateLimitText(httpErr.Body) {
		return strings.TrimSpace("rate_limit " + rateLimitHeaderSummary(httpErr.Headers))
	}
	text := err.Error()
	lower := strings.ToLower(text)
	if strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "deadline exceeded") {
		return "timeout"
	}
	for _, marker := range []string{"rate limit", "rate_limit", "too many requests", "model_not_found", "unauthorized", "invalid_api_key", "timeout"} {
		if strings.Contains(lower, marker) {
			if marker == "rate limit" || marker == "too many requests" {
				return "rate_limit"
			}
			return marker
		}
	}
	runes := []rune(text)
	if len(runes) > 360 {
		return string(runes[:360])
	}
	return text
}

func controlModelFailureReply(summary string) string {
	switch summary {
	case "rate_limit":
		return "控制模型现在被上游限流了，所以我暂时不能做语义意图判断，也不会用本地关键词硬启动流程。上游没有返回明确恢复时间，请稍后重试。"
	case "invalid_api_key", "unauthorized":
		return "控制模型鉴权失败，所以我暂时不能做语义意图判断。请检查模型 key 或服务配置。"
	case "timeout":
		return "控制模型请求超时，所以我暂时不能做语义意图判断。请稍后重试。"
	default:
		if strings.HasPrefix(summary, "rate_limit ") {
			return "控制模型现在被上游限流了，所以我暂时不能做语义意图判断，也不会用本地关键词硬启动流程。限流信息：" + strings.TrimSpace(strings.TrimPrefix(summary, "rate_limit")) + "。"
		}
		return "控制模型暂时不可用，所以我不会仅凭本地关键词启动生成流程。请稍后重试。"
	}
}

func isRateLimitText(text string) bool {
	text = strings.ToLower(text)
	return strings.Contains(text, "rate limit") || strings.Contains(text, "rate_limit") || strings.Contains(text, "too many requests")
}

func rateLimitHeaderSummary(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	parts := []string{}
	for _, key := range []string{"retry-after", "x-ratelimit-reset", "x-ratelimit-reset-requests", "x-ratelimit-reset-tokens", "x-ratelimit-remaining", "x-request-id"} {
		if value := strings.TrimSpace(headers[key]); value != "" {
			parts = append(parts, key+"="+value)
		}
	}
	return strings.Join(parts, " ")
}

func formatDecisionForDebug(decision Decision) string {
	return fmt.Sprintf("intent=%s action=%s mode=%s reason=%s", decision.Intent, decision.NextAction, decision.SourceMode, decision.Reason)
}

func contextPackSystemInstruction() string {
	return `AgentContextPack contract:
- You receive one JSON object, not only the latest user message.
- Read request, project, conversation.recent_turns, run, approval_request, event_digest, artifact_index, current_step_context, focused_context, and upstream_context before deciding.
- If focused_context.selected_text exists, treat it as the target of a local edit or inspection unless the user clearly asks for something else.
- If current_step_context.payload_excerpt exists, use it as the current step content context.
- If run.metadata.active_task, run.metadata.last_failed_task, or run.next_action names a failed task, rerun from that task and continue remaining tasks; a restored approval does not erase the failed revision.
- Uploaded files and pasted source are evidence; they do not start generation unless the user semantically asks to generate, adapt, convert, write, split, continue, regenerate, or revise.
- The user's wording is open-ended. Never classify intent by exact keyword enumeration; infer intent from the whole context.
- For start_run, infer generation_config from request.generation_config first, then from the user's natural-language request. If target_episode_count or episode_duration_minutes is missing, reply and ask for those values instead of starting a run.
- For novel source, preserve existing episode or chapter markers only when the source clearly contains them or the user requests it; otherwise use target_episode_count plus boundary detection.
- For non_novel source, generation_config controls series_blueprint and episode_cards volume; do not silently invent a 20-episode target.
- For revise_checkpoint, fill revision_intent with one of: add_requirement_to_checkpoint, patch_artifact_field, patch_artifact_entity, patch_artifact_collection, patch_artifact_section, regenerate_artifact, patch_script_span, regenerate_script_scene, regenerate_script_episode, regenerate_script_range, rerun_failed_task, resume_after_revision, replace_source_input, clarify_revision_target, explain_or_locate.
- Use patch_artifact_field when the user targets one scalar field, for example "change the protagonist goal".
- Use patch_artifact_entity when the user targets an object/list item, for example "rewrite the protagonist profile" or "change episode 2 card".
- Use patch_artifact_collection when the number or order of list items changes, including split, merge, insert, delete, or move. For episode structures, target field_path=episodes; changing one episode into two is a collection patch, not an entity patch.
- Use patch_artifact_section when the user targets a broader named section but not the whole artifact.
- Use regenerate_* only when the user asks for a full rewrite/regeneration of an artifact, scene, episode, or range.
- revision_target.scope should be selection, field, entity, collection, section, artifact, scene, episode, range, failed_task, or source_input.`
}
