package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"novel2script-agent/backend/internal/agent"
	mock "novel2script-agent/backend/internal/agent/runtime"
)

func TestWorkspaceStoreRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version=999`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := openWorkspaceStore(path); err == nil {
		store.close()
		t.Fatal("expected newer workspace schema to be rejected")
	}
}

func TestWorkspaceStoreSerializesConcurrentWrites(t *testing.T) {
	store, err := openWorkspaceStore(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	now := time.Now().UTC()
	project := projectDTO{ProjectID: "project_concurrent", Title: "并发写入", SourceMode: agent.SourceModeNovel, Status: projectIdle, ActiveArtifacts: map[string]string{}, CreatedAt: now, UpdatedAt: now}
	if err := store.upsertProject(project); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errorsFound := make(chan error, 40)
	for index := 0; index < 20; index++ {
		wg.Add(2)
		go func(index int) {
			defer wg.Done()
			message := messageDTO{MessageID: fmt.Sprintf("message_%02d", index), ProjectID: project.ProjectID, Role: "user", Content: "并发消息", CreatedAt: now.Add(time.Duration(index) * time.Millisecond)}
			if err := store.insertMessage(message); err != nil {
				errorsFound <- err
			}
		}(index)
		go func(index int) {
			defer wg.Done()
			updated := project
			updated.UpdatedAt = now.Add(time.Duration(index) * time.Millisecond)
			if err := store.upsertProject(updated); err != nil {
				errorsFound <- err
			}
		}(index)
	}
	wg.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("concurrent workspace write failed: %v", err)
	}
	_, messages, _, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(messages[project.ProjectID]) != 20 {
		t.Fatalf("expected 20 persisted messages, got %d", len(messages[project.ProjectID]))
	}
}

func TestWorkspaceStoreRestoresProjectsMessagesAndFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := openWorkspaceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	project := projectDTO{
		ProjectID:              "project_restore",
		Title:                  "恢复测试作品",
		SourceMode:             agent.SourceModeNovel,
		Status:                 projectWaitingApproval,
		ActiveRunID:            "run_restore",
		CurrentFocusArtifactID: "artifact_restore",
		ActiveArtifacts:        map[string]string{"story_bible": "artifact_restore"},
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if err := store.upsertProject(project); err != nil {
		t.Fatal(err)
	}
	message := messageDTO{
		MessageID:        "message_restore",
		ProjectID:        project.ProjectID,
		RunID:            project.ActiveRunID,
		Role:             "user",
		Content:          "恢复这条完整聊天",
		Attachments:      []string{"file_restore"},
		SelectionContext: map[string]any{"artifact_id": "artifact_restore"},
		Intent:           "revise_artifact",
		DecisionContext:  map[string]any{"next_action": "revise_checkpoint", "reason": "test_audit"},
		CreatedAt:        now,
	}
	if err := store.insertMessage(message); err != nil {
		t.Fatal(err)
	}
	file := fileDTO{
		FileID:        "file_restore",
		ProjectID:     project.ProjectID,
		Filename:      "novel.txt",
		MimeType:      "text/plain",
		SizeBytes:     18,
		Status:        "uploaded",
		TextPreview:   "完整附件预览",
		TextContent:   "完整附件正文",
		ContentBase64: "",
		CreatedAt:     now,
	}
	if err := store.insertFile(file); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := openWorkspaceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	projects, messages, files, err := reopened.load()
	if err != nil {
		t.Fatal(err)
	}
	if got := projects[project.ProjectID]; got.Title != project.Title || got.ActiveArtifacts["story_bible"] != "artifact_restore" {
		t.Fatalf("project did not restore: %#v", got)
	}
	if got := messages[project.ProjectID]; len(got) != 1 || got[0].Content != message.Content || got[0].Attachments[0] != "file_restore" || got[0].DecisionContext["reason"] != "test_audit" {
		t.Fatalf("messages did not restore: %#v", got)
	}
	if got := files[file.FileID]; got.TextContent != file.TextContent || got.ProjectID != project.ProjectID {
		t.Fatalf("file did not restore: %#v", got)
	}
}

func TestWorkspaceIDsAreUniqueAndNamespaced(t *testing.T) {
	seen := map[string]bool{}
	for _, prefix := range []string{"project", "message", "file"} {
		for range 50 {
			value, err := newWorkspaceID(prefix)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(value, prefix+"_") {
				t.Fatalf("unexpected id prefix: %s", value)
			}
			if seen[value] {
				t.Fatalf("duplicate id generated: %s", value)
			}
			seen[value] = true
		}
	}
}

func TestProjectAPIsKeepWorkspacesIsolatedAndPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := openWorkspaceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &apiServer{
		runtime:  mock.NewRuntime(staticTestWorker{}),
		store:    store,
		projects: map[string]projectDTO{},
		messages: map[string][]messageDTO{},
		files:    map[string]fileDTO{},
	}
	projectA, err := server.newProject("作品 A", agent.SourceModeNovel)
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := server.newProject("作品 B", agent.SourceModeNonNovel)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{project_id}/files", server.uploadProjectFile)
	mux.HandleFunc("POST /api/projects/{project_id}/messages", server.projectMessage)
	mux.HandleFunc("GET /api/projects/{project_id}/messages", server.listProjectMessages)
	mux.HandleFunc("GET /api/projects/{project_id}/files", server.listProjectFiles)

	upload := httptest.NewRecorder()
	mux.ServeHTTP(upload, httptest.NewRequest(
		http.MethodPost,
		"/api/projects/"+projectA.ProjectID+"/files",
		bytes.NewBufferString(`{"file_name":"a.txt","mime_type":"text/plain","text_content":"只属于作品 A"}`),
	))
	if upload.Code != http.StatusCreated {
		t.Fatalf("upload failed: %d %s", upload.Code, upload.Body.String())
	}
	var uploadPayload struct {
		File fileDTO `json:"file"`
	}
	if err := json.Unmarshal(upload.Body.Bytes(), &uploadPayload); err != nil {
		t.Fatal(err)
	}

	message := httptest.NewRecorder()
	mux.ServeHTTP(message, httptest.NewRequest(
		http.MethodPost,
		"/api/projects/"+projectA.ProjectID+"/messages",
		bytes.NewBufferString(`{"content":"","file_ids":["`+uploadPayload.File.FileID+`"]}`),
	))
	if message.Code != http.StatusOK {
		t.Fatalf("message failed: %d %s", message.Code, message.Body.String())
	}

	if got := server.filesForProject(projectB.ProjectID); len(got) != 0 {
		t.Fatalf("project B received project A files: %#v", got)
	}
	server.mu.Lock()
	projectBMessages := append([]messageDTO(nil), server.messages[projectB.ProjectID]...)
	server.mu.Unlock()
	if len(projectBMessages) != 0 {
		t.Fatalf("project B received project A messages: %#v", projectBMessages)
	}
	if len(server.messages[projectA.ProjectID]) != 2 {
		t.Fatalf("expected user and agent messages for project A, got %#v", server.messages[projectA.ProjectID])
	}

	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openWorkspaceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	projects, messages, files, err := reopened.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 || len(messages[projectA.ProjectID]) != 2 || len(messages[projectB.ProjectID]) != 0 {
		t.Fatalf("workspace isolation did not persist: projects=%d messagesA=%d messagesB=%d", len(projects), len(messages[projectA.ProjectID]), len(messages[projectB.ProjectID]))
	}
	if got := files[uploadPayload.File.FileID]; got.ProjectID != projectA.ProjectID || got.TextContent != "只属于作品 A" {
		t.Fatalf("project A file did not persist correctly: %#v", got)
	}
}

func TestDeleteProjectAPICascadesWorkspaceData(t *testing.T) {
	store, err := openWorkspaceStore(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	server := &apiServer{
		runtime: mock.NewRuntime(nil), store: store,
		projects: map[string]projectDTO{}, messages: map[string][]messageDTO{}, files: map[string]fileDTO{},
	}
	project, err := server.newProject("待删除作品", agent.SourceModeNovel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.appendMessage(messageDTO{ProjectID: project.ProjectID, Role: "user", Content: "测试消息"}); err != nil {
		t.Fatal(err)
	}
	file, err := server.storeFile(project.ProjectID, projectFileRequest{FileName: "source.txt", TextContent: "测试附件"})
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	newAPIHandler(server).ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/projects/"+project.ProjectID, nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"deleted":true`) {
		t.Fatalf("delete failed: %d %s", response.Code, response.Body.String())
	}
	if _, ok := server.projectByID(project.ProjectID); ok {
		t.Fatal("project remains in server memory")
	}
	if _, ok := server.files[file.FileID]; ok || len(server.messages[project.ProjectID]) != 0 {
		t.Fatal("project messages or files remain in server memory")
	}
	projects, messages, files, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := projects[project.ProjectID]; ok || len(messages[project.ProjectID]) != 0 {
		t.Fatal("project or messages remain in workspace database")
	}
	if _, ok := files[file.FileID]; ok {
		t.Fatal("project file remains in workspace database")
	}
}
