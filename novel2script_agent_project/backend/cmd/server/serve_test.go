package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"novel2script-agent/backend/internal/agent"
	mock "novel2script-agent/backend/internal/agent/runtime"
	"novel2script-agent/backend/internal/llm"
	"novel2script-agent/backend/internal/mainagent"
	"novel2script-agent/backend/internal/worker"
)

type sourceReaderTestDouble struct {
	documents []worker.SourceDocument
	request   string
}

func TestServerAddressCanBeOverriddenForParallelRuntimeValidation(t *testing.T) {
	t.Setenv("N2S_ADDR", "127.0.0.1:8841")
	if got := serverAddress(); got != "127.0.0.1:8841" {
		t.Fatalf("expected configured server address, got %q", got)
	}
	t.Setenv("N2S_ADDR", "")
	if got := serverAddress(); got != "127.0.0.1:8831" {
		t.Fatalf("expected default server address, got %q", got)
	}
}

func TestCORSAllowsCurrentPackagedServerOrigin(t *testing.T) {
	t.Setenv("N2S_ADDR", "127.0.0.1:8842")
	if !isAllowedLocalOrigin("http://127.0.0.1:8842") {
		t.Fatal("current packaged server origin should be allowed")
	}
	if isAllowedLocalOrigin("http://example.com") {
		t.Fatal("external origin should not be allowed")
	}
}

func TestShutdownRequiresConfiguredToken(t *testing.T) {
	requested := make(chan struct{}, 1)
	server := &apiServer{shutdownToken: "test-token", shutdown: func() { requested <- struct{}{} }}
	for _, testCase := range []struct {
		token  string
		status int
	}{
		{token: "wrong", status: http.StatusNotFound},
		{token: "test-token", status: http.StatusAccepted},
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/system/shutdown", nil)
		request.Header.Set(shutdownTokenHeader, testCase.token)
		response := httptest.NewRecorder()
		server.shutdownService(response, request)
		if response.Code != testCase.status {
			t.Fatalf("token %q: expected status %d, got %d", testCase.token, testCase.status, response.Code)
		}
	}
	select {
	case <-requested:
	case <-time.After(time.Second):
		t.Fatal("authorized shutdown was not requested")
	}
}

func TestWriteErrorHidesSQLiteBusyDetails(t *testing.T) {
	response := httptest.NewRecorder()
	writeError(response, http.StatusInternalServerError, fmt.Errorf("save project: database is locked (5) (SQLITE_BUSY)"))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected service unavailable, got %d", response.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	errorPayload := payload["error"].(map[string]any)
	if errorPayload["code"] != "WORKSPACE_BUSY" || strings.Contains(fmt.Sprint(errorPayload["message"]), "SQLITE") {
		t.Fatalf("raw database details leaked to the user: %#v", errorPayload)
	}
}

func TestWriteErrorHidesInternalServiceDetails(t *testing.T) {
	response := httptest.NewRecorder()
	writeError(response, http.StatusInternalServerError, fmt.Errorf(`open C:\Users\developer\workspace.db: database connection failed`))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected internal server error, got %d", response.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	errorPayload := payload["error"].(map[string]any)
	message := fmt.Sprint(errorPayload["message"])
	if strings.Contains(strings.ToLower(message), "database") || strings.Contains(strings.ToLower(message), `c:\users\`) {
		t.Fatalf("internal service details leaked to the user: %#v", errorPayload)
	}
}

func TestSafeModelErrorClassifiesTimeout(t *testing.T) {
	message := safeModelError(fmt.Errorf("upstream status=504: context deadline exceeded"))
	if message != "模型服务超时，请稍后重试。" {
		t.Fatalf("unexpected timeout message: %q", message)
	}
}

func TestEinoIsDefaultRuntimeWithNativeRollback(t *testing.T) {
	t.Setenv("N2S_RUNTIME", "")
	if got := selectedRuntimeName(); got != "eino" {
		t.Fatalf("expected Eino default runtime, got %q", got)
	}
	t.Setenv("N2S_RUNTIME", "native")
	if got := selectedRuntimeName(); got != "native" {
		t.Fatalf("expected explicit native rollback runtime, got %q", got)
	}
}

func (r *sourceReaderTestDouble) AnalyzeSources(_ context.Context, request string, documents []worker.SourceDocument) (string, error) {
	r.request = request
	r.documents = append([]worker.SourceDocument(nil), documents...)
	return "已完成附件分析。", nil
}

func TestProjectMessageAnalyzesPersistedAttachmentWithoutStartingRun(t *testing.T) {
	controllerClient := &patchTestClient{response: `{
		"intent":"inspect_source",
		"confidence":0.97,
		"next_action":"inspect_source",
		"agent_reply":"正在分析附件。",
		"requires_approval":false,
		"reason":"source_analysis",
		"target_file_ids":["file_1"]
	}`}
	reader := &sourceReaderTestDouble{}
	server := &apiServer{
		mainAgent:    mainagent.New(controllerClient, "control-model"),
		sourceReader: reader,
		projects: map[string]projectDTO{
			"project_1": {ProjectID: "project_1", Title: "作品 1", SourceMode: agent.SourceModeAuto, Status: projectIdle},
		},
		messages: map[string][]messageDTO{},
		files: map[string]fileDTO{
			"file_1": {FileID: "file_1", ProjectID: "project_1", Filename: "材料.txt", MimeType: "text/plain", TextPreview: "附件摘要", TextContent: "附件完整正文"},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{project_id}/messages", server.projectMessage)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/project_1/messages", bytes.NewBufferString(`{"content":"分析一下刚才的文件"}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if reader.request != "分析一下刚才的文件" || len(reader.documents) != 1 || reader.documents[0].Text != "附件完整正文" {
		t.Fatalf("content model did not receive the persisted selected file: request=%q docs=%#v", reader.request, reader.documents)
	}
	if server.projects["project_1"].ActiveRunID != "" || server.projects["project_1"].Status != projectIdle {
		t.Fatalf("source analysis must not start a run: %#v", server.projects["project_1"])
	}
	if len(server.messages["project_1"]) != 2 || server.messages["project_1"][1].Content != "已完成附件分析。" {
		t.Fatalf("analysis conversation was not persisted: %#v", server.messages["project_1"])
	}
}

func TestProjectMessagePersistsDisplayContentWhileDecidingFromFullRequest(t *testing.T) {
	controller := &patchTestClient{response: `{"intent":"chat_idle","confidence":0.9,"next_action":"reply","source_mode":"novel","agent_reply":"已收到配置。","reason":"ack"}`}
	server := &apiServer{
		mainAgent: mainagent.New(controller, "control-model"),
		projects:  map[string]projectDTO{"project_1": {ProjectID: "project_1", Title: "作品 1", SourceMode: agent.SourceModeNovel, Status: projectIdle}},
		messages:  map[string][]messageDTO{}, files: map[string]fileDTO{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{project_id}/messages", server.projectMessage)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/project_1/messages", bytes.NewBufferString(`{"content":"把这个小说改成短剧\n\n确认生成配置：2集，每集1.5分钟","display_content":"确认生成配置：2集，每集1.5分钟"}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if got := server.messages["project_1"][0].Content; got != "确认生成配置：2集，每集1.5分钟" {
		t.Fatalf("expected persisted display content, got %q", got)
	}
	if len(controller.request.Messages) < 2 || !strings.Contains(controller.request.Messages[1].Content, "把这个小说改成短剧") {
		t.Fatalf("controller must still receive full request semantics: %#v", controller.request.Messages)
	}
}

func TestProjectFileIndexIsLightweightAndSourceSelectionReadsOnDemand(t *testing.T) {
	longText := strings.Repeat("正文", 500)
	server := &apiServer{
		files: map[string]fileDTO{
			"file_1": {
				FileID: "file_1", ProjectID: "project_1", Filename: "材料.txt", MimeType: "text/plain",
				SizeBytes: int64(len(longText)), TextPreview: longText, TextContent: longText,
			},
		},
	}

	index := server.projectFileIndex("project_1")
	if len(index) != 1 || index[0].FileID != "file_1" {
		t.Fatalf("unexpected project file index: %#v", index)
	}
	if index[0].TextPreview != "" {
		t.Fatalf("project context must not expose source text before an explicit read intent, got %d runes", len([]rune(index[0].TextPreview)))
	}

	documents, issue := server.sourceDocumentsForAnalysis(mainagent.MessageRequest{
		ProjectID: "project_1", Message: "分析一下刚才的文件",
	}, mainagent.Decision{NextAction: mainagent.ActionInspectSource})
	if issue != "" || len(documents) != 1 {
		t.Fatalf("single project file should resolve automatically, issue=%q docs=%#v", issue, documents)
	}
	if documents[0].Text != longText {
		t.Fatal("selected source should be read in full only after inspect_source")
	}
}

func TestMessageForDecisionDoesNotReadAttachmentForOrdinaryChat(t *testing.T) {
	req := mainagent.MessageRequest{Message: "你好", Attachments: []mainagent.FileAttachment{{FileName: "材料.txt", Size: 12, TextContent: "不应交给主控的正文"}}}
	message := messageForDecision(req, "")
	if strings.Contains(message, "不应交给主控的正文") || !strings.Contains(message, "材料.txt") {
		t.Fatalf("ordinary chat should receive only attachment metadata, got %q", message)
	}
	req.Message = "分析这个附件"
	message = messageForDecision(req, "")
	if !strings.Contains(message, "不应交给主控的正文") {
		t.Fatalf("explicit inspection should include a bounded source excerpt, got %q", message)
	}
}

func TestGenerationSourceInputSeparatesInstructionAndAttachmentText(t *testing.T) {
	server := &apiServer{}
	text, files, err := server.generationSourceInput(mainagent.MessageRequest{
		Message: "生成剧本", Attachments: []mainagent.FileAttachment{{FileID: "file_1", FileName: "小说.txt", TextContent: "真正的小说正文"}},
	}, "生成剧本\n\n【附件：小说.txt】\n真正的小说正文", nil)
	if err != nil {
		t.Fatal(err)
	}
	if text != "真正的小说正文" || strings.Contains(text, "生成剧本") || len(files) != 1 || files[0].FileName != "小说.txt" {
		t.Fatalf("generation source was not separated: text=%q files=%#v", text, files)
	}
}

func TestControlModelAttachmentCompactionDoesNotTruncateStartedRunSource(t *testing.T) {
	fullText := strings.Repeat("这是完整小说正文。", 800)
	controller := &patchTestClient{response: `{
		"intent":"generate_from_novel","confidence":0.98,"next_action":"start_run","source_mode":"novel",
		"agent_reply":"开始生成。","target_file_ids":["file_long"],
		"generation_config":{"target_episode_count":2,"episode_duration_minutes":1},"reason":"explicit_generation"
	}`}
	runtime := mock.NewRuntime(staticTestWorker{})
	server := &apiServer{
		runtime: runtime, mainAgent: mainagent.New(controller, "control-model"),
		projects: map[string]projectDTO{"project_long": {ProjectID: "project_long", Title: "长篇", SourceMode: agent.SourceModeNovel}},
		messages: map[string][]messageDTO{}, files: map[string]fileDTO{},
	}
	response, status, err := server.executeAgentMessage(context.Background(), mainagent.MessageRequest{
		ProjectID: "project_long", Message: "根据附件生成剧本", SourceMode: agent.SourceModeNovel,
		Attachments:      []mainagent.FileAttachment{{FileID: "file_long", FileName: "长篇.txt", TextContent: fullText}},
		GenerationConfig: &agent.GenerationConfig{TargetEpisodeCount: 2, EpisodeDurationMinutes: 1},
	})
	if err != nil || status != http.StatusOK || response.Run == nil {
		t.Fatalf("start run failed: status=%d response=%#v err=%v", status, response, err)
	}
	var source agent.Artifact
	for _, artifact := range response.Artifacts {
		if artifact.ArtifactType == "source_input" {
			source = artifact
		}
	}
	if source.Payload["text"] != fullText || strings.Contains(fmt.Sprint(source.Payload["text"]), "...[truncated]") {
		t.Fatalf("started run source was truncated: got=%d want=%d", len([]rune(fmt.Sprint(source.Payload["text"]))), len([]rune(fullText)))
	}
	if len(controller.request.Messages) < 2 || !strings.Contains(controller.request.Messages[1].Content, "...[truncated]") {
		t.Fatal("control-model request should still use bounded attachment context")
	}
}

func TestGenerationSourceInputDoesNotReadUnselectedProjectFiles(t *testing.T) {
	server := &apiServer{files: map[string]fileDTO{
		"file_1": {FileID: "file_1", ProjectID: "project_1", Filename: "one.txt", TextContent: "first source"},
		"file_2": {FileID: "file_2", ProjectID: "project_1", Filename: "two.txt", TextContent: "second source"},
	}}

	text, files, err := server.generationSourceInput(mainagent.MessageRequest{ProjectID: "project_1"}, "generate from this instruction", nil)
	if err != nil {
		t.Fatal(err)
	}
	if text != "generate from this instruction" || len(files) != 0 {
		t.Fatalf("unselected project files must not be read: text=%q files=%#v", text, files)
	}

	text, files, err = server.generationSourceInput(mainagent.MessageRequest{ProjectID: "project_1"}, "ignored instruction", []string{"file_2"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "second source" || len(files) != 1 || files[0].FileID != "file_2" {
		t.Fatalf("generation must read only the selected file: text=%q files=%#v", text, files)
	}
}

func TestGenerationTargetFileIDsRequiresUnambiguousStoredFile(t *testing.T) {
	server := &apiServer{files: map[string]fileDTO{
		"file_1": {FileID: "file_1", ProjectID: "project_1", Filename: "one.txt"},
		"file_2": {FileID: "file_2", ProjectID: "project_1", Filename: "two.txt"},
	}}
	ids, issue := server.generationTargetFileIDs(mainagent.MessageRequest{ProjectID: "project_1", Message: "根据刚才上传的附件生成短剧"}, nil)
	if len(ids) != 0 || !strings.Contains(issue, "多个附件") {
		t.Fatalf("ambiguous stored files must be clarified: ids=%#v issue=%q", ids, issue)
	}

	delete(server.files, "file_2")
	ids, issue = server.generationTargetFileIDs(mainagent.MessageRequest{ProjectID: "project_1", Message: "把这个文件改成短剧"}, nil)
	if issue != "" || len(ids) != 1 || ids[0] != "file_1" {
		t.Fatalf("one explicitly referenced stored file should be selected: ids=%#v issue=%q", ids, issue)
	}

	ids, issue = server.generationTargetFileIDs(mainagent.MessageRequest{ProjectID: "project_1", Message: "另写一个全新的职场故事"}, nil)
	if issue != "" || len(ids) != 0 {
		t.Fatalf("unreferenced project files must stay out of a new idea: ids=%#v issue=%q", ids, issue)
	}
}

func TestResolveApprovalPersistsButtonConversation(t *testing.T) {
	runtime := mock.NewRuntime(staticTestWorker{})
	started, err := runtime.StartRunAsyncContext(context.Background(), agent.StartRunRequest{
		ProjectID: "project_approval_chat", UserMessage: "生成剧本", SourceText: "素材正文", SourceMode: agent.SourceModeNonNovel,
	})
	if err != nil {
		t.Fatal(err)
	}
	var approval agent.ApprovalRequest
	for attempt := 0; attempt < 80; attempt++ {
		if current, ok := runtime.CurrentApproval(started.Run.RunID); ok {
			approval = *current
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if approval.ApprovalRequestID == "" {
		t.Fatal("expected an approval request")
	}
	server := &apiServer{
		runtime:  runtime,
		projects: map[string]projectDTO{"project_approval_chat": {ProjectID: "project_approval_chat", Title: "作品", SourceMode: agent.SourceModeNonNovel, Status: projectWaitingApproval, ActiveRunID: started.Run.RunID}},
		messages: map[string][]messageDTO{}, files: map[string]fileDTO{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/approvals/{approval_request_id}/resolve", server.resolveApproval)
	request := httptest.NewRequest(http.MethodPost, "/api/approvals/"+approval.ApprovalRequestID+"/resolve", bytes.NewBufferString(`{"action":"pause"}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	messages := server.messages["project_approval_chat"]
	if len(messages) != 2 || messages[0].Content != "暂停" || !strings.Contains(messages[1].Content, "已暂停") {
		t.Fatalf("approval button conversation was not persisted: %#v", messages)
	}
}

func TestSourceSelectionAsksWhenMultipleFilesAreAmbiguous(t *testing.T) {
	server := &apiServer{files: map[string]fileDTO{
		"file_1": {FileID: "file_1", ProjectID: "project_1", Filename: "甲.txt", TextContent: "甲"},
		"file_2": {FileID: "file_2", ProjectID: "project_1", Filename: "乙.txt", TextContent: "乙"},
	}}

	documents, issue := server.sourceDocumentsForAnalysis(mainagent.MessageRequest{ProjectID: "project_1"}, mainagent.Decision{})
	if len(documents) != 0 || !strings.Contains(issue, "多个附件") {
		t.Fatalf("ambiguous source selection must ask the user, issue=%q docs=%#v", issue, documents)
	}

	documents, issue = server.sourceDocumentsForAnalysis(mainagent.MessageRequest{ProjectID: "project_1"}, mainagent.Decision{
		TargetFileIDs: []string{"file_2"},
	})
	if issue != "" || len(documents) != 1 || documents[0].FileID != "file_2" || documents[0].Text != "乙" {
		t.Fatalf("explicit file selection should read only its target, issue=%q docs=%#v", issue, documents)
	}
}

func TestArtifactUpdateAPIRequiresBaseVersionAndReturnsConflict(t *testing.T) {
	runtime := mock.NewRuntime(staticTestWorker{})
	started, err := runtime.StartRunAsyncContext(context.Background(), agent.StartRunRequest{
		ProjectID: "project_artifact_conflict", SourceMode: agent.SourceModeNonNovel, UserMessage: "create a story",
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	artifacts := runtime.Artifacts(started.Run.RunID)
	if len(artifacts) == 0 {
		t.Fatal("expected a source artifact")
	}
	target := artifacts[0]
	server := &apiServer{runtime: runtime}
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /api/artifacts/{artifact_id}", server.updateArtifact)
	for attempt := 0; attempt < 300; attempt++ {
		current, _ := runtime.GetRun(started.Run.RunID)
		if current.Status != agent.RunRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	missingVersion := httptest.NewRecorder()
	mux.ServeHTTP(missingVersion, httptest.NewRequest(http.MethodPatch, "/api/artifacts/"+target.ArtifactID, bytes.NewBufferString(`{"payload":{"value":"missing version"}}`)))
	if missingVersion.Code != http.StatusBadRequest {
		t.Fatalf("expected missing base_version to return 400, got %d: %s", missingVersion.Code, missingVersion.Body.String())
	}

	requestBody := func(value string) *bytes.Buffer {
		body, marshalErr := json.Marshal(map[string]any{"base_version": target.Version, "payload": map[string]any{"value": value}})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return bytes.NewBuffer(body)
	}
	first := httptest.NewRecorder()
	mux.ServeHTTP(first, httptest.NewRequest(http.MethodPatch, "/api/artifacts/"+target.ArtifactID, requestBody("first save")))
	if first.Code != http.StatusOK {
		t.Fatalf("expected first save to return 200, got %d: %s", first.Code, first.Body.String())
	}

	stale := httptest.NewRecorder()
	mux.ServeHTTP(stale, httptest.NewRequest(http.MethodPatch, "/api/artifacts/"+target.ArtifactID, requestBody("stale overwrite")))
	if stale.Code != http.StatusConflict {
		t.Fatalf("expected stale save to return 409, got %d: %s", stale.Code, stale.Body.String())
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				CurrentArtifact agent.Artifact `json:"current_artifact"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stale.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if payload.Error.Code != "CONFLICT" || payload.Error.Details.CurrentArtifact.Payload["value"] != "first save" {
		t.Fatalf("expected conflict to include current artifact, got %s", stale.Body.String())
	}
}

func TestArtifactUpdateAPIRejectsEditsWhileRunIsGenerating(t *testing.T) {
	runtime := mock.NewRuntime(staticTestWorker{})
	started, err := runtime.StartRunAsyncContext(context.Background(), agent.StartRunRequest{
		ProjectID: "project_running_edit", SourceMode: agent.SourceModeNonNovel, UserMessage: "生成剧本",
	})
	if err != nil {
		t.Fatal(err)
	}
	target := runtime.Artifacts(started.Run.RunID)[0]
	server := &apiServer{runtime: runtime}
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /api/artifacts/{artifact_id}", server.updateArtifact)
	body, _ := json.Marshal(map[string]any{"base_version": target.Version, "payload": map[string]any{"text": "运行中不应保存"}})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/api/artifacts/"+target.ArtifactID, bytes.NewReader(body)))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "RUN_ACTIVE_EDIT_LOCKED") {
		t.Fatalf("running artifact edit must be locked, got %d: %s", response.Code, response.Body.String())
	}
	current, _ := runtime.ArtifactByID(target.ArtifactID)
	if current.Version != target.Version || current.Payload["text"] == "运行中不应保存" {
		t.Fatalf("locked edit changed the artifact: %#v", current)
	}
}

func TestArtifactUpdateAPIRejectsInvalidArtifactShape(t *testing.T) {
	runtime := mock.NewRuntime(fastLifecycleWorker{})
	started, err := runtime.StartRunAsyncContext(context.Background(), agent.StartRunRequest{
		ProjectID: "project_invalid_edit", SourceMode: agent.SourceModeNonNovel, UserMessage: "生成剧本",
		GenerationConfig: &agent.GenerationConfig{TargetEpisodeCount: 2, EpisodeDurationMinutes: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	var target agent.Artifact
	for attempt := 0; attempt < 200; attempt++ {
		for _, artifact := range runtime.Artifacts(started.Run.RunID) {
			if artifact.ArtifactType == "material_bank" {
				target = artifact
			}
		}
		if target.ArtifactID != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if target.ArtifactID == "" {
		t.Fatal("material_bank artifact was not generated")
	}
	server := &apiServer{runtime: runtime}
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /api/artifacts/{artifact_id}", server.updateArtifact)
	body, _ := json.Marshal(map[string]any{"base_version": target.Version, "payload": map[string]any{"only": "invalid"}})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/api/artifacts/"+target.ArtifactID, bytes.NewReader(body)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "VALIDATION_FAILED") {
		t.Fatalf("invalid artifact edit must return validation error, got %d: %s", response.Code, response.Body.String())
	}
	current, ok := runtime.ArtifactByID(target.ArtifactID)
	if !ok || current.Status == agent.ArtifactSuperseded {
		t.Fatalf("rejected edit must leave current artifact untouched: %#v", current)
	}
}

func TestCORSAcceptsOnlyLocalFrontendOrigins(t *testing.T) {
	t.Setenv("N2S_ALLOWED_ORIGINS", "https://demo.novel2script.click, https://review.example.org/")
	handler := withCORS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, testCase := range []struct {
		origin string
		status int
		allow  string
	}{
		{"http://127.0.0.1:8832", http.StatusOK, "http://127.0.0.1:8832"},
		{"http://localhost:8832", http.StatusOK, "http://localhost:8832"},
		{"https://demo.novel2script.click", http.StatusOK, "https://demo.novel2script.click"},
		{"https://review.example.org", http.StatusOK, "https://review.example.org"},
		{"https://example.com", http.StatusForbidden, ""},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		request.Header.Set("Origin", testCase.origin)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != testCase.status || recorder.Header().Get("Access-Control-Allow-Origin") != testCase.allow {
			t.Fatalf("origin %s: status=%d allow=%q", testCase.origin, recorder.Code, recorder.Header().Get("Access-Control-Allow-Origin"))
		}
	}
}

func TestEventResumeReplaysAllWhenLastEventIDIsUnknown(t *testing.T) {
	events := []agent.RunEvent{{EventID: "event_1"}, {EventID: "event_2"}, {EventID: "event_3"}}
	if sent := eventIDsThrough(events, "event_missing"); len(sent) != 0 {
		t.Fatalf("unknown resume cursor must replay all events, marked sent=%#v", sent)
	}
	sent := eventIDsThrough(events, "event_2")
	if !sent["event_1"] || !sent["event_2"] || sent["event_3"] {
		t.Fatalf("known cursor should skip only events through cursor, got %#v", sent)
	}
}

func TestPublicRouterRejectsLegacyRunBypassEndpoints(t *testing.T) {
	server := &apiServer{runtime: mock.NewRuntime(staticTestWorker{}), projects: map[string]projectDTO{}, messages: map[string][]messageDTO{}, files: map[string]fileDTO{}}
	handler := newAPIHandler(server)
	for _, path := range []string{"/api/agent/messages", "/api/runs", "/api/runs/run_1/continue", "/api/files/file_1"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusMethodNotAllowed && response.Code != http.StatusNotFound {
			t.Fatalf("legacy endpoint %s remains publicly executable: status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestPublicAPIRouteMatrixIsRegistered(t *testing.T) {
	server := &apiServer{
		runtime:  mock.NewRuntime(fastLifecycleWorker{}),
		projects: map[string]projectDTO{}, messages: map[string][]messageDTO{}, files: map[string]fileDTO{},
	}
	handler := newAPIHandler(server)
	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/healthz", ""},
		{http.MethodGet, "/api/projects", ""},
		{http.MethodPost, "/api/projects", `{}`},
		{http.MethodGet, "/api/projects/missing", ""},
		{http.MethodDelete, "/api/projects/missing", ""},
		{http.MethodGet, "/api/projects/missing/messages", ""},
		{http.MethodPost, "/api/projects/missing/messages", `{}`},
		{http.MethodGet, "/api/projects/missing/files", ""},
		{http.MethodPost, "/api/projects/missing/files", `{}`},
		{http.MethodDelete, "/api/files/missing", ""},
		{http.MethodPatch, "/api/artifacts/missing", `{"base_version":1,"payload":{"value":1}}`},
		{http.MethodPost, "/api/approvals/missing/resolve", `{}`},
		{http.MethodGet, "/api/runs/missing", ""},
		{http.MethodPost, "/api/runs/missing/pause", `{}`},
		{http.MethodPost, "/api/runs/missing/resume", `{}`},
		{http.MethodPost, "/api/runs/missing/steps/step_x/rerun", `{}`},
		{http.MethodGet, "/api/runs/missing/events", ""},
		{http.MethodGet, "/api/runs/missing/events/stream", ""},
		{http.MethodGet, "/api/runs/missing/artifacts", ""},
		{http.MethodPost, "/api/system/shutdown", ""},
	}
	for _, testCase := range tests {
		request := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(testCase.body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code == http.StatusMethodNotAllowed {
			t.Errorf("route is not registered: %s %s", testCase.method, testCase.path)
		}
	}
}

func TestRequestLimitReturnsStructured413(t *testing.T) {
	handler := withRequestLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := decodeJSON(r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, payload)
	}), 32)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(`{"content":"this payload is intentionally larger than thirty two bytes"}`))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Error struct {
			Code        string `json:"code"`
			Recoverable bool   `json:"recoverable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != "REQUEST_TOO_LARGE" || response.Error.Recoverable {
		t.Fatalf("unexpected structured error: %#v", response.Error)
	}
}

func TestLongNovelContextRemainsBounded(t *testing.T) {
	longText := strings.Repeat("这是用于验证长篇小说上下文预算的正文。", 50000)
	payload := map[string]any{
		"source_text": longText,
		"episodes": []any{
			map[string]any{"episode_id": 1, "summary": longText},
			map[string]any{"episode_id": 2, "summary": longText},
		},
	}

	compacted, err := json.Marshal(compactMap(payload, 3))
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted) > 20_000 {
		t.Fatalf("long novel context exceeded budget: %d bytes", len(compacted))
	}
	if !bytes.Contains(compacted, []byte("...[truncated]")) {
		t.Fatal("expected long source text to be summarized before entering control-model context")
	}

	focused := focusedContextFromRequest(mainagent.MessageRequest{
		SelectedText: longText,
		SelectionContext: map[string]any{
			"before_context": longText,
			"after_context":  longText,
		},
	}, nil)
	if focused == nil || len([]rune(focused.SelectedText)) > 12020 || len([]rune(focused.BeforeText)) > 620 || len([]rune(focused.AfterText)) > 620 {
		t.Fatalf("focused context is not bounded: %#v", focused)
	}
}

func TestFocusedContextPreservesMultiLineScriptRange(t *testing.T) {
	focused := focusedContextFromRequest(mainagent.MessageRequest{SelectionContext: map[string]any{
		"artifact_id": "script_3", "artifact_type": "script_unit", "episode_id": 3,
		"start_scene_id": "scene_3_1", "end_scene_id": "scene_3_2",
		"start_line_id": "line_1", "end_line_id": "line_3", "line_ids": []any{"line_1", "line_2", "line_3"},
		"selection_scope": "range", "selection_start": 2, "selection_end": 4, "selected_text": "多行文本",
	}}, nil)
	if focused == nil || focused.SelectionScope != "range" || focused.StartLineID != "line_1" || focused.EndLineID != "line_3" {
		t.Fatalf("multi-line selection range was lost: %#v", focused)
	}
	if len(focused.LineIDs) != 3 || focused.SelectionStart != 2 || focused.SelectionEnd != 4 {
		t.Fatalf("multi-line selection offsets were lost: %#v", focused)
	}
}

func TestRevisionContextTargetsRequestedArtifactAndUsesItsUpstream(t *testing.T) {
	artifacts := []agent.Artifact{
		{ArtifactID: "artifact_source", ArtifactType: "source_input", Status: agent.ArtifactConfirmed, Version: 1, Payload: map[string]any{"text": "source"}},
		{ArtifactID: "artifact_bible", ArtifactType: "story_bible", Status: agent.ArtifactConfirmed, Version: 2, Payload: map[string]any{"characters": []any{map[string]any{"goal": "old"}}}},
		{ArtifactID: "artifact_scripts", ArtifactType: "scripts", Status: agent.ArtifactConfirmed, Version: 1, Payload: map[string]any{"episode_count": 2}},
	}
	decision := mainagent.Decision{
		SourceMode:     agent.SourceModeNovel,
		RevisionIntent: mainagent.RevisionIntentPatchArtifactField,
		RevisionTarget: &mainagent.RevisionTarget{ArtifactType: "story_bible", FieldPath: "characters[0].goal", Scope: "field"},
	}
	ctx := mainagent.Context{
		Run:                &agent.Run{SourceMode: agent.SourceModeNovel, Status: agent.RunCompleted},
		CurrentStepContext: &mainagent.CurrentStepContext{ArtifactID: "artifact_scripts", ArtifactType: "scripts"},
	}

	pack := buildRevisionContextPack(mainagent.MessageRequest{Message: "修改主角目标"}, decision, ctx, artifacts)
	if pack.Target.ArtifactID != "artifact_bible" || pack.TargetArtifact == nil || pack.TargetArtifact.ArtifactType != "story_bible" {
		t.Fatalf("revision resolved the wrong artifact: target=%+v snapshot=%+v", pack.Target, pack.TargetArtifact)
	}
	if len(pack.RequiredUpstream) != 1 || pack.RequiredUpstream[0].ArtifactType != "source_input" {
		t.Fatalf("story_bible revision has wrong upstream context: %+v", pack.RequiredUpstream)
	}
}

func TestFocusedScriptContextDerivesNeighboringLinesFromArtifact(t *testing.T) {
	/* Replaced below because the original fixture was corrupted by a shell encoding conversion.
		artifact := agent.Artifact{ArtifactID: "script_8", ArtifactType: "script_unit", Payload: map[string]any{
			"episode_id": 8,
			"scenes": []any{map[string]any{"scene_id": "scene_8_1", "blocks": []any{
				map[string]any{"block_type": "action", "text": "前两行动作"},
				map[string]any{"block_type": "dialogue", "speaker": "甲", "text": "前一句"},
				map[string]any{"block_type": "dialogue", "speaker": "乙", "text": "目标句"},
				map[string]any{"block_type": "action", "text": "后一个动作"},
				map[string]any{"block_type": "dialogue", "speaker": "丙", "text": "后一句"},
			}},
		}}
		focused := focusedContextFromRequest(mainagent.MessageRequest{
			SelectedArtifactID: "script_8", SelectedText: "目标句",
			SelectionContext: map[string]any{"artifact_type": "script_unit", "episode_id": "8", "scene_id": "scene_8_1", "node_id": "scene_8_1-line-3"},
		}, []agent.Artifact{artifact})
		if focused == nil || !strings.Contains(focused.BeforeText, "甲：前一句") || !strings.Contains(focused.AfterText, "丙：后一句") {
			t.Fatalf("script selection did not derive local neighbors: %#v", focused)
		}
	}

	*/
	artifact := agent.Artifact{
		ArtifactID:   "script_8",
		ArtifactType: "script_unit",
		Payload: map[string]any{
			"episode_id": 8,
			"scenes": []any{
				map[string]any{
					"scene_id": "scene_8_1",
					"blocks": []any{
						map[string]any{"block_type": "action", "text": "Earlier action."},
						map[string]any{"block_type": "dialogue", "speaker": "A", "text": "Previous line."},
						map[string]any{"block_type": "dialogue", "speaker": "B", "text": "Target line."},
						map[string]any{"block_type": "action", "text": "Following action."},
						map[string]any{"block_type": "dialogue", "speaker": "C", "text": "Next line."},
					},
				},
			},
		},
	}
	focused := focusedContextFromRequest(mainagent.MessageRequest{
		SelectedArtifactID: "script_8",
		SelectedText:       "Target line.",
		SelectionContext: map[string]any{
			"artifact_type": "script_unit",
			"episode_id":    "8",
			"scene_id":      "scene_8_1",
			"node_id":       "scene_8_1-line-3",
		},
	}, []agent.Artifact{artifact})
	if focused == nil || !strings.Contains(focused.BeforeText, "Previous line.") || !strings.Contains(focused.AfterText, "Next line.") {
		t.Fatalf("script selection did not derive local neighbors: %#v", focused)
	}
}

func TestRevisionContextResolvesRequestedScriptEpisode(t *testing.T) {
	artifacts := []agent.Artifact{
		{ArtifactID: "script_1", ArtifactType: "script_unit", Status: agent.ArtifactConfirmed, Version: 1, Payload: map[string]any{"episode_id": 1, "script_text": "第一集"}},
		{ArtifactID: "script_2", ArtifactType: "script_unit", Status: agent.ArtifactConfirmed, Version: 1, Payload: map[string]any{"episode_id": 2, "script_text": "第二集"}},
	}
	decision := mainagent.Decision{
		SourceMode:     agent.SourceModeNovel,
		RevisionIntent: mainagent.RevisionIntentRegenerateScriptEpisode,
		RevisionTarget: &mainagent.RevisionTarget{ArtifactType: "script_unit", EpisodeID: "1", Scope: "episode"},
	}
	pack := buildRevisionContextPack(mainagent.MessageRequest{Message: "重写第一集"}, decision, mainagent.Context{Run: &agent.Run{SourceMode: agent.SourceModeNovel}}, artifacts)
	if pack.Target.ArtifactID != "script_1" || pack.TargetArtifact == nil || pack.TargetArtifact.Payload["script_text"] != "第一集" {
		t.Fatalf("resolved wrong script episode context: target=%+v artifact=%+v", pack.Target, pack.TargetArtifact)
	}
}

func TestRevisionContinuationUsesResolvedModelInstructionInsteadOfShortConfirmation(t *testing.T) {
	artifact := agent.Artifact{
		ArtifactID: "script_2", ArtifactType: "script_unit", Status: agent.ArtifactConfirmed, Version: 1,
		Payload: map[string]any{"episode_id": 2, "script_text": "旧台词"},
	}
	decision := mainagent.Decision{
		SourceMode:     agent.SourceModeNovel,
		AgentReply:     "实际修改这句台词，强化装傻挑衅下暗藏的怒火，其余内容保持不变。",
		Reason:         "forced_revision_from_conversation_continuation",
		RevisionIntent: mainagent.RevisionIntentPatchScriptSpan,
		RevisionTarget: &mainagent.RevisionTarget{ArtifactID: "script_2", ArtifactType: "script_unit", EpisodeID: "2", NodeID: "line_6", Scope: "selection"},
	}
	pack := buildRevisionContextPack(
		mainagent.MessageRequest{Message: "ok"}, decision,
		mainagent.Context{Run: &agent.Run{SourceMode: agent.SourceModeNovel}}, []agent.Artifact{artifact},
	)
	if pack.UserRequest != decision.AgentReply {
		t.Fatalf("expected resolved revision instruction, got %q", pack.UserRequest)
	}
}

func TestRevisionContinuationCarriesRecentSelectionIntoExecutionPack(t *testing.T) {
	artifact := agent.Artifact{
		ArtifactID: "script_2", ArtifactType: "script_unit", Status: agent.ArtifactConfirmed, Version: 1,
		Payload: map[string]any{"episode_id": 2, "script_text": "陈知: 你谁啊？"},
	}
	decision := mainagent.Decision{
		SourceMode:     agent.SourceModeNovel,
		AgentReply:     "强化这句台词的情绪，其余内容保持不变。",
		Reason:         "forced_revision_from_conversation_continuation",
		RevisionIntent: mainagent.RevisionIntentPatchScriptSpan,
		RevisionTarget: &mainagent.RevisionTarget{ArtifactID: "script_2", ArtifactType: "script_unit", EpisodeID: "2", NodeID: "line_6", Scope: "selection"},
	}
	ctx := mainagent.Context{
		Run: &agent.Run{SourceMode: agent.SourceModeNovel},
		Conversation: &mainagent.ConversationContext{RecentTurns: []mainagent.ConversationTurn{
			{Role: "user", Content: "加强这句话的情绪", SelectionContext: map[string]any{
				"artifact_id": "script_2", "artifact_type": "script_unit", "episode_id": "2",
				"scene_id": "scene_2_1", "node_id": "line_6", "line_ids": []any{"line_6"},
				"selection_scope": "line", "selected_text": "陈知: 你谁啊？",
			}},
		}},
	}

	pack := buildRevisionContextPack(mainagent.MessageRequest{Message: "ok"}, decision, ctx, []agent.Artifact{artifact})
	if pack.FocusedContext == nil || pack.FocusedContext.SelectedText != "陈知: 你谁啊？" {
		t.Fatalf("recent selection was not carried into execution pack: %#v", pack.FocusedContext)
	}
	if pack.FocusedContext.NodeID != "line_6" || pack.Target.NodeID != "line_6" {
		t.Fatalf("recent selection locator was not preserved: focused=%#v target=%#v", pack.FocusedContext, pack.Target)
	}
	if issue := validateRevisionContextPack(pack); issue != "" {
		t.Fatalf("valid continuation pack was rejected: %s", issue)
	}
}

func TestControlTimeoutRetryUsesOriginalSelectedInstruction(t *testing.T) {
	artifact := agent.Artifact{
		ArtifactID: "script_1", ArtifactType: "script_unit", Status: agent.ArtifactConfirmed, Version: 1,
		Payload: map[string]any{"episode_id": 1, "script_text": "旧动作"},
	}
	decision := mainagent.Decision{
		SourceMode: agent.SourceModeNovel, Reason: "forced_revision_retry_after_control_failure",
		RevisionIntent: mainagent.RevisionIntentPatchScriptSpan,
		RevisionTarget: &mainagent.RevisionTarget{ArtifactID: "script_1", ArtifactType: "script_unit", EpisodeID: "1", NodeID: "line_8", Scope: "selection"},
	}
	ctx := mainagent.Context{
		Run: &agent.Run{SourceMode: agent.SourceModeNovel},
		Conversation: &mainagent.ConversationContext{RecentTurns: []mainagent.ConversationTurn{
			{Role: "user", Content: "这句话情绪更丰富一些", SelectionContext: map[string]any{
				"artifact_id": "script_1", "artifact_type": "script_unit", "episode_id": "1", "node_id": "line_8", "selected_text": "旧动作",
			}},
			{Role: "agent", Intent: string(mainagent.IntentUnsupported), Content: "控制模型请求超时"},
			{Role: "user", Content: "重试"},
			{Role: "agent", Content: "本次尚未执行"},
			{Role: "user", Content: "继续"},
		}},
	}

	pack := buildRevisionContextPack(mainagent.MessageRequest{Message: "重试"}, decision, ctx, []agent.Artifact{artifact})
	if pack.UserRequest != "这句话情绪更丰富一些" {
		t.Fatalf("retry lost the original selected instruction: %q", pack.UserRequest)
	}
}

func TestRevisionContinuationDoesNotReuseSelectionForDifferentTarget(t *testing.T) {
	decision := mainagent.Decision{
		Reason:         "forced_revision_from_conversation_continuation",
		RevisionIntent: mainagent.RevisionIntentPatchScriptSpan,
		RevisionTarget: &mainagent.RevisionTarget{ArtifactID: "script_2", ArtifactType: "script_unit", NodeID: "line_2", Scope: "selection"},
	}
	ctx := mainagent.Context{Conversation: &mainagent.ConversationContext{RecentTurns: []mainagent.ConversationTurn{{
		SelectionContext: map[string]any{"artifact_id": "script_1", "artifact_type": "script_unit", "selected_text": "旧选区"},
	}}}}

	pack := buildRevisionContextPack(mainagent.MessageRequest{Message: "ok"}, decision, ctx, nil)
	if pack.FocusedContext != nil {
		t.Fatalf("selection from a different artifact leaked into the revision: %#v", pack.FocusedContext)
	}
}

func TestDirectScriptRevisionCarriesMatchingRecentSelection(t *testing.T) {
	decision := mainagent.Decision{
		AgentReply:     "Strengthen the emotion in this dialogue and keep all other lines unchanged.",
		RevisionIntent: mainagent.RevisionIntentPatchScriptSpan,
		RevisionTarget: &mainagent.RevisionTarget{
			ArtifactID: "script_2", ArtifactType: "script_unit", EpisodeID: "2",
			SceneID: "scene_2_1", NodeID: "line_6", Scope: "selection",
		},
	}
	ctx := mainagent.Context{Conversation: &mainagent.ConversationContext{RecentTurns: []mainagent.ConversationTurn{{
		SelectionContext: map[string]any{
			"artifact_id": "script_2", "artifact_type": "script_unit", "episode_id": "2",
			"scene_id": "scene_2_1", "node_id": "line_6", "selected_text": "陈知: 你谁啊？",
		},
	}}}}
	pack := buildRevisionContextPack(mainagent.MessageRequest{Message: "确认修改"}, decision, ctx, nil)
	if pack.FocusedContext == nil || pack.FocusedContext.NodeID != "line_6" {
		t.Fatalf("direct script revision did not recover matching selection: %#v", pack.FocusedContext)
	}
	if pack.UserRequest != decision.AgentReply {
		t.Fatalf("direct continuation passed the short confirmation instead of the resolved instruction: %q", pack.UserRequest)
	}
}

func TestDirectScriptRevisionRejectsRecentSelectionFromDifferentLine(t *testing.T) {
	decision := mainagent.Decision{
		RevisionIntent: mainagent.RevisionIntentPatchScriptSpan,
		RevisionTarget: &mainagent.RevisionTarget{
			ArtifactID: "script_2", ArtifactType: "script_unit", SceneID: "scene_2_1", NodeID: "line_6", Scope: "selection",
		},
	}
	ctx := mainagent.Context{Conversation: &mainagent.ConversationContext{RecentTurns: []mainagent.ConversationTurn{{
		SelectionContext: map[string]any{
			"artifact_id": "script_2", "artifact_type": "script_unit", "scene_id": "scene_2_1",
			"node_id": "line_5", "selected_text": "另一句台词",
		},
	}}}}
	pack := buildRevisionContextPack(mainagent.MessageRequest{Message: "修改第六行"}, decision, ctx, nil)
	if pack.FocusedContext != nil {
		t.Fatalf("selection from a different line leaked into direct revision: %#v", pack.FocusedContext)
	}
}

func TestConversationContextKeepsLatestSelectionOutsideNormalWindow(t *testing.T) {
	server := &apiServer{messages: map[string][]messageDTO{"project_1": {}}}
	server.messages["project_1"] = append(server.messages["project_1"], messageDTO{
		MessageID: "selected", Role: "user", Content: "修改这句话",
		SelectionContext: map[string]any{"artifact_id": "script_2", "selected_text": "旧台词"},
	})
	for index := 0; index < 12; index++ {
		server.messages["project_1"] = append(server.messages["project_1"], messageDTO{
			MessageID: fmt.Sprintf("message_%d", index), Role: "agent", Content: "后续消息",
		})
	}

	context := server.conversationContext("project_1", 8)
	if context == nil || len(context.RecentTurns) != 9 {
		t.Fatalf("expected latest selection plus normal window, got %#v", context)
	}
	if context.RecentTurns[0].SelectionContext["artifact_id"] != "script_2" {
		t.Fatalf("latest selected turn was not retained: %#v", context.RecentTurns)
	}
}

func TestConversationContextDoesNotReviveConsumedSelection(t *testing.T) {
	server := &apiServer{messages: map[string][]messageDTO{"project_1": {
		{MessageID: "selected", Role: "user", Content: "修改这句话", SelectionContext: map[string]any{"artifact_id": "script_2", "selected_text": "旧台词"}},
		{MessageID: "revised", Role: "agent", Content: "修改完成", Intent: string(mainagent.IntentReviseCheckpoint)},
		{MessageID: "greeting", Role: "user", Content: "你好"},
	}}}

	context := server.conversationContext("project_1", 8)
	if context == nil {
		t.Fatal("expected conversation context")
	}
	if focused := focusedContextFromConversation(context, mainagent.RevisionTarget{}, nil); focused != nil {
		t.Fatalf("consumed selection was revived: %#v", focused)
	}
}

func TestSelectionConsumptionIncludesResumeAndRerun(t *testing.T) {
	for _, intent := range []mainagent.Intent{mainagent.IntentResumeRun, mainagent.IntentRerunStep} {
		if !selectionConsumingMessageIntent(string(intent)) {
			t.Fatalf("%s must consume a stale selection", intent)
		}
	}
}

func TestDecisionAuditContextPersistsGenerationPromptState(t *testing.T) {
	config := agent.GenerationConfig{TargetEpisodeCount: 2, EpisodeDurationMinutes: 1.5}
	audit := decisionAuditContext(mainagent.Decision{
		Intent: mainagent.IntentGenerateFromNovel, NextAction: mainagent.ActionReply,
		SourceMode: agent.SourceModeNovel, RequiresGenerationConfig: true,
		GenerationConfig: &config, TargetFileIDs: []string{"file_1"},
	})
	if audit["requires_generation_config"] != true || audit["generation_config"] == nil {
		t.Fatalf("generation prompt state was not persisted: %#v", audit)
	}
	ids, ok := audit["target_file_ids"].([]string)
	if !ok || len(ids) != 1 || ids[0] != "file_1" {
		t.Fatalf("target files were not persisted: %#v", audit)
	}
}

func TestCompactSelectionContextKeepsAllLocatorFields(t *testing.T) {
	selection := map[string]any{
		"artifact_id": "script_2", "artifact_type": "script_unit", "selected_text": "selected dialogue",
		"episode_id": "2", "scene_id": "scene_2_1", "node_id": "line_6",
		"start_scene_id": "scene_2_1", "end_scene_id": "scene_2_1",
		"start_line_id": "line_6", "end_line_id": "line_6", "line_ids": []any{"line_6"},
		"selection_scope": "line", "selection_start": 0, "selection_end": 9,
		"selection_hash": "hash", "selection_source": "lexical_script_editor",
		"before_context": "before", "after_context": "after", "block_type": "dialogue",
		"version": 1,
	}
	compacted := compactSelectionContext(selection)
	if len(compacted) != len(selection) {
		t.Fatalf("selection context lost fields: got %d want %d", len(compacted), len(selection))
	}
	if compacted["selected_text"] != "selected dialogue" || compacted["node_id"] != "line_6" || compacted["artifact_id"] != "script_2" {
		t.Fatalf("critical selection fields were truncated: %#v", compacted)
	}
}

func TestRequiredRevisionArtifactsCoversBothFlows(t *testing.T) {
	cases := []struct {
		mode         agent.SourceMode
		artifactType string
		expected     []string
	}{
		{agent.SourceModeNovel, "story_bible", []string{"source_input"}},
		{agent.SourceModeNovel, "episode_split", []string{"source_input", "story_bible"}},
		{agent.SourceModeNovel, "episode_cards", []string{"story_bible", "episode_split"}},
		{agent.SourceModeNovel, "script_context", []string{"story_bible", "episode_split", "episode_cards"}},
		{agent.SourceModeNovel, "script_unit", []string{"story_bible", "episode_split", "episode_cards", "script_context"}},
		{agent.SourceModeNonNovel, "material_bank", []string{"source_input"}},
		{agent.SourceModeNonNovel, "story_seed", []string{"source_input", "material_bank"}},
		{agent.SourceModeNonNovel, "series_blueprint", []string{"material_bank", "story_seed"}},
		{agent.SourceModeNonNovel, "episode_cards", []string{"material_bank", "story_seed", "series_blueprint"}},
		{agent.SourceModeNonNovel, "script_context", []string{"material_bank", "story_seed", "series_blueprint", "episode_cards"}},
		{agent.SourceModeNonNovel, "script_unit", []string{"material_bank", "story_seed", "series_blueprint", "episode_cards", "script_context"}},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.mode)+"_"+testCase.artifactType, func(t *testing.T) {
			actual := requiredRevisionArtifacts(testCase.mode, testCase.artifactType)
			if !reflect.DeepEqual(actual, testCase.expected) {
				t.Fatalf("wrong revision upstream: got=%v want=%v", actual, testCase.expected)
			}
		})
	}
}

func TestUnknownProjectCannotBeCreatedByMessageOrUpload(t *testing.T) {
	server := &apiServer{
		runtime:  mock.NewRuntime(staticTestWorker{}),
		projects: map[string]projectDTO{},
		messages: map[string][]messageDTO{},
		files:    map[string]fileDTO{},
	}
	tests := []struct {
		name    string
		body    string
		handler http.HandlerFunc
		path    string
	}{
		{name: "message", body: `{"content":"hello"}`, handler: server.projectMessage, path: "/api/projects/missing/messages"},
		{name: "upload", body: `{"file_name":"a.txt","text_content":"hello"}`, handler: server.uploadProjectFile, path: "/api/projects/missing/files"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			mux := http.NewServeMux()
			pattern := "POST /api/projects/{project_id}/" + testCase.name
			if testCase.name == "message" {
				pattern = "POST /api/projects/{project_id}/messages"
			} else {
				pattern = "POST /api/projects/{project_id}/files"
			}
			mux.HandleFunc(pattern, testCase.handler)
			request := httptest.NewRequest(http.MethodPost, testCase.path, bytes.NewBufferString(testCase.body))
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d: %s", response.Code, response.Body.String())
			}
			if len(server.projects) != 0 {
				t.Fatalf("unknown project was created: %#v", server.projects)
			}
		})
	}
}

type patchTestClient struct {
	response string
	request  llm.ChatRequest
}

func (c *patchTestClient) Configured() bool { return true }

func (c *patchTestClient) Complete(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	c.request = req
	return llm.ChatResponse{Content: c.response}, nil
}

func TestServeUnderTest(t *testing.T) {
	if os.Getenv("N2S_SERVE_UNDER_TEST") != "1" {
		t.Skip("set N2S_SERVE_UNDER_TEST=1 to run the local backend through go test")
	}

	llmConfig := llm.ConfigFromEnv()
	llmClient := llm.NewOpenAICompatibleClient(llmConfig)
	contentWorker := worker.NewLLMWorker(llmClient, llmConfig.GenerationModel)
	var runtimeWorker mock.ContentWorker = contentWorker
	controlAgent := mainagent.NewWithFallback(llmClient, llmConfig.ControlModel, llmConfig.ControlFallbackModel)
	var mainAgent mainagent.Decider = controlAgent
	runtimeName := selectedRuntimeName()
	if runtimeName == "eino" {
		einoWorker, err := worker.NewEinoWorker(contentWorker)
		if err != nil {
			t.Fatal(err)
		}
		runtimeWorker = einoWorker
		einoMainAgent, err := mainagent.NewEinoAgent(controlAgent)
		if err != nil {
			t.Fatal(err)
		}
		mainAgent = einoMainAgent
	}
	if !contentWorker.Configured() && os.Getenv("N2S_ENABLE_STATIC_DEMO") == "1" {
		runtimeWorker = staticTestWorker{}
	}
	server := &apiServer{
		runtime:     mock.NewRuntimeWithStateAndName(runtimeWorker, runtimeStatePath(), runtimeName),
		mainAgent:   mainAgent,
		projects:    make(map[string]projectDTO),
		messages:    make(map[string][]messageDTO),
		files:       make(map[string]fileDTO),
		runtimeName: runtimeName,
	}
	server.restoreProjectsFromRuntime()

	addr := serverAddress()
	log.Printf("agent backend test server listening on http://%s", addr)
	if err := http.ListenAndServe(addr, newAPIHandler(server)); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreProjectsFromRuntime(t *testing.T) {
	runtime := mock.NewRuntime(staticTestWorker{})
	started, err := runtime.StartRunAsyncContext(context.Background(), agent.StartRunRequest{
		ProjectID:   "project_restore_api",
		SourceMode:  agent.SourceModeNonNovel,
		UserMessage: "restore this project",
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	for attempt := 0; attempt < 100; attempt++ {
		run, _ := runtime.GetRun(started.Run.RunID)
		if len(runtime.Artifacts(started.Run.RunID)) > 1 || run.Status != agent.RunRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	server := &apiServer{
		runtime:  runtime,
		projects: make(map[string]projectDTO),
		messages: make(map[string][]messageDTO),
		files:    make(map[string]fileDTO),
	}
	server.restoreProjectsFromRuntime()

	project, ok := server.projects["project_restore_api"]
	if !ok {
		t.Fatal("expected project to be restored from runtime")
	}
	if project.ActiveRunID != started.Run.RunID {
		t.Fatalf("expected active run %s, got %s", started.Run.RunID, project.ActiveRunID)
	}
	currentRun, _ := runtime.GetRun(started.Run.RunID)
	if project.Status != projectStatusFromRun(currentRun.Status) {
		t.Fatalf("expected restored status %s, got %s", projectStatusFromRun(currentRun.Status), project.Status)
	}
	if len(project.ActiveArtifacts) == 0 {
		t.Fatal("expected restored active artifacts")
	}
}

func TestRestoreProjectsClearsMissingActiveRun(t *testing.T) {
	server := &apiServer{
		runtime: mock.NewRuntime(staticTestWorker{}),
		projects: map[string]projectDTO{"project_stale": {
			ProjectID: "project_stale", Title: "stale", Status: projectRunning, ActiveRunID: "run_missing",
			CurrentFocusArtifactID: "artifact_missing", ActiveArtifacts: map[string]string{"story_bible": "artifact_missing"},
		}},
		messages: map[string][]messageDTO{}, files: map[string]fileDTO{},
	}
	if err := server.restoreProjectsFromRuntime(); err != nil {
		t.Fatal(err)
	}
	project := server.projects["project_stale"]
	if project.ActiveRunID != "" || project.Status != projectIdle || project.CurrentFocusArtifactID != "" || len(project.ActiveArtifacts) != 0 {
		t.Fatalf("stale run reference was not cleared: %#v", project)
	}
}

func TestGetProjectReconcilesAsynchronousRuntimeState(t *testing.T) {
	runtime := mock.NewRuntime(staticTestWorker{})
	started, err := runtime.StartRunAsyncContext(context.Background(), agent.StartRunRequest{
		ProjectID: "project_reconcile", SourceMode: agent.SourceModeNonNovel, UserMessage: "build a story",
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	for attempt := 0; attempt < 100; attempt++ {
		run, _ := runtime.GetRun(started.Run.RunID)
		if run.Status != agent.RunRunning || len(runtime.Artifacts(started.Run.RunID)) > 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	server := &apiServer{
		runtime: runtime,
		projects: map[string]projectDTO{"project_reconcile": {
			ProjectID: "project_reconcile", Title: "Project", SourceMode: agent.SourceModeNonNovel,
			Status: projectRunning, ActiveRunID: started.Run.RunID, ActiveArtifacts: map[string]string{"source_input": "stale"},
		}},
		messages: map[string][]messageDTO{}, files: map[string]fileDTO{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects/{project_id}", server.getProject)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/projects/project_reconcile", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	currentRun, _ := runtime.GetRun(started.Run.RunID)
	project := server.projects["project_reconcile"]
	if project.Status != projectStatusFromRun(currentRun.Status) {
		t.Fatalf("project status was stale: got %s want %s", project.Status, projectStatusFromRun(currentRun.Status))
	}
	if project.ActiveArtifacts["source_input"] == "stale" || len(project.ActiveArtifacts) == 0 {
		t.Fatalf("project active artifacts were not reconciled: %#v", project.ActiveArtifacts)
	}
}

func TestRevisionMatrixAcceptsEverySupportedPlanningArtifactScope(t *testing.T) {
	cases := []struct {
		artifactType string
		intent       mainagent.RevisionIntent
		fieldPath    string
	}{
		{"source_input", mainagent.RevisionIntentPatchArtifactField, "notes"},
		{"source_input", mainagent.RevisionIntentReplaceSourceInput, ""},
		{"story_bible", mainagent.RevisionIntentPatchArtifactField, "characters[0].goal"},
		{"story_bible", mainagent.RevisionIntentPatchArtifactEntity, "characters[0]"},
		{"story_bible", mainagent.RevisionIntentPatchArtifactSection, "relationships"},
		{"story_bible", mainagent.RevisionIntentRegenerateArtifact, ""},
		{"episode_split", mainagent.RevisionIntentPatchArtifactField, "generation_config.episode_duration_minutes"},
		{"episode_split", mainagent.RevisionIntentPatchArtifactEntity, "episodes[0]"},
		{"episode_split", mainagent.RevisionIntentPatchArtifactSection, "coverage_check"},
		{"episode_split", mainagent.RevisionIntentRegenerateArtifact, ""},
		{"material_bank", mainagent.RevisionIntentPatchArtifactEntity, "conflict_materials[0]"},
		{"material_bank", mainagent.RevisionIntentPatchArtifactField, "most_promising_direction"},
		{"material_bank", mainagent.RevisionIntentPatchArtifactSection, "gaps_and_questions"},
		{"material_bank", mainagent.RevisionIntentRegenerateArtifact, ""},
		{"story_seed", mainagent.RevisionIntentPatchArtifactField, "core_premise"},
		{"story_seed", mainagent.RevisionIntentPatchArtifactEntity, "protagonist"},
		{"story_seed", mainagent.RevisionIntentPatchArtifactSection, "main_plotline"},
		{"story_seed", mainagent.RevisionIntentRegenerateArtifact, ""},
		{"series_blueprint", mainagent.RevisionIntentPatchArtifactField, "series_promise"},
		{"series_blueprint", mainagent.RevisionIntentPatchArtifactEntity, "phase_plan[0]"},
		{"series_blueprint", mainagent.RevisionIntentPatchArtifactSection, "payoff_distribution"},
		{"series_blueprint", mainagent.RevisionIntentRegenerateArtifact, ""},
		{"episode_cards", mainagent.RevisionIntentPatchArtifactField, "episodes[0].ending_hook"},
		{"episode_cards", mainagent.RevisionIntentPatchArtifactEntity, "episodes[0]"},
		{"episode_cards", mainagent.RevisionIntentPatchArtifactSection, "continuity_delta"},
		{"episode_cards", mainagent.RevisionIntentRegenerateArtifact, ""},
	}
	for _, testCase := range cases {
		pack := &mainagent.RevisionContextPack{
			RevisionIntent: testCase.intent,
			Target: mainagent.RevisionTarget{
				ArtifactID: "artifact_test", ArtifactType: testCase.artifactType, FieldPath: testCase.fieldPath,
			},
		}
		if issue := validateRevisionContextPack(pack); issue != "" {
			t.Fatalf("expected %s %s %s to be accepted, got %s", testCase.artifactType, testCase.intent, testCase.fieldPath, issue)
		}
	}
}

func TestRevisionMatrixRejectsUnsupportedOrAmbiguousTargets(t *testing.T) {
	cases := []*mainagent.RevisionContextPack{
		{RevisionIntent: mainagent.RevisionIntentPatchArtifactField, Target: mainagent.RevisionTarget{ArtifactID: "a", ArtifactType: "story_seed", FieldPath: "unknown_field", Scope: "field"}},
		{RevisionIntent: mainagent.RevisionIntentPatchArtifactField, Target: mainagent.RevisionTarget{ArtifactID: "a", ArtifactType: "source_input", FieldPath: "text", Scope: "field"}},
		{RevisionIntent: mainagent.RevisionIntentPatchArtifactSection, Target: mainagent.RevisionTarget{ArtifactID: "a", ArtifactType: "story_seed", FieldPath: "unknown_story_section", Scope: "section"}},
		{RevisionIntent: mainagent.RevisionIntentPatchArtifactField, Target: mainagent.RevisionTarget{ArtifactID: "a", ArtifactType: "material_bank", FieldPath: "unknown_material_field", Scope: "field"}},
		{RevisionIntent: mainagent.RevisionIntentPatchArtifactSection, Target: mainagent.RevisionTarget{ArtifactID: "a", ArtifactType: "series_blueprint", FieldPath: "unknown_blueprint_section", Scope: "section"}},
		{RevisionIntent: mainagent.RevisionIntentReplaceSourceInput, Target: mainagent.RevisionTarget{ArtifactID: "a", ArtifactType: "story_bible", Scope: "artifact"}},
		{RevisionIntent: mainagent.RevisionIntentPatchArtifactField, Target: mainagent.RevisionTarget{ArtifactID: "a", ArtifactType: "episode_split", FieldPath: "generation_config.episode_duration_minutes", EpisodeID: "1", Scope: "field"}},
		{RevisionIntent: mainagent.RevisionIntentPatchArtifactField, Target: mainagent.RevisionTarget{ArtifactID: "a", ArtifactType: "script_context", FieldPath: "must_follow_facts", Scope: "field"}},
		{RevisionIntent: mainagent.RevisionIntentPatchScriptSpan, Target: mainagent.RevisionTarget{ArtifactID: "a", ArtifactType: "script_unit", EpisodeID: "1", Scope: "selection"}},
	}
	for index, pack := range cases {
		if issue := validateRevisionContextPack(pack); issue == "" {
			t.Fatalf("expected invalid revision case %d to be rejected", index)
		}
	}
}

func TestRevisionMatrixAcceptsCollectionPatchForAnyListArtifact(t *testing.T) {
	cases := []struct {
		artifactType string
		fieldPath    string
	}{
		{"story_bible", "characters"},
		{"episode_split", "episodes"},
		{"material_bank", "conflict_materials"},
		{"story_seed", "main_characters"},
		{"series_blueprint", "phase_plan"},
		{"episode_cards", "episodes"},
	}
	for _, testCase := range cases {
		t.Run(testCase.artifactType+"_"+testCase.fieldPath, func(t *testing.T) {
			pack := &mainagent.RevisionContextPack{
				RevisionIntent: mainagent.RevisionIntentPatchArtifactCollection,
				Target:         mainagent.RevisionTarget{ArtifactID: "artifact_list", ArtifactType: testCase.artifactType, FieldPath: testCase.fieldPath, Scope: "collection"},
				TargetArtifact: &mainagent.ArtifactSnapshot{Payload: map[string]any{testCase.fieldPath: []any{map[string]any{"id": 1}}}},
			}
			if issue := validateRevisionContextPack(pack); issue != "" {
				t.Fatalf("collection patch should be accepted: %s", issue)
			}
		})
	}
}

func TestRevisionMatrixRejectsCollectionPatchForScalarField(t *testing.T) {
	pack := &mainagent.RevisionContextPack{
		RevisionIntent: mainagent.RevisionIntentPatchArtifactCollection,
		Target:         mainagent.RevisionTarget{ArtifactID: "artifact_scalar", ArtifactType: "story_seed", FieldPath: "core_premise", Scope: "collection"},
		TargetArtifact: &mainagent.ArtifactSnapshot{Payload: map[string]any{"core_premise": "not a list"}},
	}
	if issue := validateRevisionContextPack(pack); issue == "" {
		t.Fatal("scalar field was incorrectly accepted as a collection patch")
	}
}

func TestRevisionMatrixAcceptsExistingExtensionField(t *testing.T) {
	pack := &mainagent.RevisionContextPack{
		RevisionIntent: mainagent.RevisionIntentPatchArtifactField,
		Target:         mainagent.RevisionTarget{ArtifactID: "artifact_extension", ArtifactType: "story_seed", FieldPath: "future_extension", Scope: "field"},
		TargetArtifact: &mainagent.ArtifactSnapshot{ArtifactID: "artifact_extension", ArtifactType: "story_seed", Payload: map[string]any{"future_extension": "old"}},
	}
	if issue := validateRevisionContextPack(pack); issue != "" {
		t.Fatalf("existing extension field should remain locally editable: %s", issue)
	}
}

func TestSourceReplacementPayloadUsesAttachmentWithoutModelRewrite(t *testing.T) {
	pack := &mainagent.RevisionContextPack{
		SourceMode: agent.SourceModeNovel,
		Target:     mainagent.RevisionTarget{ArtifactID: "source_v1", ArtifactType: "source_input"},
		TargetArtifact: &mainagent.ArtifactSnapshot{Payload: map[string]any{
			"text": "旧原文", "notes": []any{"保留说明"}, "generation_config": map[string]any{"target_episode_count": 2},
		}},
	}
	payload, issue := sourceReplacementPayload(pack, []mainagent.FileAttachment{{FileName: "new.txt", MimeType: "text/plain", TextContent: "新原文正文"}})
	if issue != "" {
		t.Fatalf("expected replacement payload, got %s", issue)
	}
	if payload["text"] != "【附件：new.txt】\n新原文正文" {
		t.Fatalf("expected exact attachment text, got %#v", payload["text"])
	}
	if payload["notes"].([]any)[0] != "保留说明" || payload["generation_config"].(map[string]any)["target_episode_count"] != float64(2) {
		t.Fatalf("expected source metadata to be preserved, got %#v", payload)
	}
	if _, issue := sourceReplacementPayload(pack, nil); issue == "" {
		t.Fatal("expected replacement without attachment to be rejected")
	}
}

func TestSourceReplacementMessageReturnsSuccessAndCreatesNewVersion(t *testing.T) {
	runtime := mock.NewRuntime(fastLifecycleWorker{})
	started, err := runtime.StartRunAsyncContext(context.Background(), agent.StartRunRequest{
		ProjectID: "project_source_replace", UserMessage: "生成剧本", SourceText: "旧原文",
		SourceMode: agent.SourceModeNovel, GenerationConfig: &agent.GenerationConfig{TargetEpisodeCount: 1, EpisodeDurationMinutes: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForCompletedRun(t, runtime, started.Run.RunID)

	var source agent.Artifact
	for _, artifact := range runtime.Artifacts(started.Run.RunID) {
		if artifact.ArtifactType == "source_input" && artifact.Status != agent.ArtifactSuperseded {
			source = artifact
		}
	}
	if source.ArtifactID == "" {
		t.Fatal("source_input artifact was not found")
	}
	controller := &patchTestClient{response: fmt.Sprintf(`{
		"intent":"revise_checkpoint","confidence":0.99,"next_action":"revise_checkpoint","source_mode":"novel",
		"agent_reply":"替换输入材料。","reason":"replace_source",
		"revision_intent":"replace_source_input",
		"revision_target":{"artifact_id":%q,"artifact_type":"source_input","scope":"artifact"}
	}`, source.ArtifactID)}
	server := &apiServer{
		runtime: runtime, mainAgent: mainagent.New(controller, "control-model"),
		projects: map[string]projectDTO{"project_source_replace": {
			ProjectID: "project_source_replace", Title: "作品", SourceMode: agent.SourceModeNovel,
			Status: projectCompleted, ActiveRunID: started.Run.RunID,
		}},
		messages: map[string][]messageDTO{}, files: map[string]fileDTO{},
	}
	response, status, err := server.executeAgentMessage(context.Background(), mainagent.MessageRequest{
		ProjectID: "project_source_replace", RunID: started.Run.RunID, SourceMode: agent.SourceModeNovel,
		Message:     "用这个附件替换输入材料",
		Attachments: []mainagent.FileAttachment{{FileID: "file_new", FileName: "新原文.txt", MimeType: "text/plain", TextContent: "新原文正文"}},
	})
	if err != nil || status != http.StatusOK {
		t.Fatalf("source replacement must return 200, status=%d err=%v", status, err)
	}
	var latest agent.Artifact
	for _, artifact := range response.Artifacts {
		if artifact.ArtifactType == "source_input" && artifact.Status != agent.ArtifactSuperseded {
			latest = artifact
		}
	}
	if latest.Version != source.Version+1 || latest.Payload["text"] != "【附件：新原文.txt】\n新原文正文" {
		t.Fatalf("replacement was not persisted as a new source version: %#v", latest)
	}
}

func TestLLMWorkerScriptSpanUsesSparsePatchContract(t *testing.T) {
	client := &patchTestClient{response: `{"artifact_type":"script_unit","status":"pending_approval","operation":"patch_script_span","episode_id":3,"scene_id":"scene_3_2","node_id":"scene_3_2-line-2","old_text":"旧句","new_text":"新句"}`}
	contentWorker := worker.NewLLMWorker(client, "generation-model")
	target := agent.Artifact{ArtifactID: "artifact_script", ArtifactType: "script_unit", SourceMode: agent.SourceModeNovel, Payload: map[string]any{
		"episode_id": 3, "script_text": "旧句", "scenes": []any{map[string]any{"scene_id": "scene_3_2", "blocks": []any{map[string]any{"text": "旧句"}}}},
	}}
	instruction := `USER_REVISION_REQUEST:
增强这一句
REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_script_span","target":{"artifact_id":"artifact_script","artifact_type":"script_unit","episode_id":"3","scene_id":"scene_3_2","node_id":"scene_3_2-line-2","scope":"selection"},"focused_context":{"selected_text":"旧句"}}`
	planned, err := contentWorker.PatchArtifactStep(context.Background(), agent.Run{SourceMode: agent.SourceModeNovel}, target, []agent.Artifact{target}, "script_unit", instruction)
	if err != nil {
		t.Fatalf("patch script span: %v", err)
	}
	if _, ok := planned.Payload["__script_span_patch"]; !ok {
		t.Fatalf("expected sparse script patch payload, got %#v", planned.Payload)
	}
	if _, ok := planned.Payload["scenes"]; ok {
		t.Fatalf("worker must not return full scenes for span patch: %#v", planned.Payload)
	}
	if len(client.request.Messages) < 2 || !strings.Contains(client.request.Messages[1].Content, "do not return a full scene, episode, or script") {
		t.Fatalf("expected strict sparse patch prompt, got %#v", client.request.Messages)
	}
}

type staticTestWorker struct{}

func (staticTestWorker) PlanStep(ctx context.Context, run agent.Run, source agent.Artifact, existing []agent.Artifact, artifactType string) (mock.PlannedArtifact, *agent.ApprovalRequest, error) {
	if err := staticStepDelay(ctx); err != nil {
		return mock.PlannedArtifact{}, nil, err
	}
	status := agent.ArtifactConfirmed
	if artifactType == "episode_cards" || artifactType == "episode_split" {
		status = agent.ArtifactPendingApproval
	}
	payload := map[string]any{"note": "test artifact"}
	switch artifactType {
	case "material_bank":
		payload = map[string]any{"explicit_user_material": []string{"测试素材"}, "inferred_material": []string{"复仇短剧"}, "missing_information": []string{}}
	case "story_seed":
		payload = map[string]any{"logline": "女主回归复仇。", "core_hook": "旧案反杀", "emotional_engine": "压迫后的反击"}
	case "series_blueprint":
		payload = map[string]any{"target_episode_count": 3, "phase_map": []string{"回归", "布局", "反杀"}}
	case "story_bible":
		payload = map[string]any{"story_overview": map[string]any{"one_sentence_logline": "女主回归复仇。", "core_conflict": "旧权力与新复仇"}, "characters": []string{"女主"}, "must_keep_facts": []string{}, "adaptation_risks": []string{}}
	case "episode_split":
		payload = map[string]any{"target_episode_count": 3, "actual_episode_count": 3, "episodes": []map[string]any{{"episode_id": 1, "source_summary": "开局", "hook_strength": "中", "requires_user_attention": false}}}
	case "episode_cards":
		payload = map[string]any{"episodes": []map[string]any{
			{"episode_id": 1, "title": "回宫", "opening_pressure": "女主被迫回到权力中心。", "main_conflict": "她要隐藏身份查旧案。", "scene_plan": []string{"回宫", "试探"}, "hook": "发现旧案关键证人。", "continuity_delta": map[string]any{}, "risk_notes": []string{}},
			{"episode_id": 2, "title": "设局", "opening_pressure": "仇人步步紧逼。", "main_conflict": "女主借力打力。", "scene_plan": []string{"设局", "反制"}, "hook": "仇人落入圈套。", "continuity_delta": map[string]any{}, "risk_notes": []string{}},
		}}
	}
	var approval *agent.ApprovalRequest
	if artifactType == "episode_cards" {
		approval = &agent.ApprovalRequest{Title: "确认分集卡后继续生成剧本", Reason: "确认后进入剧本生成。"}
	}
	return mock.PlannedArtifact{ArtifactType: artifactType, Status: status, Payload: payload}, approval, nil
}

func (staticTestWorker) WriteScriptStep(ctx context.Context, run agent.Run, artifacts []agent.Artifact, artifactType string, note string) (mock.PlannedArtifact, error) {
	if err := staticStepDelay(ctx); err != nil {
		return mock.PlannedArtifact{}, err
	}
	payload := map[string]any{"note": "test script artifact"}
	switch artifactType {
	case "script_context":
		payload = map[string]any{"source_mode": run.SourceMode, "must_follow_facts": []string{"女主回归复仇"}, "allowed_additions": []string{}, "forbidden_changes": []string{}, "continuity_state": map[string]any{}, "style_constraints": map[string]any{}, "user_notes": []string{note}}
	case "script_unit":
		payload = map[string]any{"episode_id": 1, "title": "回宫", "scenes": []map[string]any{{"scene_id": "scene_1_1", "heading": "INT. 宫门 - 夜", "blocks": []map[string]any{{"block_type": "action", "text": "雨声压低了宫门外的脚步。"}, {"block_type": "dialogue", "speaker": "女主", "text": "这一次，我不会再退。"}}}}, "continuity_delta": map[string]any{}, "self_check": map[string]any{}}
	case "scripts":
		payload = map[string]any{"source_mode": run.SourceMode, "episode_count": 1, "script_units": []map[string]any{{"episode_id": 1, "version": 1, "status": "generated"}}, "global_continuity_state": map[string]any{}, "quality_flags": []string{}}
	}
	return mock.PlannedArtifact{ArtifactType: artifactType, Status: agent.ArtifactConfirmed, Payload: payload}, nil
}

func staticStepDelay(ctx context.Context) error {
	timer := time.NewTimer(900 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type fastLifecycleWorker struct{}

func (fastLifecycleWorker) PlanStep(_ context.Context, run agent.Run, _ agent.Artifact, _ []agent.Artifact, artifactType string) (mock.PlannedArtifact, *agent.ApprovalRequest, error) {
	payload := map[string]any{"value": artifactType}
	if artifactType == "episode_cards" {
		payload = map[string]any{"episodes": []any{map[string]any{"episode_id": 1, "title": "第1集"}}}
	}
	return mock.PlannedArtifact{ArtifactType: artifactType, Status: agent.ArtifactPendingApproval, Payload: payload}, nil, nil
}

func (fastLifecycleWorker) WriteScriptStep(_ context.Context, run agent.Run, _ []agent.Artifact, artifactType string, _ string) (mock.PlannedArtifact, error) {
	payload := map[string]any{"source_mode": string(run.SourceMode)}
	if artifactType == "script_unit" {
		payload = map[string]any{"episode_id": 1, "title": "第1集", "script_text": strings.Repeat("△动作推进。", 45), "scenes": []any{}}
	}
	return mock.PlannedArtifact{ArtifactType: artifactType, Status: agent.ArtifactConfirmed, Payload: payload}, nil
}

func waitForCompletedRun(t *testing.T, runtime *mock.Runtime, runID string) {
	t.Helper()
	for attempt := 0; attempt < 500; attempt++ {
		run, _ := runtime.GetRun(runID)
		switch run.Status {
		case agent.RunCompleted:
			return
		case agent.RunFailed:
			t.Fatalf("run failed while preparing test: %#v", run)
		case agent.RunWaitingApproval:
			if _, err := runtime.ContinueRunAsyncContext(context.Background(), runID, agent.ContinueRunRequest{Decision: "approve"}); err != nil {
				t.Fatalf("approve test checkpoint: %v", err)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %s did not complete", runID)
}
