package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	"novel2script-agent/backend/internal/agent"
	"novel2script-agent/backend/internal/llm"
)

type sourceAnalysisTestClient struct {
	request llm.ChatRequest
}

func (c *sourceAnalysisTestClient) Configured() bool { return true }

func (c *sourceAnalysisTestClient) Complete(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	c.request = request
	return llm.ChatResponse{Content: "这是材料分析结果。"}, nil
}

func TestAnalyzeSourcesUsesSelectedContentWithoutArtifactGeneration(t *testing.T) {
	client := &sourceAnalysisTestClient{}
	contentWorker := NewLLMWorker(client, "content-model")

	answer, err := contentWorker.AnalyzeSources(context.Background(), "分析一下这个文件", []SourceDocument{{
		FileID: "file_1", FileName: "材料.txt", MimeType: "text/plain", Text: "这是被选中的正文。",
	}})
	if err != nil {
		t.Fatalf("analyze source: %v", err)
	}
	if answer != "这是材料分析结果。" {
		t.Fatalf("unexpected answer: %q", answer)
	}
	if client.request.Model != "content-model" || len(client.request.Messages) != 2 {
		t.Fatalf("unexpected content-model request: %#v", client.request)
	}
	if !strings.Contains(client.request.Messages[1].Content, "这是被选中的正文。") || !strings.Contains(client.request.Messages[1].Content, "材料.txt") {
		t.Fatalf("selected source was not included: %s", client.request.Messages[1].Content)
	}
	if strings.Contains(client.request.Messages[0].Content, "Output strict JSON only") {
		t.Fatalf("source analysis must not use the artifact-generation contract: %s", client.request.Messages[0].Content)
	}
	if !strings.Contains(client.request.Messages[0].Content, "Do not assume an attachment is a novel") {
		t.Fatalf("source type guard missing: %s", client.request.Messages[0].Content)
	}
}

func TestTruncateSourceTextKeepsHeadAndTail(t *testing.T) {
	value := strings.Repeat("头", 80) + strings.Repeat("中", 80) + strings.Repeat("尾", 80)
	actual := truncateSourceText(value, 100)
	if len([]rune(actual)) != 100 || !strings.HasPrefix(actual, "头") || !strings.HasSuffix(actual, "尾") {
		t.Fatalf("unexpected bounded source: %q", actual)
	}
	if !strings.Contains(actual, "因长度限制") {
		t.Fatalf("expected truncation marker: %q", actual)
	}
}

func TestDesignReferenceLoadsScriptPromptAndRules(t *testing.T) {
	ref := designReferenceForArtifact(agent.SourceModeNonNovel, "script_unit")
	for _, want := range []string{
		"script_generate",
		"04_剧本写作技法",
		"05_对白规则",
		"08_示例库",
		"△",
	} {
		if !strings.Contains(ref, want) {
			t.Fatalf("design reference missing %q", want)
		}
	}
	if strings.Contains(ref, "[missing design file:") {
		t.Fatalf("design reference still has missing file marker: %s", ref)
	}
}

func TestPromptAndRuleRegistryCoversBothProductionFlows(t *testing.T) {
	tests := []struct {
		mode         agent.SourceMode
		artifactType string
		promptSuffix string
		ruleHint     string
	}{
		{agent.SourceModeNovel, "story_bible", "step1_story_bible.md", "01_素材理解与故事圣经.md"},
		{agent.SourceModeNovel, "episode_split", "step2_episode_split.md", "03_结构规划_开头_冲突_爽点_尾钩.md"},
		{agent.SourceModeNovel, "episode_cards", "step3_episode_cards.md", "02_人物关系与声口.md"},
		{agent.SourceModeNonNovel, "material_bank", "step1_material_bank.md", "09_非小说素材与故事种子.md"},
		{agent.SourceModeNonNovel, "story_seed", "step2_story_seed.md", "09_非小说素材与故事种子.md"},
		{agent.SourceModeNonNovel, "series_blueprint", "step3_series_blueprint.md", "03_结构规划_开头_冲突_爽点_尾钩.md"},
		{agent.SourceModeNonNovel, "episode_cards", "step4_episode_cards.md", "02_人物关系与声口.md"},
		{agent.SourceModeNovel, "script_context", "script_context.md", "06_转场_闪回_连续性_格式.md"},
		{agent.SourceModeNonNovel, "script_unit", "script_generate.md", "05_对白规则.md"},
	}
	for _, test := range tests {
		prompt, rules := promptAndRulesForArtifact(test.mode, test.artifactType)
		if !strings.HasSuffix(prompt, test.promptSuffix) {
			t.Fatalf("%s/%s prompt=%q, expected suffix %q", test.mode, test.artifactType, prompt, test.promptSuffix)
		}
		if !containsStringSuffix(rules, test.ruleHint) {
			t.Fatalf("%s/%s rules=%#v, expected %q", test.mode, test.artifactType, rules, test.ruleHint)
		}
	}
}

func containsStringSuffix(values []string, suffix string) bool {
	for _, value := range values {
		if strings.HasSuffix(value, suffix) {
			return true
		}
	}
	return false
}

func TestScriptContextUsesContextPromptInsteadOfScriptWritingPrompt(t *testing.T) {
	prompt := scriptStepPrompt(agent.SourceModeNovel, []map[string]any{{"artifact_type": "episode_cards"}}, "script_context", "")
	if !strings.Contains(prompt, "Do not output markdown or script prose") {
		t.Fatalf("script_context prompt did not use context-only contract: %s", prompt)
	}
	if strings.Contains(prompt, "Put renderable script lines") {
		t.Fatal("script_context prompt must not contain script-writing instructions")
	}
	if err := ValidateDesignReferences(); err != nil {
		t.Fatalf("design reference validation failed: %v", err)
	}
}

func TestPlanningAndScriptPromptsUsePendingApprovalStatus(t *testing.T) {
	planning := planStepPrompt(agent.SourceModeNovel, map[string]any{}, nil, "story_bible")
	script := scriptStepPrompt(agent.SourceModeNovel, nil, "script_unit", "")
	for name, prompt := range map[string]string{"planning": planning, "script": script} {
		if !strings.Contains(prompt, `"status": "pending_approval"`) || strings.Contains(prompt, `"status": "confirmed"`) {
			t.Fatalf("%s prompt has contradictory artifact status", name)
		}
	}
}

func TestNormalizeArtifactPayloadUnwrapsPromptShape(t *testing.T) {
	payload := normalizeArtifactPayload("script_unit", map[string]any{
		"script_unit": map[string]any{
			"episode_id": 1,
			"title":      "第一集",
		},
		"continuity_delta": map[string]any{"new_facts": []any{}},
	})
	if _, ok := payload["script_unit"]; ok {
		t.Fatal("expected nested script_unit key to be unwrapped")
	}
	if payload["title"] != "第一集" {
		t.Fatalf("expected title to be preserved, got %#v", payload["title"])
	}
	if _, ok := payload["continuity_delta"]; !ok {
		t.Fatal("expected sibling continuity_delta to be preserved")
	}
}

func TestRevisionGuidanceIsInjectedForPlanningAndScriptPrompts(t *testing.T) {
	note := `USER_REVISION_REQUEST:
修改主角目标

REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_field","target":{"artifact_type":"story_bible","field_path":"characters[0].goal","scope":"field"}}`

	planningPrompt := planStepPrompt(agent.SourceModeNovel, map[string]any{
		"text":               "source",
		"user_revision_note": note,
	}, nil, "story_bible")
	if !strings.Contains(planningPrompt, "Revision mode:") || !strings.Contains(planningPrompt, "preserve every existing field") {
		t.Fatalf("expected planning prompt to include revision guidance, got %s", planningPrompt)
	}

	scriptPrompt := scriptStepPrompt(agent.SourceModeNovel, nil, "script_unit", note)
	if !strings.Contains(scriptPrompt, "REVISION_CONTEXT_PACK_JSON") || !strings.Contains(scriptPrompt, "only rewrite the targeted selected span") {
		t.Fatalf("expected script prompt to include revision guidance, got %s", scriptPrompt)
	}
}

func TestParsePatchArtifactBuildsSparseFieldPayload(t *testing.T) {
	note := `USER_REVISION_REQUEST:
only change goal

REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_field","target":{"artifact_type":"story_bible","field_path":"characters[0].goal","scope":"field"}}`
	planned, err := parsePatchArtifact(`{"artifact_type":"story_bible","status":"pending_approval","field_path":"characters[0].goal","scope":"field","patch_value":"new goal"}`, "story_bible", note)
	if err != nil {
		t.Fatalf("expected patch artifact to parse, got %v", err)
	}
	characters, ok := planned.Payload["characters"].([]any)
	if !ok || len(characters) != 1 {
		t.Fatalf("expected sparse characters array, got %#v", planned.Payload)
	}
	character, ok := characters[0].(map[string]any)
	if !ok {
		t.Fatalf("expected sparse character map, got %#v", characters[0])
	}
	if character["goal"] != "new goal" {
		t.Fatalf("expected sparse goal patch, got %#v", character)
	}
	if _, exists := character["name"]; exists {
		t.Fatalf("expected no unrelated fields in sparse patch, got %#v", character)
	}
}

func TestParsePatchArtifactAcceptsMarkdownJSONFence(t *testing.T) {
	note := `USER_REVISION_REQUEST:
加强情绪

REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_script_span","target":{"artifact_type":"script_unit","episode_id":"14","scene_id":"scene_14_1","node_id":"scene_14_1-line-8","scope":"selection"},"focused_context":{"selected_text":"别打了！都是一家人！","selection_start":0,"selection_end":10}}`
	response := "```json\n" + `{"artifact_type":"script_unit","status":"pending_approval","operation":"patch_script_span","episode_id":14,"scene_id":"scene_14_1","node_id":"scene_14_1-line-8","old_text":"别打了！都是一家人！","new_text":"别打了！求你们住手！我们都是一家人啊！"}` + "\n```"
	planned, err := parsePatchArtifact(response, "script_unit", note)
	if err != nil {
		t.Fatalf("markdown fenced patch should parse: %v", err)
	}
	patch := planned.Payload["__script_span_patch"].(map[string]any)
	if patch["new_text"] != "别打了！求你们住手！我们都是一家人啊！" {
		t.Fatalf("unexpected parsed patch: %#v", patch)
	}
}

func TestScriptSpanPatchUsesMinimalDeduplicatedContext(t *testing.T) {
	client := &sourceAnalysisTestClient{}
	clientResponse := `{"artifact_type":"script_unit","status":"pending_approval","operation":"patch_script_span","episode_id":8,"scene_id":"scene_8_1","node_id":"scene_8_1-line-10","old_text":"目标句","new_text":"更有情绪的目标句"}`
	clientWithResponse := &fixedWorkerClient{request: &client.request, response: clientResponse}
	contentWorker := NewLLMWorker(clientWithResponse, "content-model")
	blocks := make([]any, 0, 20)
	for index := 1; index <= 20; index++ {
		text := "附近行"
		if index == 1 || index == 20 {
			text = "FAR_UNRELATED_LINE"
		}
		if index == 10 {
			text = "目标句"
		}
		blocks = append(blocks, map[string]any{"block_type": "dialogue", "speaker": "人物", "text": text})
	}
	target := agent.Artifact{ArtifactID: "script_8", ArtifactType: "script_unit", Status: agent.ArtifactConfirmed, Payload: map[string]any{
		"episode_id": 8, "script_text": strings.Repeat("整集正文", 2000),
		"scenes": []any{map[string]any{"scene_id": "scene_8_1", "blocks": blocks}},
	}}
	existing := []agent.Artifact{
		{ArtifactID: "source", ArtifactType: "source_input", Status: agent.ArtifactConfirmed, Payload: map[string]any{"text": strings.Repeat("原文", 6000)}},
		{ArtifactID: "split", ArtifactType: "episode_split", Status: agent.ArtifactConfirmed, Payload: map[string]any{"episodes": []any{map[string]any{
			"episode_id": 8, "source_refs": []any{map[string]any{"start_offset": 2000, "end_offset": 3200}},
		}}}},
		target,
	}
	instruction := `USER_REVISION_REQUEST:
增强这句台词

REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_script_span","target":{"artifact_id":"script_8","artifact_type":"script_unit","episode_id":"8","scene_id":"scene_8_1","node_id":"scene_8_1-line-10","scope":"selection"},"focused_context":{"selected_text":"目标句"},"target_artifact":{"payload":{"secret":"DUPLICATED_TARGET_PAYLOAD"}},"recent_turns":[{"content":"UNRELATED_HISTORY"}]}`
	if _, err := contentWorker.PatchArtifactStep(context.Background(), agent.Run{SourceMode: agent.SourceModeNovel}, target, existing, "script_unit", instruction); err != nil {
		t.Fatalf("patch script span: %v", err)
	}
	prompt := client.request.Messages[1].Content
	for _, forbidden := range []string{"DUPLICATED_TARGET_PAYLOAD", "UNRELATED_HISTORY", "FAR_UNRELATED_LINE", strings.Repeat("整集正文", 100)} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("patch prompt retained unrelated or duplicate context %q", forbidden[:min(len(forbidden), 40)])
		}
	}
	if !strings.Contains(prompt, "目标句") || !strings.Contains(prompt, "scene_8_1-line-10") {
		t.Fatalf("patch prompt lost target context: %s", prompt)
	}
	if len([]rune(prompt)) > 25000 {
		t.Fatalf("single-line patch prompt is still oversized: %d", len([]rune(prompt)))
	}
}

func TestParseCollectionPatchReturnsStructuralOperation(t *testing.T) {
	instruction := "REVISION_CONTEXT_PACK_JSON:" + `{"revision_intent":"patch_artifact_collection","target":{"artifact_type":"episode_split","field_path":"episodes","scope":"collection"}}`
	planned, err := parsePatchArtifact(`{
		"artifact_type":"episode_split","status":"pending_approval","field_path":"episodes","scope":"collection",
		"operation":"replace_range","start_index":1,"delete_count":1,
		"items":[{"episode_id":2,"source_summary":"part a"},{"episode_id":3,"source_summary":"part b"}]
	}`, "episode_split", instruction)
	if err != nil {
		t.Fatalf("parse collection patch: %v", err)
	}
	patch, ok := planned.Payload["__artifact_collection_patch"].(map[string]any)
	if !ok || patch["operation"] != "replace_range" || patch["field_path"] != "episodes" {
		t.Fatalf("unexpected collection patch: %#v", planned.Payload)
	}
}

func TestWorkerRequestCarriesRunCorrelation(t *testing.T) {
	var request llm.ChatRequest
	client := &fixedWorkerClient{request: &request, response: `{"artifact_type":"script_unit","status":"pending_approval","payload":{"episode_id":4,"source_mode":"novel","title":"Episode 4","source_refs":[],"script_text":"ok","scenes":[]}}`}
	worker := NewLLMWorker(client, "content-model")
	_, err := worker.WriteScriptStep(context.Background(), agent.Run{RunID: "run_trace", ProjectID: "project_trace", SourceMode: agent.SourceModeNovel}, nil, "script_unit", "TASK: Generate only episode_id=4")
	if err != nil {
		t.Fatalf("write script: %v", err)
	}
	trace := request.TraceContext
	if trace["component"] != "content" || trace["operation"] != "write_script" || trace["run_id"] != "run_trace" || trace["project_id"] != "project_trace" || trace["episode_id"] != "4" {
		t.Fatalf("worker trace correlation missing: %#v", trace)
	}
	if trace["task_id"] != "task_generate_script_episode_4" {
		t.Fatalf("script task correlation missing: %#v", trace)
	}
}

func TestScriptContextTraceUsesDedicatedOperation(t *testing.T) {
	var request llm.ChatRequest
	client := &fixedWorkerClient{request: &request, response: `{
		"artifact_type":"script_context",
		"status":"confirmed",
		"payload":{"source_mode":"novel","must_follow_facts":[],"source_material":{"text":"","refs":[],"basis":[]}}
	}`}
	worker := NewLLMWorker(client, "content-model")
	_, err := worker.WriteScriptStep(context.Background(), agent.Run{RunID: "run_context", ProjectID: "project_context", SourceMode: agent.SourceModeNovel}, nil, "script_context", "")
	if err != nil {
		t.Fatalf("build script context: %v", err)
	}
	trace := request.TraceContext
	if trace["operation"] != "build_script_context" {
		t.Fatalf("script context trace operation is ambiguous: %#v", trace)
	}
	if trace["task_id"] != "task_build_script_context_model_request" {
		t.Fatalf("script context task correlation is wrong: %#v", trace)
	}
	if _, exists := trace["episode_id"]; exists {
		t.Fatalf("script context trace must not claim an episode: %#v", trace)
	}
}

func TestCorrectionAttemptTraceIsDistinguishable(t *testing.T) {
	var request llm.ChatRequest
	client := &fixedWorkerClient{request: &request, response: `{"artifact_type":"script_unit","status":"pending_approval","payload":{"episode_id":4,"source_mode":"novel","title":"Episode 4","source_refs":[],"script_text":"ok","scenes":[]}}`}
	worker := NewLLMWorker(client, "content-model")
	_, err := worker.WriteScriptStep(context.Background(), agent.Run{RunID: "run_trace", SourceMode: agent.SourceModeNovel}, nil, "script_unit", "TASK: Generate only episode_id=4\nLENGTH CORRECTION ATTEMPT 2: shorten it")
	if err != nil {
		t.Fatalf("correct script length: %v", err)
	}
	if request.TraceContext["operation"] != "write_script_length_correction" || request.TraceContext["correction_attempt"] != "2" {
		t.Fatalf("correction trace is ambiguous: %#v", request.TraceContext)
	}
}

func TestPatchTraceUsesArtifactPatchTaskID(t *testing.T) {
	trace := workerTraceContext(agent.Run{RunID: "run_patch"}, "patch_artifact", "episode_split", 1)
	if trace["task_id"] != "task_split_episodes_patch_request" || trace["episode_id"] != "1" {
		t.Fatalf("patch task correlation is wrong: %#v", trace)
	}
}

type fixedWorkerClient struct {
	request  *llm.ChatRequest
	response string
}

func (c *fixedWorkerClient) Configured() bool { return true }

func (c *fixedWorkerClient) Complete(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	*c.request = request
	return llm.ChatResponse{Content: c.response}, nil
}

func TestParsePatchArtifactBuildsSparsePayloadForSupportedRevisionMatrix(t *testing.T) {
	cases := []struct {
		name         string
		artifactType string
		intent       string
		fieldPath    string
		scope        string
		patchValue   string
		root         string
	}{
		{"source notes", "source_input", "patch_artifact_field", "notes", "field", `["新说明"]`, "notes"},
		{"story field", "story_bible", "patch_artifact_field", "characters[0].goal", "field", `"新目标"`, "characters"},
		{"story entity", "story_bible", "patch_artifact_entity", "characters[0]", "entity", `{"name":"主角","goal":"新目标"}`, "characters"},
		{"story section", "story_bible", "patch_artifact_section", "relationships", "section", `[{"from":"A","to":"B"}]`, "relationships"},
		{"episode split field", "episode_split", "patch_artifact_field", "generation_config.episode_duration_minutes", "field", `1`, "generation_config"},
		{"episode split entity", "episode_split", "patch_artifact_entity", "episodes[0]", "entity", `{"episode_id":1,"boundary_check":{"cut_after_anchor":"新切点"}}`, "episodes"},
		{"episode split section", "episode_split", "patch_artifact_section", "coverage_check", "section", `{"coverage_ratio":1}`, "coverage_check"},
		{"material entity", "material_bank", "patch_artifact_entity", "conflict_materials[0]", "entity", `{"id":"M1","summary":"新冲突"}`, "conflict_materials"},
		{"material field", "material_bank", "patch_artifact_field", "most_promising_direction", "field", `"新方向"`, "most_promising_direction"},
		{"material section", "material_bank", "patch_artifact_section", "gaps_and_questions", "section", `[{"question":"新问题"}]`, "gaps_and_questions"},
		{"story seed field", "story_seed", "patch_artifact_field", "core_premise", "field", `"新前提"`, "core_premise"},
		{"story seed entity", "story_seed", "patch_artifact_entity", "protagonist", "entity", `{"name":"新主角"}`, "protagonist"},
		{"story seed section", "story_seed", "patch_artifact_section", "main_plotline", "section", `[{"beat":"新主线"}]`, "main_plotline"},
		{"blueprint field", "series_blueprint", "patch_artifact_field", "series_promise", "field", `"新承诺"`, "series_promise"},
		{"blueprint entity", "series_blueprint", "patch_artifact_entity", "phase_plan[0]", "entity", `{"phase_id":"P1","phase_function":"新阶段"}`, "phase_plan"},
		{"blueprint section", "series_blueprint", "patch_artifact_section", "payoff_distribution", "section", `[{"episode":2}]`, "payoff_distribution"},
		{"episode card field", "episode_cards", "patch_artifact_field", "episodes[0].ending_hook", "field", `{"hook_text":"新尾钩"}`, "episodes"},
		{"episode card entity", "episode_cards", "patch_artifact_entity", "episodes[0]", "entity", `{"episode_id":1,"main_conflict":"新冲突"}`, "episodes"},
		{"episode card section", "episode_cards", "patch_artifact_section", "continuity_delta", "section", `{"new_facts":["新事实"]}`, "continuity_delta"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			note := "USER_REVISION_REQUEST:\nchange target\nREVISION_CONTEXT_PACK_JSON:\n" +
				`{"revision_intent":"` + testCase.intent + `","target":{"artifact_type":"` + testCase.artifactType + `","field_path":"` + testCase.fieldPath + `","scope":"` + testCase.scope + `"}}`
			response := `{"artifact_type":"` + testCase.artifactType + `","status":"pending_approval","field_path":"` + testCase.fieldPath + `","scope":"` + testCase.scope + `","patch_value":` + testCase.patchValue + `}`
			planned, err := parsePatchArtifact(response, testCase.artifactType, note)
			if err != nil {
				t.Fatalf("parse sparse patch: %v", err)
			}
			if len(planned.Payload) != 1 {
				t.Fatalf("expected only one root in sparse payload, got %#v", planned.Payload)
			}
			if _, ok := planned.Payload[testCase.root]; !ok {
				t.Fatalf("expected sparse root %s, got %#v", testCase.root, planned.Payload)
			}
		})
	}
}

func TestParseEntityPatchSupportsAppendAndDelete(t *testing.T) {
	note := `USER_REVISION_REQUEST:
change material entities
REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_entity","target":{"artifact_type":"material_bank","field_path":"conflict_materials","scope":"entity"}}`
	appendPatch, err := parsePatchArtifact(`{"artifact_type":"material_bank","status":"pending_approval","field_path":"conflict_materials","scope":"entity","operation":"append_entity","patch_value":{"id":"M2","summary":"新反派"}}`, "material_bank", note)
	if err != nil {
		t.Fatalf("parse append entity: %v", err)
	}
	marker, ok := appendPatch.Payload["__artifact_entity_patch"].(map[string]any)
	if !ok || marker["operation"] != "append_entity" || marker["field_path"] != "conflict_materials" {
		t.Fatalf("unexpected append marker: %#v", appendPatch.Payload)
	}

	deleteNote := `USER_REVISION_REQUEST:
delete material entity
REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_entity","target":{"artifact_type":"material_bank","field_path":"conflict_materials[0]","scope":"entity"}}`
	deletePatch, err := parsePatchArtifact(`{"artifact_type":"material_bank","status":"pending_approval","field_path":"conflict_materials[0]","scope":"entity","operation":"delete_entity","patch_value":null}`, "material_bank", deleteNote)
	if err != nil {
		t.Fatalf("parse delete entity: %v", err)
	}
	marker, ok = deletePatch.Payload["__artifact_entity_patch"].(map[string]any)
	if !ok || marker["operation"] != "delete_entity" || marker["field_path"] != "conflict_materials[0]" {
		t.Fatalf("unexpected delete marker: %#v", deletePatch.Payload)
	}
}

func TestPatchParserKeepsControllerTargetAuthoritative(t *testing.T) {
	note := `USER_REVISION_REQUEST:
change premise
REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_field","target":{"artifact_type":"story_seed","field_path":"core_premise","scope":"field"}}`
	planned, err := parsePatchArtifact(`{"artifact_type":"story_seed","status":"pending_approval","field_path":"logline","scope":"field","patch_value":"新前提"}`, "story_seed", note)
	if err != nil {
		t.Fatalf("parse field patch: %v", err)
	}
	if planned.Payload["core_premise"] != "新前提" {
		t.Fatalf("model field_path overrode controller target: %#v", planned.Payload)
	}
	if _, exists := planned.Payload["logline"]; exists {
		t.Fatalf("unexpected model-selected field in sparse patch: %#v", planned.Payload)
	}

	scriptNote := `USER_REVISION_REQUEST:
change selected line
REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_script_span","target":{"artifact_type":"script_unit","episode_id":"1","scene_id":"scene_1","node_id":"line_1","scope":"selection"},"focused_context":{"selected_text":"旧句"}}`
	scriptPatch, err := parsePatchArtifact(`{"artifact_type":"script_unit","status":"pending_approval","operation":"patch_script_span","episode_id":1,"scene_id":"scene_2","node_id":"line_2","old_text":"旧句","new_text":"新句"}`, "script_unit", scriptNote)
	if err != nil {
		t.Fatalf("parse script patch: %v", err)
	}
	span := scriptPatch.Payload["__script_span_patch"].(map[string]any)
	if span["scene_id"] != "scene_1" || span["node_id"] != "line_1" {
		t.Fatalf("model script target overrode controller target: %#v", span)
	}
}

func TestParseNamedSelectorPatchKeepsSelectorForRuntimeResolution(t *testing.T) {
	note := `USER_REVISION_REQUEST:
change protagonist goal
REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_field","target":{"artifact_type":"story_bible","field_path":"characters[name=protagonist].goal","scope":"field"}}`
	planned, err := parsePatchArtifact(`{"artifact_type":"story_bible","status":"pending_approval","field_path":"characters[name=protagonist].goal","scope":"field","patch_value":"新目标"}`, "story_bible", note)
	if err != nil {
		t.Fatalf("parse named selector patch: %v", err)
	}
	marker, ok := planned.Payload["__artifact_value_patch"].(map[string]any)
	if !ok || marker["field_path"] != "characters[name=protagonist].goal" || marker["patch_value"] != "新目标" {
		t.Fatalf("named selector was not preserved: %#v", planned.Payload)
	}
}

func TestScriptScenePatchRejectsDifferentScene(t *testing.T) {
	note := `USER_REVISION_REQUEST:
rewrite scene
REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"regenerate_script_scene","target":{"artifact_type":"script_unit","episode_id":"1","scene_id":"scene_1","scope":"scene"}}`
	_, err := parsePatchArtifact(`{"artifact_type":"script_unit","status":"pending_approval","operation":"patch_script_scene","scene_id":"scene_2","patch_value":{"scene_id":"scene_2","heading":"错场景","blocks":[]}}`, "script_unit", note)
	if err == nil {
		t.Fatal("expected mismatched scene patch to be rejected")
	}
}

func TestParseScriptSpanPatchReturnsSparseOperation(t *testing.T) {
	note := `USER_REVISION_REQUEST:
make it stronger
REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_script_span","target":{"artifact_type":"script_unit","episode_id":"3","scene_id":"scene_3_2","node_id":"scene_3_2-line-2","scope":"selection"},"focused_context":{"selected_text":"旧句"}}`
	planned, err := parsePatchArtifact(`{"artifact_type":"script_unit","status":"pending_approval","operation":"patch_script_span","episode_id":3,"scene_id":"scene_3_2","node_id":"scene_3_2-line-2","old_text":"旧句","new_text":"新句"}`, "script_unit", note)
	if err != nil {
		t.Fatalf("parse script span patch: %v", err)
	}
	patch, ok := planned.Payload["__script_span_patch"].(map[string]any)
	if !ok || patch["new_text"] != "新句" {
		t.Fatalf("expected sparse script patch, got %#v", planned.Payload)
	}
	if _, exists := planned.Payload["scenes"]; exists {
		t.Fatalf("script span patch must not contain full scenes: %#v", planned.Payload)
	}
}

func TestParseScriptRangePatchKeepsOrderedReplacementLines(t *testing.T) {
	note := `USER_REVISION_REQUEST:
make these lines stronger
REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_script_span","target":{"artifact_type":"script_unit","episode_id":"3","scope":"selection"},"focused_context":{"selected_text":"旧一\n旧二","start_line_id":"line_1","end_line_id":"line_2","line_ids":["line_1","line_2"],"selection_start":1,"selection_end":2}}`
	response := `{"artifact_type":"script_unit","status":"pending_approval","operation":"patch_script_range","episode_id":3,"start_line_id":"line_1","end_line_id":"line_2","old_text":"旧一\n旧二","replacement_lines":[{"line_id":"line_1","new_text":"新一"},{"line_id":"line_2","new_text":"新二"}]}`
	planned, err := parsePatchArtifact(response, "script_unit", note)
	if err != nil {
		t.Fatalf("parse script range patch: %v", err)
	}
	patch := planned.Payload["__script_span_patch"].(map[string]any)
	if len(patch["replacement_lines"].([]any)) != 2 || patch["selection_start"] != 1 || patch["selection_end"] != 2 {
		t.Fatalf("expected ordered sparse range patch, got %#v", patch)
	}
}

func TestParseScriptScenePatchReturnsOnlyTargetScene(t *testing.T) {
	note := `USER_REVISION_REQUEST:
rewrite this scene
REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"regenerate_script_scene","target":{"artifact_type":"script_unit","episode_id":"2","scene_id":"scene_2_3","scope":"scene"}}`
	planned, err := parsePatchArtifact(`{"artifact_type":"script_unit","status":"pending_approval","operation":"patch_script_scene","scene_id":"scene_2_3","patch_value":{"scene_id":"scene_2_3","heading":"新场景","blocks":[]}}`, "script_unit", note)
	if err != nil {
		t.Fatalf("parse script scene patch: %v", err)
	}
	patch, ok := planned.Payload["__script_scene_patch"].(map[string]any)
	if !ok || patch["scene_id"] != "scene_2_3" {
		t.Fatalf("expected one sparse scene patch, got %#v", planned.Payload)
	}
	if len(planned.Payload) != 1 {
		t.Fatalf("scene patch must not contain sibling scenes or full script: %#v", planned.Payload)
	}
}

func TestArtifactDigestKeepsOnlyCurrentEffectiveArtifacts(t *testing.T) {
	base := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	digest := artifactDigest([]agent.Artifact{
		{
			ArtifactID:   "story_old",
			ArtifactType: "story_bible",
			Status:       agent.ArtifactSuperseded,
			Version:      1,
			UpdatedAt:    base,
			Payload:      map[string]any{"goal": "old"},
		},
		{
			ArtifactID:   "story_current",
			ArtifactType: "story_bible",
			Status:       agent.ArtifactConfirmed,
			Version:      1,
			UpdatedAt:    base.Add(time.Minute),
			Payload:      map[string]any{"goal": "current"},
		},
		{
			ArtifactID:   "cards_old",
			ArtifactType: "episode_cards",
			Status:       agent.ArtifactPendingApproval,
			Version:      1,
			UpdatedAt:    base,
			Payload:      map[string]any{"version": "old"},
		},
		{
			ArtifactID:   "cards_current",
			ArtifactType: "episode_cards",
			Status:       agent.ArtifactPendingApproval,
			Version:      2,
			UpdatedAt:    base.Add(time.Minute),
			Payload:      map[string]any{"version": "current"},
		},
		{
			ArtifactID:   "script_ep1",
			ArtifactType: "script_unit",
			Status:       agent.ArtifactConfirmed,
			Version:      1,
			UpdatedAt:    base,
			Payload:      map[string]any{"episode_id": 1},
		},
		{
			ArtifactID:   "script_ep2",
			ArtifactType: "script_unit",
			Status:       agent.ArtifactConfirmed,
			Version:      1,
			UpdatedAt:    base,
			Payload:      map[string]any{"episode_id": 2},
		},
	})

	ids := map[string]bool{}
	for _, item := range digest {
		ids[item["artifact_id"].(string)] = true
	}
	if ids["story_old"] || ids["cards_old"] {
		t.Fatalf("digest should not include stale artifacts: %#v", ids)
	}
	for _, want := range []string{"story_current", "cards_current", "script_ep1", "script_ep2"} {
		if !ids[want] {
			t.Fatalf("digest missing %s: %#v", want, ids)
		}
	}
}
