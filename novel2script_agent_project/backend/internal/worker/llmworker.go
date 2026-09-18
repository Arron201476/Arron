package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"novel2script-agent/backend/internal/agent"
	agentruntime "novel2script-agent/backend/internal/agent/runtime"
	"novel2script-agent/backend/internal/llm"
)

type LLMWorker struct {
	client llm.ChatClient
	model  string
}

type SourceDocument struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	Text     string `json:"text"`
}

func NewLLMWorker(client llm.ChatClient, generationModel string) *LLMWorker {
	return &LLMWorker{
		client: client,
		model:  strings.TrimSpace(generationModel),
	}
}

func (w *LLMWorker) Configured() bool {
	return w != nil && w.client != nil && w.client.Configured() && w.model != ""
}

func (w *LLMWorker) AnalyzeSources(ctx context.Context, instruction string, documents []SourceDocument) (string, error) {
	if !w.Configured() {
		return "", errors.New("generation model is not configured")
	}
	if len(documents) == 0 {
		return "", errors.New("no source documents were selected")
	}

	bounded := make([]SourceDocument, 0, len(documents))
	remaining := 120000
	for _, document := range documents {
		if remaining <= 0 {
			break
		}
		limit := 60000
		if remaining < limit {
			limit = remaining
		}
		document.Text = truncateSourceText(document.Text, limit)
		remaining -= len([]rune(document.Text))
		bounded = append(bounded, document)
	}
	if len(bounded) == 0 {
		return "", errors.New("selected source documents contain no readable text")
	}

	payload, err := json.Marshal(map[string]any{
		"user_request": strings.TrimSpace(instruction),
		"files":        bounded,
	})
	if err != nil {
		return "", err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	resp, err := w.client.Complete(reqCtx, llm.ChatRequest{
		Model:        w.model,
		Temperature:  llm.Temperature(0.3),
		TraceContext: map[string]string{"component": "content", "operation": "analyze_sources"},
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "You analyze only the selected source files and answer the user's request directly in Chinese. Do not start a generation run, create artifacts, or write a script unless the user explicitly asks for analysis of an existing script. Do not assume an attachment is a novel; identify its type from its contents. Distinguish source evidence from inference. For a vague evaluation request, cover what the file contains, strengths, weaknesses, and adaptation potential. Do not output JSON unless the user explicitly asks for JSON."},
			{Role: llm.RoleUser, Content: string(payload)},
		},
	})
	if err != nil {
		return "", err
	}
	answer := strings.TrimSpace(resp.Content)
	if answer == "" {
		return "", errors.New("source analysis model returned an empty response")
	}
	return answer, nil
}

func truncateSourceText(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	marker := "\n\n[中间内容因长度限制已省略]\n\n"
	markerLength := len([]rune(marker))
	if limit <= markerLength+2 {
		return string(runes[:limit])
	}
	remaining := limit - markerLength
	head := remaining * 2 / 3
	tail := remaining - head
	return string(runes[:head]) + marker + string(runes[len(runes)-tail:])
}

func (w *LLMWorker) PlanStep(ctx context.Context, run agent.Run, source agent.Artifact, existing []agent.Artifact, artifactType string) (agentruntime.PlannedArtifact, *agent.ApprovalRequest, error) {
	if !w.Configured() {
		return agentruntime.PlannedArtifact{}, nil, errors.New("generation model is not configured")
	}
	assembled := assembleWorkerContext(source.Payload, existing, artifactType, revisionNoteFromSource(source.Payload))
	resp, err := w.completeWithTrace(ctx, planStepPrompt(run.SourceMode, assembled.Source, assembled.Artifacts, artifactType), workerTraceContext(run, "plan_artifact", artifactType, 0))
	if err != nil {
		return agentruntime.PlannedArtifact{}, nil, err
	}
	planned, approval, err := parseSingleArtifact(resp, artifactType)
	if err != nil {
		return agentruntime.PlannedArtifact{}, nil, err
	}
	config, _ := run.Metadata["generation_config"].(map[string]any)
	if err := ValidateArtifactAgainstGenerationConfig(planned.ArtifactType, planned.Payload, config); err != nil {
		return agentruntime.PlannedArtifact{}, nil, err
	}
	return planned, approval, nil
}

func (w *LLMWorker) WriteScriptStep(ctx context.Context, run agent.Run, artifacts []agent.Artifact, artifactType string, note string) (agentruntime.PlannedArtifact, error) {
	if !w.Configured() {
		return agentruntime.PlannedArtifact{}, errors.New("generation model is not configured")
	}
	assembled := assembleWorkerContext(sourcePayloadFromArtifacts(artifacts), artifacts, artifactType, note)
	episodeID := episodeFromNote(note)
	operation := "write_script"
	if artifactType == "script_context" {
		operation = "build_script_context"
	}
	trace := workerTraceContext(run, operation, artifactType, episodeID)
	if attempt := correctionAttemptFromNote(note); attempt > 0 {
		trace["operation"] = "write_script_length_correction"
		trace["correction_attempt"] = strconv.Itoa(attempt)
	}
	resp, err := w.completeWithTrace(ctx, scriptStepPrompt(run.SourceMode, artifactContextWithSource(assembled), artifactType, note), trace)
	if err != nil {
		return agentruntime.PlannedArtifact{}, err
	}
	planned, _, err := parseSingleArtifact(resp, artifactType)
	return planned, err
}

func (w *LLMWorker) PatchArtifactStep(ctx context.Context, run agent.Run, target agent.Artifact, existing []agent.Artifact, artifactType string, instruction string) (agentruntime.PlannedArtifact, error) {
	if !w.Configured() {
		return agentruntime.PlannedArtifact{}, errors.New("generation model is not configured")
	}
	assembled := assembleWorkerContext(sourcePayloadFromArtifacts(existing), existing, artifactType, instruction)
	pack := parseRevisionPatchPack(instruction)
	target.Payload = compactPatchTargetPayload(target.Payload, pack)
	workerInstruction := compactRevisionInstruction(instruction)
	episodeID := episodeNumberText(pack.Target.EpisodeID)
	resp, err := w.completeWithTrace(ctx, patchArtifactStepPrompt(run.SourceMode, target, artifactContextWithSource(assembled), artifactType, workerInstruction), workerTraceContext(run, "patch_artifact", artifactType, episodeID))
	if err != nil {
		return agentruntime.PlannedArtifact{}, err
	}
	return parsePatchArtifact(resp, artifactType, instruction)
}

func (w *LLMWorker) complete(ctx context.Context, prompt string) (string, error) {
	return w.completeWithTrace(ctx, prompt, nil)
}

func (w *LLMWorker) completeWithTrace(ctx context.Context, prompt string, traceContext map[string]string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	resp, err := w.client.Complete(reqCtx, llm.ChatRequest{
		Model:        w.model,
		Temperature:  llm.Temperature(0.4),
		TraceContext: traceContext,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "You are a Novel2Script content worker. Output strict JSON only. Do not output Markdown, comments, or explanation. All user-facing text should be Chinese."},
			{Role: llm.RoleUser, Content: prompt},
		},
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func planStepPrompt(sourceMode agent.SourceMode, sourcePayload map[string]any, existing []map[string]any, artifactType string) string {
	sourceJSON, _ := json.Marshal(sourcePayload)
	existingJSON, _ := json.Marshal(existing)
	schema := artifactSchema(artifactType, sourceMode)
	designReference := designReferenceForArtifact(sourceMode, artifactType)
	return `You are generating one Novel2Script planning artifact.
Output strict JSON only. Do not output markdown.
The JSON must be:
{
  "artifact_type": "` + artifactType + `",
  "status": "pending_approval",
  "payload": ` + schema + `,
  "approval": {"title": "", "reason": ""}
}
Use status "pending_approval" for the artifact being generated.
Use Chinese for user-facing content. Do not invent facts that conflict with the source.
You must follow the project prompt and rules below. If the prompt's original output shape differs, adapt it into the wrapper JSON above and put the real content in payload.
Always copy source_payload.generation_config into payload.generation_config when the schema contains it.
Never invent a default 20-episode target. Use generation_config.target_episode_count if present; otherwise state the missing config in risks.
For novel episode_split, calculate splits from source volume, target_episode_count, and boundary_detection_window_chars. Each episode must include boundary_check with previous_episode_end and next_episode_start evidence.
For non_novel series_blueprint and episode_cards, generation_config controls resolved_episode_count and pacing density.
` + revisionGuidance(revisionNoteFromSource(sourcePayload)) + `

PROJECT PROMPT AND RULES:
` + designReference + `

source_mode: ` + string(sourceMode) + `
requested_artifact_type: ` + artifactType + `
source_payload:
` + string(sourceJSON) + `

existing_artifacts:
` + string(existingJSON)
}

func scriptStepPrompt(sourceMode agent.SourceMode, artifacts []map[string]any, artifactType string, note string) string {
	if artifactType == "script_context" {
		return scriptContextStepPrompt(sourceMode, artifacts, note)
	}
	artifactsJSON, _ := json.Marshal(artifacts)
	noteJSON, _ := json.Marshal(note)
	schema := artifactSchema(artifactType, sourceMode)
	designReference := designReferenceForArtifact(sourceMode, artifactType)
	return `You are generating one Novel2Script script artifact.
Output strict JSON only. Do not output markdown.
The JSON must be:
{
  "artifact_type": "` + artifactType + `",
  "status": "pending_approval",
  "payload": ` + schema + `
}
Follow confirmed episode_cards and do not change the confirmed structure.
You must follow the project script generation prompt and rules below. If the prompt's original output shape differs, adapt it into the wrapper JSON above and put the real content in payload.
Read generation_config from available_artifacts, especially target_script_chars and episode_duration_minutes. Do not write beyond the selected episode card's structure.
Keep payload.script_text between 60% and 150% of generation_config.target_script_chars. If the first draft falls outside that range, compress or expand the same episode before returning JSON.

Hard format rules for script text:
- Never write novel prose paragraphs.
- Use short-drama script format: episode title, scene heading, 人物, then short action/dialogue lines.
- Action lines must start with "△".
- Dialogue must be separated by speaker, for example "林夏：你凭什么替我决定？".
- Put renderable script lines in payload.scenes[].blocks[] and payload.script_text.
- Keep scene headings like "场 1-1 客厅 日 内" or "场 1-1 INT. 客厅 - 日".
- If requested_artifact_type is script_unit and user_note contains "Generate only episode_id=", generate exactly that one episode. Do not generate earlier or later episodes in the same response.
- For script_unit, payload.episode_id must equal the requested episode_id from user_note.
` + revisionGuidance(note) + `

PROJECT PROMPT AND RULES:
` + designReference + `

source_mode: ` + string(sourceMode) + `
requested_artifact_type: ` + artifactType + `
user_note:
` + string(noteJSON) + `

available_artifacts:
` + string(artifactsJSON)
}

func scriptContextStepPrompt(sourceMode agent.SourceMode, artifacts []map[string]any, note string) string {
	artifactsJSON, _ := json.Marshal(artifacts)
	noteJSON, _ := json.Marshal(note)
	designReference := designReferenceForArtifact(sourceMode, "script_context")
	return `You are preparing the internal unified context used by the script-writing step.
Output strict JSON only. Do not output markdown or script prose.
Return exactly one artifact:
{
  "artifact_type": "script_context",
  "status": "confirmed",
  "payload": ` + scriptContextSchema(sourceMode) + `
}

Rules:
- Synthesize only stable facts, constraints, character and relationship state, continuity state, allowed additions, forbidden changes, source references, and user notes.
- Do not write scenes, dialogue, action lines, episode scripts, or a scripts aggregate.
- Preserve the distinction between source facts and model-generated additions.
- For novel mode, keep source references and faithful facts; the runtime supplies each episode's source window separately during script generation.
- For non-novel mode, keep user-provided facts separate from allowed generated additions.
- Copy the authoritative generation_config from available_artifacts.

PROJECT CONTEXT CONTRACT:
` + designReference + `

source_mode: ` + string(sourceMode) + `
user_note:
` + string(noteJSON) + `

available_artifacts:
` + string(artifactsJSON)
}

func patchArtifactStepPrompt(sourceMode agent.SourceMode, target agent.Artifact, existing []map[string]any, artifactType string, instruction string) string {
	targetJSON, _ := json.Marshal(target.Payload)
	existingJSON, _ := json.Marshal(existing)
	instructionJSON, _ := json.Marshal(instruction)
	pack := parseRevisionPatchPack(instruction)
	if pack.RevisionIntent == "patch_script_span" {
		return scriptSpanPatchPrompt(sourceMode, targetJSON, existingJSON, instructionJSON, pack)
	}
	if pack.RevisionIntent == "regenerate_script_scene" {
		return scriptScenePatchPrompt(sourceMode, targetJSON, existingJSON, instructionJSON, pack)
	}
	fieldPath := firstNonEmptyString(pack.Target.FieldPath, pack.FocusedContext.FieldPath)
	scope := firstNonEmptyString(pack.Target.Scope, "field")
	if pack.RevisionIntent == "patch_artifact_collection" {
		return collectionPatchPrompt(sourceMode, artifactType, fieldPath, targetJSON, existingJSON, instructionJSON)
	}
	return `You are applying a targeted Novel2Script artifact revision.
Output strict JSON only. Do not output markdown.
Return only the changed value or changed entity, not the full artifact.
The JSON must be:
{
  "artifact_type": "` + artifactType + `",
  "status": "pending_approval",
  "field_path": "` + fieldPath + `",
  "scope": "` + scope + `",
  "operation": "replace_entity | append_entity | delete_entity",
  "patch_value": "the rewritten value, object, or list item for field_path"
}

Rules:
- Read USER_REVISION_REQUEST and REVISION_CONTEXT_PACK_JSON from user_instruction.
- Return only the requested field, entity, or section value.
- For entity changes, use replace_entity for an existing item, append_entity to add one item to a target list, and delete_entity to remove the indexed target item. Omit operation for field and section patches.
- For delete_entity, keep field_path equal to the indexed target path and return patch_value as null.
- Preserve facts from target_artifact_payload and upstream artifact digest.
- Use Chinese for user-facing content.

source_mode: ` + string(sourceMode) + `
requested_artifact_type: ` + artifactType + `
user_instruction:
` + string(instructionJSON) + `
target_artifact_payload:
` + string(targetJSON) + `
available_artifact_digest:
` + string(existingJSON)
}

func collectionPatchPrompt(sourceMode agent.SourceMode, artifactType string, fieldPath string, targetJSON []byte, existingJSON []byte, instructionJSON []byte) string {
	return `You are applying a structural list revision to a Novel2Script artifact.
Output strict JSON only. Do not return the full artifact.
Return exactly one collection operation:
{
  "artifact_type": "` + artifactType + `",
  "status": "pending_approval",
  "scope": "collection",
  "field_path": "` + fieldPath + `",
  "operation": "replace_range | insert_entities | delete_entities | move_entity",
  "start_index": 0,
  "delete_count": 0,
  "items": [],
  "move_to": 0
}

Rules:
- Use zero-based indexes into the current field_path list.
- replace_range replaces delete_count existing items at start_index with items. A split is one item replaced by multiple items; a merge is multiple items replaced by one item.
- insert_entities inserts items at start_index with delete_count=0.
- delete_entities removes delete_count items at start_index and returns items=[].
- move_entity moves exactly one existing item at start_index to move_to; return items=[].
- Preserve every item outside the affected range. Do not renumber episode_id; the runtime does that deterministically after applying the operation.
- For an episode split, keep all source facts from the original episode across the replacement items without inventing unrelated events.
- Use Chinese for user-facing content.

source_mode: ` + string(sourceMode) + `
requested_artifact_type: ` + artifactType + `
user_instruction:
` + string(instructionJSON) + `
target_artifact_payload:
` + string(targetJSON) + `
available_artifact_digest:
` + string(existingJSON)
}

func scriptSpanPatchPrompt(sourceMode agent.SourceMode, targetJSON []byte, existingJSON []byte, instructionJSON []byte, pack revisionPatchPack) string {
	if len(pack.FocusedContext.LineIDs) > 1 {
		lineIDsJSON, _ := json.Marshal(pack.FocusedContext.LineIDs)
		return `You are applying a targeted Novel2Script multi-line script revision.
Output strict JSON only. Do not output markdown and do not return a full scene, episode, or script.
Return exactly:
{"artifact_type":"script_unit","status":"pending_approval","operation":"patch_script_range","episode_id":"` + pack.Target.EpisodeID + `","start_line_id":"` + pack.FocusedContext.StartLineID + `","end_line_id":"` + pack.FocusedContext.EndLineID + `","old_text":"the exact selected text","replacement_lines":[{"line_id":"line id from focused_context.line_ids","new_text":"replacement for only the selected portion on this line"}],"continuity_note":""}
Rules:
- replacement_lines must contain exactly one entry for every focused_context.line_ids item, in the same order: ` + string(lineIDsJSON) + `.
- Preserve every line_id, speaker, block type, scene, and line count.
- On the first and last line, return only replacement text for the selected portion; surrounding unselected text is preserved by the runtime.
- On middle lines, return the full replacement text for that selected line.
- old_text must equal focused_context.selected_text.
source_mode: ` + string(sourceMode) + `
user_instruction:
` + string(instructionJSON) + `
target_artifact_payload:
` + string(targetJSON) + `
available_artifact_digest:
` + string(existingJSON)
	}
	return `You are applying a targeted Novel2Script script span revision.
Output strict JSON only. Do not output markdown and do not return a full scene, episode, or script.
Return exactly:
{"artifact_type":"script_unit","status":"pending_approval","operation":"patch_script_span","episode_id":"` + pack.Target.EpisodeID + `","scene_id":"` + pack.Target.SceneID + `","node_id":"` + pack.Target.NodeID + `","old_text":"the exact selected text","new_text":"only the replacement text","continuity_note":""}
The old_text must equal focused_context.selected_text. Change only that text. Preserve speaker, block type, facts, and surrounding continuity unless explicitly requested.
source_mode: ` + string(sourceMode) + `
user_instruction:
` + string(instructionJSON) + `
target_artifact_payload:
` + string(targetJSON) + `
available_artifact_digest:
` + string(existingJSON)
}

func scriptScenePatchPrompt(sourceMode agent.SourceMode, targetJSON []byte, existingJSON []byte, instructionJSON []byte, pack revisionPatchPack) string {
	return `You are applying a targeted Novel2Script scene revision.
Output strict JSON only. Do not return a full episode or scripts artifact.
Return exactly:
{"artifact_type":"script_unit","status":"pending_approval","operation":"patch_script_scene","scene_id":"` + pack.Target.SceneID + `","patch_value":{"scene_id":"` + pack.Target.SceneID + `","heading":"","blocks":[]}}
Rewrite only the requested scene and preserve its scene_id. Do not include sibling scenes.
source_mode: ` + string(sourceMode) + `
user_instruction:
` + string(instructionJSON) + `
target_artifact_payload:
` + string(targetJSON) + `
available_artifact_digest:
` + string(existingJSON)
}

func revisionGuidance(note string) string {
	if !strings.Contains(note, "REVISION_CONTEXT_PACK_JSON") {
		return ``
	}
	return `
Revision mode:
- user_note contains REVISION_CONTEXT_PACK_JSON. Treat it as the controlling edit contract.
- Read revision_intent, target, target_artifact, required_upstream, focused_context, recent_turns, recent_events, generation_config, downstream_refresh_scope, and apply_policy before writing.
- If revision_intent is patch_artifact_field, rewrite only target.field_path.
- If revision_intent is patch_artifact_entity, rewrite only the targeted object/list item, for example characters[0] or episodes[1], and preserve sibling entities.
- If revision_intent is patch_artifact_collection, return one structural collection operation and preserve every item outside its affected range.
- If revision_intent is patch_artifact_section, rewrite only the named section and preserve unrelated sections.
- If revision_intent starts with patch_, preserve every existing field that is not named by target.field_path, target.scope, focused_context.selected_text, or the user's request.
- If target_artifact.payload is provided, use it as the current version to patch. Do not replace unrelated fields with blanks or generic defaults.
- If the artifact must be regenerated, keep source facts and confirmed upstream artifacts stable unless the user explicitly asks to change them.
- For script revisions, only rewrite the targeted selected span, scene, episode, or range. Keep other episodes and continuity facts unchanged.
- The output must still be the full expected artifact payload, because the frontend refreshes the artifact from your returned JSON.
`
}

func revisionNoteFromSource(sourcePayload map[string]any) string {
	if len(sourcePayload) == 0 {
		return ""
	}
	if note, ok := sourcePayload["user_revision_note"].(string); ok {
		return note
	}
	if note := fmt.Sprint(sourcePayload["user_revision_note"]); note != "<nil>" {
		return note
	}
	return ""
}

func designReferenceForArtifact(sourceMode agent.SourceMode, artifactType string) string {
	promptPath, rulePaths := promptAndRulesForArtifact(sourceMode, artifactType)
	var builder strings.Builder
	if promptPath != "" {
		builder.WriteString("\n--- PROJECT PROMPT: ")
		builder.WriteString(promptPath)
		builder.WriteString(" ---\n")
		builder.WriteString(readDesignText(promptPath))
		builder.WriteString("\n")
	}
	for _, rulePath := range rulePaths {
		builder.WriteString("\n--- PROJECT RULE: ")
		builder.WriteString(rulePath)
		builder.WriteString(" ---\n")
		builder.WriteString(readDesignText(rulePath))
		builder.WriteString("\n")
	}
	if builder.Len() == 0 {
		return "(no project prompt configured for artifact)"
	}
	return builder.String()
}

func promptAndRulesForArtifact(sourceMode agent.SourceMode, artifactType string) (string, []string) {
	ruleVoice := "design/rules/shared/02_人物关系与声口.md"
	ruleStructure := "design/rules/shared/03_结构规划_开头_冲突_爽点_尾钩.md"
	ruleWriting := "design/rules/shared/04_剧本写作技法.md"
	ruleDialogue := "design/rules/shared/05_对白规则.md"
	ruleFormat := "design/rules/shared/06_转场_闪回_连续性_格式.md"
	rulePlatform := "design/rules/shared/07_小程序短剧适配.md"
	ruleExamples := "design/rules/shared/08_示例库.md"
	ruleNonNovel := "design/rules/shared/09_非小说素材与故事种子.md"

	switch artifactType {
	case "story_bible":
		return "design/小说-prompts/step1_story_bible.md", []string{
			"design/rules/shared/01_素材理解与故事圣经.md",
			ruleVoice,
		}
	case "episode_split":
		return "design/小说-prompts/step2_episode_split.md", []string{
			ruleStructure,
			rulePlatform,
		}
	case "material_bank":
		return "design/非小说-prompts/step1_material_bank.md", []string{
			ruleNonNovel,
			ruleVoice,
		}
	case "story_seed":
		return "design/非小说-prompts/step2_story_seed.md", []string{
			ruleNonNovel,
			ruleVoice,
			ruleStructure,
			rulePlatform,
		}
	case "series_blueprint":
		return "design/非小说-prompts/step3_series_blueprint.md", []string{
			ruleStructure,
			rulePlatform,
		}
	case "episode_cards":
		if sourceMode == agent.SourceModeNovel {
			return "design/小说-prompts/step3_episode_cards.md", []string{
				ruleVoice,
				ruleStructure,
				rulePlatform,
				ruleFormat,
			}
		}
		return "design/非小说-prompts/step4_episode_cards.md", []string{
			ruleVoice,
			ruleStructure,
			rulePlatform,
			ruleFormat,
		}
	case "script_context", "script_unit", "scripts":
		if artifactType == "script_context" {
			return "design/prompts/script_context.md", []string{
				ruleVoice,
				ruleFormat,
			}
		}
		return "design/prompts/script_generate.md", []string{
			ruleVoice,
			ruleWriting,
			ruleDialogue,
			ruleFormat,
			rulePlatform,
			ruleExamples,
		}
	default:
		return "", nil
	}
}

func ValidateDesignReferences() error {
	seen := map[string]bool{}
	for _, splitStagePath := range []string{"design/小说-prompts/step2a_episode_split_global.md", "design/小说-prompts/step2b_episode_split_batch.md"} {
		seen[splitStagePath] = true
		path := filepath.Join(projectRoot(), filepath.FromSlash(canonicalDesignPath(splitStagePath)))
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("required prompt or rule %s is unavailable: %w", splitStagePath, err)
		}
		if info.IsDir() || info.Size() == 0 {
			return fmt.Errorf("required prompt or rule %s is empty or not a file", splitStagePath)
		}
	}
	for _, sourceMode := range []agent.SourceMode{agent.SourceModeNovel, agent.SourceModeNonNovel} {
		for _, artifactType := range []string{"story_bible", "episode_split", "material_bank", "story_seed", "series_blueprint", "episode_cards", "script_context", "script_unit"} {
			promptPath, rulePaths := promptAndRulesForArtifact(sourceMode, artifactType)
			paths := append([]string{promptPath}, rulePaths...)
			for _, relPath := range paths {
				if relPath == "" || seen[relPath] {
					continue
				}
				seen[relPath] = true
				path := filepath.Join(projectRoot(), filepath.FromSlash(canonicalDesignPath(relPath)))
				info, err := os.Stat(path)
				if err != nil {
					return fmt.Errorf("required prompt or rule %s is unavailable: %w", relPath, err)
				}
				if info.IsDir() || info.Size() == 0 {
					return fmt.Errorf("required prompt or rule %s is empty or not a file", relPath)
				}
			}
		}
	}
	return nil
}
func readDesignText(relPath string) string {
	relPath = canonicalDesignPath(relPath)
	path := filepath.FromSlash(relPath)
	root := projectRoot()
	data, err := os.ReadFile(filepath.Join(root, path))
	if err == nil {
		return string(data)
	}
	return "[missing design file: " + relPath + "; error: " + err.Error() + "]"
}

func canonicalDesignPath(relPath string) string {
	switch {
	case strings.Contains(relPath, "step1_story_bible.md"):
		return "design/小说-prompts/step1_story_bible.md"
	case strings.Contains(relPath, "step2_episode_split.md"):
		return "design/小说-prompts/step2_episode_split.md"
	case strings.Contains(relPath, "step3_episode_cards.md") && strings.Contains(relPath, "-prompts"):
		return "design/小说-prompts/step3_episode_cards.md"
	case strings.Contains(relPath, "step1_material_bank.md"):
		return "design/非小说-prompts/step1_material_bank.md"
	case strings.Contains(relPath, "step2_story_seed.md"):
		return "design/非小说-prompts/step2_story_seed.md"
	case strings.Contains(relPath, "step3_series_blueprint.md"):
		return "design/非小说-prompts/step3_series_blueprint.md"
	case strings.Contains(relPath, "step4_episode_cards.md"):
		return "design/非小说-prompts/step4_episode_cards.md"
	case strings.Contains(relPath, "01_"):
		return "design/rules/shared/01_素材理解与故事圣经.md"
	case strings.Contains(relPath, "02_"):
		return "design/rules/shared/02_人物关系与声口.md"
	case strings.Contains(relPath, "03_"):
		return "design/rules/shared/03_结构规划_开头_冲突_爽点_尾钩.md"
	case strings.Contains(relPath, "04_"):
		return "design/rules/shared/04_剧本写作技法.md"
	case strings.Contains(relPath, "05_"):
		return "design/rules/shared/05_对白规则.md"
	case strings.Contains(relPath, "06_"):
		return "design/rules/shared/06_转场_闪回_连续性_格式.md"
	case strings.Contains(relPath, "07_"):
		return "design/rules/shared/07_小程序短剧适配.md"
	case strings.Contains(relPath, "08_"):
		return "design/rules/shared/08_示例库.md"
	case strings.Contains(relPath, "09_"):
		return "design/rules/shared/09_非小说素材与故事种子.md"
	case strings.Contains(relPath, "10_"):
		return "design/rules/video_to_script_extract/10_视频高还原剧本生成规则.md"
	default:
		return relPath
	}
}

func projectRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	current := wd
	for range 8 {
		if _, err := os.Stat(filepath.Join(current, "design")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return wd
}

func artifactDigest(artifacts []agent.Artifact) []map[string]any {
	artifacts = currentEffectiveArtifacts(artifacts)
	digest := make([]map[string]any, 0, len(artifacts))
	for _, artifact := range artifacts {
		digest = append(digest, map[string]any{
			"artifact_id":   artifact.ArtifactID,
			"artifact_type": artifact.ArtifactType,
			"status":        artifact.Status,
			"version":       artifact.Version,
			"payload":       artifact.Payload,
		})
	}
	return digest
}

func currentEffectiveArtifacts(artifacts []agent.Artifact) []agent.Artifact {
	byKey := map[string]agent.Artifact{}
	order := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Status == agent.ArtifactSuperseded || artifact.Status == agent.ArtifactInvalidated {
			continue
		}
		key := artifactDigestKey(artifact)
		current, exists := byKey[key]
		if !exists {
			order = append(order, key)
			byKey[key] = artifact
			continue
		}
		if newerArtifact(artifact, current) {
			byKey[key] = artifact
		}
	}
	out := make([]agent.Artifact, 0, len(order))
	for _, key := range order {
		out = append(out, byKey[key])
	}
	return out
}

func artifactDigestKey(artifact agent.Artifact) string {
	if artifact.ArtifactType == "script_unit" {
		if episodeID, ok := artifact.Payload["episode_id"]; ok {
			return artifact.ArtifactType + ":" + fmt.Sprint(episodeID)
		}
	}
	return artifact.ArtifactType
}

func newerArtifact(candidate agent.Artifact, current agent.Artifact) bool {
	if candidate.Version != current.Version {
		return candidate.Version > current.Version
	}
	if !candidate.UpdatedAt.Equal(current.UpdatedAt) {
		return candidate.UpdatedAt.After(current.UpdatedAt)
	}
	return candidate.ArtifactID > current.ArtifactID
}

func parseSingleArtifact(resp string, expectedType string) (agentruntime.PlannedArtifact, *agent.ApprovalRequest, error) {
	var payload struct {
		ArtifactType string         `json:"artifact_type"`
		Status       string         `json:"status"`
		Payload      map[string]any `json:"payload"`
		Approval     struct {
			Title  string `json:"title"`
			Reason string `json:"reason"`
		} `json:"approval"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(resp)), &payload); err != nil {
		return agentruntime.PlannedArtifact{}, nil, fmt.Errorf("%s model returned invalid JSON: %w", expectedType, err)
	}
	if payload.ArtifactType == "" {
		payload.ArtifactType = expectedType
	}
	if payload.ArtifactType != expectedType {
		return agentruntime.PlannedArtifact{}, nil, fmt.Errorf("model returned artifact_type %q, expected %q", payload.ArtifactType, expectedType)
	}
	if payload.Payload == nil {
		return agentruntime.PlannedArtifact{}, nil, fmt.Errorf("%s model returned empty payload", expectedType)
	}
	payload.Payload = normalizeArtifactPayload(expectedType, payload.Payload)
	if err := validateArtifactPayload(expectedType, payload.Payload); err != nil {
		return agentruntime.PlannedArtifact{}, nil, err
	}
	var approval *agent.ApprovalRequest
	if payload.Approval.Title != "" || payload.Approval.Reason != "" {
		approval = &agent.ApprovalRequest{
			Title:  payload.Approval.Title,
			Reason: payload.Approval.Reason,
		}
	}
	return agentruntime.PlannedArtifact{
		ArtifactType: payload.ArtifactType,
		Status:       artifactStatus(payload.Status),
		Payload:      payload.Payload,
	}, approval, nil
}

type revisionPatchPack struct {
	RevisionIntent string `json:"revision_intent,omitempty"`
	Target         struct {
		ArtifactID string `json:"artifact_id,omitempty"`
		FieldPath  string `json:"field_path,omitempty"`
		Scope      string `json:"scope,omitempty"`
		EpisodeID  string `json:"episode_id,omitempty"`
		SceneID    string `json:"scene_id,omitempty"`
		NodeID     string `json:"node_id,omitempty"`
	} `json:"target,omitempty"`
	FocusedContext struct {
		FieldPath      string   `json:"field_path,omitempty"`
		SelectedText   string   `json:"selected_text,omitempty"`
		StartLineID    string   `json:"start_line_id,omitempty"`
		EndLineID      string   `json:"end_line_id,omitempty"`
		LineIDs        []string `json:"line_ids,omitempty"`
		SelectionStart int      `json:"selection_start,omitempty"`
		SelectionEnd   int      `json:"selection_end,omitempty"`
		BeforeText     string   `json:"before_text,omitempty"`
		AfterText      string   `json:"after_text,omitempty"`
	} `json:"focused_context,omitempty"`
}

func compactRevisionInstruction(instruction string) string {
	const marker = "REVISION_CONTEXT_PACK_JSON:"
	index := strings.Index(instruction, marker)
	if index < 0 {
		return strings.TrimSpace(instruction)
	}
	prefix := strings.TrimSpace(instruction[:index])
	var pack map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(instruction[index+len(marker):])), &pack); err != nil {
		return strings.TrimSpace(instruction)
	}
	for _, key := range []string{"target_artifact", "required_upstream", "recent_turns", "recent_events", "current_step_context", "downstream_refresh_scope", "user_request"} {
		delete(pack, key)
	}
	payload, err := json.Marshal(pack)
	if err != nil {
		return strings.TrimSpace(instruction)
	}
	return prefix + "\n\n" + marker + "\n" + string(payload)
}

func compactPatchTargetPayload(payload map[string]any, pack revisionPatchPack) map[string]any {
	if pack.RevisionIntent != "patch_script_span" && pack.RevisionIntent != "regenerate_script_scene" {
		return payload
	}
	result := map[string]any{}
	for _, key := range []string{"episode_id", "title", "generation_config_ref"} {
		if value, ok := payload[key]; ok {
			result[key] = value
		}
	}
	scenes, _ := payload["scenes"].([]any)
	for _, value := range scenes {
		scene, _ := value.(map[string]any)
		if scene == nil || (pack.Target.SceneID != "" && strings.TrimSpace(fmt.Sprint(scene["scene_id"])) != pack.Target.SceneID) {
			continue
		}
		if pack.RevisionIntent == "regenerate_script_scene" {
			result["scenes"] = []any{scene}
			return result
		}
		blocks, _ := scene["blocks"].([]any)
		if len(blocks) == 0 {
			continue
		}
		start := scriptLineIndex(pack.Target.NodeID, blocks)
		if start < 0 {
			start = scriptLineIndex(pack.FocusedContext.StartLineID, blocks)
		}
		if start < 0 {
			start = 0
		}
		end := scriptLineIndex(pack.FocusedContext.EndLineID, blocks)
		if end < start {
			end = start
		}
		windowStart := start - 3
		if windowStart < 0 {
			windowStart = 0
		}
		windowEnd := end + 4
		if windowEnd > len(blocks) {
			windowEnd = len(blocks)
		}
		window := make([]any, 0, windowEnd-windowStart)
		for index := windowStart; index < windowEnd; index++ {
			block, _ := blocks[index].(map[string]any)
			copyBlock := cloneContextMap(block)
			if _, exists := copyBlock["line_id"]; !exists {
				copyBlock["line_id"] = fmt.Sprintf("%s-line-%d", pack.Target.SceneID, index+1)
			}
			window = append(window, copyBlock)
		}
		result["scenes"] = []any{map[string]any{
			"scene_id": scene["scene_id"], "heading": scene["heading"], "blocks": window,
		}}
		return result
	}
	return result
}

func scriptLineIndex(lineID string, blocks []any) int {
	lineID = strings.TrimSpace(lineID)
	if lineID == "" {
		return -1
	}
	for index, value := range blocks {
		if block, ok := value.(map[string]any); ok && strings.TrimSpace(fmt.Sprint(block["line_id"])) == lineID {
			return index
		}
	}
	marker := "-line-"
	position := strings.LastIndex(lineID, marker)
	if position < 0 {
		return -1
	}
	number, err := strconv.Atoi(strings.TrimSpace(lineID[position+len(marker):]))
	if err != nil || number < 1 || number > len(blocks) {
		return -1
	}
	return number - 1
}

func parseRevisionPatchPack(instruction string) revisionPatchPack {
	const marker = "REVISION_CONTEXT_PACK_JSON:"
	var pack revisionPatchPack
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

func parsePatchArtifact(resp string, expectedType string, instruction string) (agentruntime.PlannedArtifact, error) {
	jsonResponse := extractJSONObject(resp)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonResponse), &raw); err != nil {
		return agentruntime.PlannedArtifact{}, fmt.Errorf("%s patch model returned invalid JSON: %w", expectedType, err)
	}
	var payload struct {
		ArtifactType     string         `json:"artifact_type"`
		Status           string         `json:"status"`
		FieldPath        string         `json:"field_path"`
		Scope            string         `json:"scope"`
		Payload          map[string]any `json:"payload"`
		PatchValue       any            `json:"patch_value"`
		Operation        string         `json:"operation"`
		StartIndex       int            `json:"start_index"`
		DeleteCount      int            `json:"delete_count"`
		Items            []any          `json:"items"`
		MoveTo           int            `json:"move_to"`
		EpisodeID        any            `json:"episode_id"`
		SceneID          string         `json:"scene_id"`
		NodeID           string         `json:"node_id"`
		OldText          string         `json:"old_text"`
		NewText          string         `json:"new_text"`
		ContinuityNote   string         `json:"continuity_note"`
		StartLineID      string         `json:"start_line_id"`
		EndLineID        string         `json:"end_line_id"`
		ReplacementLines []struct {
			LineID  string `json:"line_id"`
			NewText string `json:"new_text"`
		} `json:"replacement_lines"`
	}
	if err := json.Unmarshal([]byte(jsonResponse), &payload); err != nil {
		return agentruntime.PlannedArtifact{}, fmt.Errorf("%s patch model returned invalid shape: %w", expectedType, err)
	}
	if payload.ArtifactType == "" {
		payload.ArtifactType = expectedType
	}
	if payload.ArtifactType != expectedType {
		return agentruntime.PlannedArtifact{}, fmt.Errorf("patch model returned artifact_type %q, expected %q", payload.ArtifactType, expectedType)
	}
	pack := parseRevisionPatchPack(instruction)
	if pack.RevisionIntent == "patch_script_span" {
		if len(pack.FocusedContext.LineIDs) > 1 {
			if len(payload.ReplacementLines) != len(pack.FocusedContext.LineIDs) {
				return agentruntime.PlannedArtifact{}, fmt.Errorf("script range patch returned %d replacement lines, expected %d", len(payload.ReplacementLines), len(pack.FocusedContext.LineIDs))
			}
			replacements := make([]any, 0, len(payload.ReplacementLines))
			for index, replacement := range payload.ReplacementLines {
				if replacement.LineID != pack.FocusedContext.LineIDs[index] {
					return agentruntime.PlannedArtifact{}, fmt.Errorf("script range patch line %d has id %q, expected %q", index, replacement.LineID, pack.FocusedContext.LineIDs[index])
				}
				replacements = append(replacements, map[string]any{"line_id": replacement.LineID, "new_text": replacement.NewText})
			}
			oldText := firstNonEmptyString(payload.OldText, pack.FocusedContext.SelectedText)
			if oldText == "" {
				return agentruntime.PlannedArtifact{}, fmt.Errorf("script range patch has no selected old_text")
			}
			return agentruntime.PlannedArtifact{ArtifactType: expectedType, Status: artifactStatus(payload.Status), Payload: map[string]any{
				"__script_span_patch": map[string]any{
					"episode_id": payload.EpisodeID, "start_line_id": pack.FocusedContext.StartLineID,
					"end_line_id": pack.FocusedContext.EndLineID, "line_ids": append([]string(nil), pack.FocusedContext.LineIDs...),
					"selection_start": pack.FocusedContext.SelectionStart, "selection_end": pack.FocusedContext.SelectionEnd,
					"old_text": oldText, "replacement_lines": replacements, "continuity_note": payload.ContinuityNote,
				},
			}}, nil
		}
		if strings.TrimSpace(payload.NewText) == "" {
			return agentruntime.PlannedArtifact{}, fmt.Errorf("script span patch model returned no new_text")
		}
		oldText := firstNonEmptyString(payload.OldText, pack.FocusedContext.SelectedText)
		if oldText == "" {
			return agentruntime.PlannedArtifact{}, fmt.Errorf("script span patch has no selected old_text")
		}
		return agentruntime.PlannedArtifact{ArtifactType: expectedType, Status: artifactStatus(payload.Status), Payload: map[string]any{
			"__script_span_patch": map[string]any{
				"episode_id": payload.EpisodeID, "scene_id": firstNonEmptyString(pack.Target.SceneID, payload.SceneID),
				"node_id": firstNonEmptyString(pack.Target.NodeID, payload.NodeID), "old_text": oldText,
				"selection_start": pack.FocusedContext.SelectionStart, "selection_end": pack.FocusedContext.SelectionEnd,
				"new_text": payload.NewText, "continuity_note": payload.ContinuityNote,
			},
		}}, nil
	}
	if pack.RevisionIntent == "regenerate_script_scene" {
		if _, ok := raw["patch_value"]; !ok || payload.PatchValue == nil {
			return agentruntime.PlannedArtifact{}, fmt.Errorf("script scene patch model returned no patch_value")
		}
		scene, ok := payload.PatchValue.(map[string]any)
		if !ok {
			return agentruntime.PlannedArtifact{}, fmt.Errorf("script scene patch is not an object")
		}
		targetSceneID := strings.TrimSpace(pack.Target.SceneID)
		returnedSceneID := strings.TrimSpace(fmt.Sprint(scene["scene_id"]))
		if returnedSceneID == "" {
			scene["scene_id"] = targetSceneID
		} else if targetSceneID != "" && returnedSceneID != targetSceneID {
			return agentruntime.PlannedArtifact{}, fmt.Errorf("script scene patch returned scene_id %q, expected %q", returnedSceneID, targetSceneID)
		}
		return agentruntime.PlannedArtifact{ArtifactType: expectedType, Status: artifactStatus(payload.Status), Payload: map[string]any{
			"__script_scene_patch": scene,
		}}, nil
	}
	if pack.RevisionIntent == "patch_artifact_collection" {
		fieldPath := firstNonEmptyString(pack.Target.FieldPath, pack.FocusedContext.FieldPath, payload.FieldPath)
		if fieldPath == "" {
			return agentruntime.PlannedArtifact{}, fmt.Errorf("%s collection patch returned no field_path", expectedType)
		}
		operation := normalizeCollectionPatchOperation(payload.Operation)
		if operation == "" {
			return agentruntime.PlannedArtifact{}, fmt.Errorf("%s collection patch returned unsupported operation %q", expectedType, payload.Operation)
		}
		if payload.StartIndex < 0 || payload.DeleteCount < 0 || payload.MoveTo < 0 {
			return agentruntime.PlannedArtifact{}, fmt.Errorf("%s collection patch returned negative indexes", expectedType)
		}
		switch operation {
		case "replace_range":
			if payload.DeleteCount == 0 || len(payload.Items) == 0 {
				return agentruntime.PlannedArtifact{}, fmt.Errorf("%s replace_range requires delete_count and items", expectedType)
			}
		case "insert_entities":
			if len(payload.Items) == 0 {
				return agentruntime.PlannedArtifact{}, fmt.Errorf("%s insert_entities requires items", expectedType)
			}
		case "delete_entities":
			if payload.DeleteCount == 0 {
				return agentruntime.PlannedArtifact{}, fmt.Errorf("%s delete_entities requires delete_count", expectedType)
			}
		}
		return agentruntime.PlannedArtifact{
			ArtifactType: payload.ArtifactType,
			Status:       artifactStatus(payload.Status),
			Payload: map[string]any{"__artifact_collection_patch": map[string]any{
				"operation": operation, "field_path": fieldPath, "start_index": payload.StartIndex,
				"delete_count": payload.DeleteCount, "items": payload.Items, "move_to": payload.MoveTo,
			}},
		}, nil
	}
	if payload.Payload != nil {
		return agentruntime.PlannedArtifact{
			ArtifactType: payload.ArtifactType,
			Status:       artifactStatus(payload.Status),
			Payload:      payload.Payload,
		}, nil
	}
	if _, ok := raw["patch_value"]; !ok {
		return agentruntime.PlannedArtifact{}, fmt.Errorf("%s patch model returned no patch_value", expectedType)
	}
	if pack.RevisionIntent == "patch_artifact_entity" && strings.TrimSpace(pack.Target.EpisodeID) != "" && strings.TrimSpace(pack.Target.FieldPath) == "" {
		entity, ok := payload.PatchValue.(map[string]any)
		if !ok {
			return agentruntime.PlannedArtifact{}, fmt.Errorf("%s episode entity patch is not an object", expectedType)
		}
		if _, exists := entity["episode_id"]; !exists {
			entity["episode_id"] = pack.Target.EpisodeID
		}
		return agentruntime.PlannedArtifact{
			ArtifactType: payload.ArtifactType,
			Status:       artifactStatus(payload.Status),
			Payload:      map[string]any{"episodes": []any{entity}},
		}, nil
	}
	fieldPath := firstNonEmptyString(pack.Target.FieldPath, pack.FocusedContext.FieldPath, payload.FieldPath)
	if fieldPath == "" {
		return agentruntime.PlannedArtifact{}, fmt.Errorf("%s patch model returned no field_path", expectedType)
	}
	if pack.RevisionIntent == "patch_artifact_entity" {
		operation := normalizeEntityPatchOperation(payload.Operation)
		if operation == "append_entity" || operation == "delete_entity" {
			return agentruntime.PlannedArtifact{
				ArtifactType: payload.ArtifactType,
				Status:       artifactStatus(payload.Status),
				Payload: map[string]any{"__artifact_entity_patch": map[string]any{
					"operation": operation, "field_path": fieldPath, "patch_value": payload.PatchValue,
				}},
			}, nil
		}
	}
	if hasNamedFieldSelector(fieldPath) {
		return agentruntime.PlannedArtifact{
			ArtifactType: payload.ArtifactType,
			Status:       artifactStatus(payload.Status),
			Payload: map[string]any{"__artifact_value_patch": map[string]any{
				"field_path": fieldPath, "patch_value": payload.PatchValue,
			}},
		}, nil
	}
	tokens := patchFieldPathTokens(fieldPath)
	if payload.Scope == "entity" || pack.Target.Scope == "entity" || pack.RevisionIntent == "patch_artifact_entity" {
		tokens = patchEntityTokens(tokens)
	}
	sparse := sparsePayloadFromTokens(tokens, payload.PatchValue)
	if len(sparse) == 0 {
		return agentruntime.PlannedArtifact{}, fmt.Errorf("%s patch model returned unusable field_path %q", expectedType, fieldPath)
	}
	return agentruntime.PlannedArtifact{
		ArtifactType: payload.ArtifactType,
		Status:       artifactStatus(payload.Status),
		Payload:      sparse,
	}, nil
}

func hasNamedFieldSelector(fieldPath string) bool {
	for remaining := fieldPath; ; {
		open := strings.Index(remaining, "[")
		if open < 0 {
			return false
		}
		close := strings.Index(remaining[open+1:], "]")
		if close < 0 {
			return false
		}
		selector := strings.TrimSpace(remaining[open+1 : open+1+close])
		if _, err := strconv.Atoi(selector); err != nil {
			return selector != ""
		}
		remaining = remaining[open+1+close+1:]
	}
}

func normalizeEntityPatchOperation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "append", "add", "append_entity", "add_entity":
		return "append_entity"
	case "delete", "remove", "delete_entity", "remove_entity":
		return "delete_entity"
	default:
		return "replace_entity"
	}
}

func normalizeCollectionPatchOperation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "replace_range", "split", "merge":
		return "replace_range"
	case "insert", "insert_entities", "append", "append_entities":
		return "insert_entities"
	case "delete", "remove", "delete_entities", "remove_entities":
		return "delete_entities"
	case "move", "move_entity", "reorder":
		return "move_entity"
	default:
		return ""
	}
}

func normalizeArtifactPayload(expectedType string, payload map[string]any) map[string]any {
	nested, ok := payload[expectedType].(map[string]any)
	if !ok {
		return payload
	}
	out := make(map[string]any, len(nested)+len(payload))
	for key, value := range nested {
		out[key] = value
	}
	for key, value := range payload {
		if key == expectedType {
			continue
		}
		if _, exists := out[key]; !exists {
			out[key] = value
		}
	}
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func patchFieldPathTokens(path string) []any {
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

func patchEntityTokens(tokens []any) []any {
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

func sparsePayloadFromTokens(tokens []any, value any) map[string]any {
	if len(tokens) == 0 {
		return nil
	}
	first, ok := tokens[0].(string)
	if !ok || strings.TrimSpace(first) == "" {
		return nil
	}
	return map[string]any{first: sparseNodeFromTokens(tokens[1:], value)}
}

func sparseNodeFromTokens(tokens []any, value any) any {
	if len(tokens) == 0 {
		return value
	}
	switch typed := tokens[0].(type) {
	case string:
		return map[string]any{typed: sparseNodeFromTokens(tokens[1:], value)}
	case int:
		if typed < 0 {
			return value
		}
		items := make([]any, typed+1)
		items[typed] = sparseNodeFromTokens(tokens[1:], value)
		return items
	default:
		return value
	}
}

func artifactSchema(artifactType string, sourceMode agent.SourceMode) string {
	switch artifactType {
	case "story_bible":
		return storyBibleSchema()
	case "episode_split":
		return episodeSplitSchema()
	case "material_bank":
		return materialBankSchema()
	case "story_seed":
		return storySeedSchema()
	case "series_blueprint":
		return seriesBlueprintSchema()
	case "episode_cards":
		return episodeCardsSchema(sourceMode)
	case "script_context":
		return scriptContextSchema(sourceMode)
	case "script_unit":
		return scriptUnitSchema(sourceMode)
	case "scripts":
		return scriptsSchema(sourceMode)
	default:
		return `{}`
	}
}

func artifactStatus(value string) agent.ArtifactStatus {
	switch value {
	case string(agent.ArtifactPendingApproval):
		return agent.ArtifactPendingApproval
	case string(agent.ArtifactDraft):
		return agent.ArtifactDraft
	case string(agent.ArtifactSuperseded):
		return agent.ArtifactSuperseded
	case string(agent.ArtifactInvalidated):
		return agent.ArtifactInvalidated
	default:
		return agent.ArtifactConfirmed
	}
}

func storyBibleSchema() string {
	return `{"story_overview":{"one_sentence_logline":"","core_conflict":"","main_emotional_drive":"","genre_tags":[]},"source_structure":[{"source_unit_id":"S001","source_range":"","summary":"","key_events":[],"character_changes":[],"conflict_stage":"","hook_or_suspense_potential":"high | medium | low","source_evidence":[]}],"characters":[{"name":"","role":"","goal":"","relationship_position":"","speech_profile":{"base_style":"","regional_speech":"","usage_boundary":""},"source_evidence":[]}],"relationships":[],"world_rules":[],"major_plotline":[],"climax_map":{"first_major_climax_candidate":{"source_range":"","event_summary":"","emotional_payoff":"","why_it_can_hold_first_card":"","recommended_position_note":"","source_evidence":[],"risk_notes":[]},"major_climax_candidates":[]},"foreshadowing_and_payoff":[],"must_keep_facts":[],"short_drama_assets":{"high_value_conflicts":[],"core_hook_refinement":[],"emotional_drive_frontload":[],"payoff_candidates":[],"hook_candidates":[],"visualization_candidates":[],"preserve_candidates":[],"merge_or_compress_candidates":[],"delete_or_deemphasize_candidates":[],"frontload_candidates":[],"psychology_to_scene_candidates":[],"compression_candidates":[],"risk_flags":[]},"adaptation_risks":[],"source_trace":{"from_source_text":[],"model_inference":[]}}`
}

func episodeSplitSchema() string {
	return `{"generation_config":{"target_episode_count":0,"episode_duration_minutes":0,"target_script_chars":0,"target_source_chars_per_episode":0,"boundary_detection_window_chars":800,"preserve_existing_episode_marks":false,"existing_episode_markers_detected":false},"target_episode_count":null,"actual_episode_count":0,"split_strategy":"","source_volume_assessment":{"source_chars":0,"effective_source_chars":0,"target_source_chars_per_episode":0,"detected_episode_markers":[],"marker_policy":"preserve | ignore | user_confirm_required"},"episodes":[{"episode_id":1,"source_refs":[{"source_unit_id":"S001","source_range":"","start_anchor":"","end_anchor":"","start_offset":0,"end_offset":0}],"source_summary":"","core_event":"","character_turn":"","boundary_reason":"","boundary_check":{"previous_episode_end":"","next_episode_start":"","candidate_window_chars":800,"cut_after_anchor":"","cut_before_anchor":"","continuity_risk":"none | low | medium | high","qa_or_action_split_risk":false,"manual_review_required":false},"hook_strength":"high | medium | low","hook_type":"conflict | secret | decision | danger | source_supported_preview | weak_source_boundary","risk":"","information_density":"high | medium | low","pacing_risk":"none | weak_source_boundary | low_information_density | likely_padding | source_too_short","split_confidence":"high | medium | low","weak_episode_reason":"","requires_user_attention":false,"adaptation_added":[]}],"coverage_check":{"covered_source_ranges":[],"missing_source_ranges":[],"duplicated_source_ranges":[],"order_issues":[]},"global_risks":[],"source_trace":{"from_source_text":[],"from_story_bible":[],"model_inference":[]}}`
}

func materialBankSchema() string {
	return `{"generation_config":{"target_episode_count":0,"episode_duration_minutes":0,"target_script_chars":0},"input_type_tags":[],"user_supplied_facts":{"characters":[],"relationships":[],"events":[],"world_rules":[],"scenes":[],"dialogue_lines":[],"selling_points":[]},"conflict_materials":[],"emotional_drives":[],"payoff_candidates":[],"hook_candidates":[],"visual_scene_candidates":[],"discard_or_later":[],"inferred_candidates":[],"gaps_and_questions":[],"volume_fit_notes":{"target_episode_count":0,"material_sufficiency":"enough | weak | insufficient","risks":[],"questions":[]},"most_promising_direction":"","source_trace":{"from_user_material":[],"inferred":[]}}`
}

func storySeedSchema() string {
	return `{"generation_config":{"target_episode_count":0,"episode_duration_minutes":0,"target_script_chars":0},"logline":"","core_premise":"","genre_tags":[],"protagonist":{"name_or_role":"","goal":"","pressure":"","inner_need":""},"main_characters":[],"relationship_engine":[],"central_conflict":"","world_rules":[],"main_plotline":{"opening_situation":"","escalation_path":"","major_turn":"","final_payoff_direction":""},"payoff_chain":[],"hook_engine":[],"generated_additions":[],"development_notes":[],"volume_plan_notes":{"target_episode_count":0,"can_support_target":true,"expansion_strategy":[],"padding_risks":[]},"risks":[],"source_trace":{"from_material_bank":[],"inferred":[]}}`
}

func seriesBlueprintSchema() string {
	return `{"generation_config":{"target_episode_count":0,"episode_duration_minutes":0,"target_script_chars":0},"resolved_episode_count":0,"recommended_episode_count":0,"episode_count_reason":"","series_promise":"","phase_plan":[{"phase_id":"P1","episode_range":"1-3","phase_function":"","main_conflict":"","payoff_focus":"","hook_strategy":""}],"payoff_distribution":[],"hook_distribution":[],"first_major_climax_plan":{"recommended_episode_range":"","event_summary":"","emotional_payoff":"","supporting_basis":[],"risk_notes":[]},"pacing_density_plan":[],"character_progression":[],"relationship_progression":[],"continuity_rules":[],"generated_additions":[],"fit_risks":[],"source_trace":{"from_story_seed":[],"from_material_bank":[],"inferred":[]}}`
}

func episodeCardsSchema(sourceMode agent.SourceMode) string {
	if sourceMode == agent.SourceModeNovel {
		return `{"generation_config":{"target_episode_count":0,"episode_duration_minutes":0,"target_script_chars":0},"episodes":[{"episode_id":1,"source_refs":[],"source_summary":"","episode_function":"","opening_state":"","main_conflict":"","payoff_or_reversal":"","character_turn":"","ending_hook":{"hook_text":"","hook_strength":"high | medium | low","hook_source":"source_fact | source_supported_preview | adaptation_suggestion"},"card_point_function":"","pacing_plan":{"information_density":"high | medium | low","pacing_risk":"","weak_episode_handling":"none | compress_scene | sharpen_existing_conflict | mark_for_user_review","must_not_expand":[]},"visual_strategy":{"key_visual_moments":[],"flashback_or_memory_use":"","sound_or_object_triggers":[]},"must_keep_facts":[],"must_keep_dialogue_or_moments":[],"scene_outline":[],"adaptation_suggestions":[],"risk_notes":[]}],"continuity_delta":{"new_facts":[],"character_state_changes":[],"relationship_changes":[],"foreshadowing_opened":[],"foreshadowing_resolved":[]},"next_action":"script_generate"}`
	}
	return `{"generation_config":{"target_episode_count":0,"episode_duration_minutes":0,"target_script_chars":0},"episodes":[{"episode_id":1,"episode_function":"","opening_state":"","main_conflict":"","key_events":[],"payoff_or_reversal":"","character_turn":"","ending_hook":{"hook_text":"","hook_type":"conflict | secret | decision | danger | emotional_turn | reveal","hook_strength":"high | medium | low"},"card_point_function":"","pacing_plan":{"information_density":"high | medium | low","pacing_risk":"","weak_episode_handling":"none | compress_scene | sharpen_existing_conflict | mark_for_user_review","must_not_expand":[]},"visual_strategy":{"key_visual_moments":[],"flashback_or_memory_use":"","sound_or_object_triggers":[]},"scene_outline":[],"source_basis":{"from_user_material":[],"from_story_seed":[],"from_series_blueprint":[],"generated_additions":[]},"risk_notes":[]}],"continuity_delta":{"new_facts":[],"character_state_changes":[],"relationship_changes":[],"hooks_opened":[],"hooks_resolved":[]},"next_action":"script_generate"}`
}

func scriptContextSchema(sourceMode agent.SourceMode) string {
	return `{"source_mode":"` + string(sourceMode) + `","generation_config":{"target_episode_count":0,"episode_duration_minutes":0,"target_script_chars":0},"must_follow_facts":[],"allowed_additions":[],"forbidden_changes":[],"character_state":[],"relationship_state":[],"continuity_state":{},"source_material":{"text":"","refs":[],"basis":[]},"style_constraints":{},"user_notes":[]}`
}

func scriptUnitSchema(sourceMode agent.SourceMode) string {
	return `{"episode_id":1,"source_mode":"` + string(sourceMode) + `","generation_config":{"target_episode_count":0,"episode_duration_minutes":0,"target_script_chars":0},"title":"","script_text":"","scenes":[{"scene_id":"scene_1_1","heading":"","blocks":[]}],"source_refs":[],"source_basis":{"from_source_text":[],"from_story_bible":[],"from_story_seed":[],"from_series_blueprint":[],"generated_additions":[]},"used_adaptation_suggestions":[],"used_generated_additions":[],"applied_visual_strategy":[],"pacing_execution_notes":[],"risk_notes":[],"continuity_delta":{"new_facts":[],"character_state_changes":[],"relationship_changes":[],"foreshadowing_opened":[],"foreshadowing_resolved":[],"hooks_opened":[],"hooks_resolved":[]},"self_check":{}}`
}

func scriptsSchema(sourceMode agent.SourceMode) string {
	return `{"source_mode":"` + string(sourceMode) + `","generation_config":{"target_episode_count":0,"episode_duration_minutes":0,"target_script_chars":0},"episode_count":0,"script_units":[],"global_continuity_state":{},"quality_flags":[]}`
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
