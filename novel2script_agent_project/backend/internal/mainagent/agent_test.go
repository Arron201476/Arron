package mainagent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"novel2script-agent/backend/internal/agent"
	"novel2script-agent/backend/internal/llm"
)

type fakeChatClient struct {
	configured bool
	response   string
	err        error
	request    llm.ChatRequest
	called     bool
}

func (f *fakeChatClient) Complete(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	f.called = true
	f.request = req
	if f.err != nil {
		return llm.ChatResponse{}, f.err
	}
	return llm.ChatResponse{Content: f.response}, nil
}

func (f *fakeChatClient) Configured() bool {
	return f.configured
}

func TestDecideAllowsSourceInspectionWithoutRun(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"inspect_source",
			"confidence":0.96,
			"next_action":"inspect_source",
			"agent_reply":"正在读取附件。",
			"requires_approval":false,
			"reason":"user_requested_source_analysis",
			"target_file_ids":["file_1"]
		}`,
	}
	controller := New(client, "control-model")

	decision := controller.Decide(context.Background(), Context{
		Request: MessageRequest{ProjectID: "project_1", Message: "分析一下刚才的文件"},
		Project: &ProjectContext{ProjectID: "project_1", Files: []ProjectFile{{
			FileID: "file_1", FileName: "材料.txt", MimeType: "text/plain", TextPreview: "一段待分析的材料",
		}}},
	})

	if decision.NextAction != ActionInspectSource || decision.Intent != IntentInspectSource {
		t.Fatalf("expected inspect_source, got %s", formatDecisionForDebug(decision))
	}
	if len(decision.TargetFileIDs) != 1 || decision.TargetFileIDs[0] != "file_1" {
		t.Fatalf("expected selected project file, got %#v", decision.TargetFileIDs)
	}
	if decision.Trace == nil || decision.Trace.GuardApplied {
		t.Fatalf("source inspection should not be rejected by artifact guards: %#v", decision.Trace)
	}
}

func TestDecideForcesExplicitRevisionOnCompletedRun(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"chat_idle","confidence":0.72,"next_action":"reply","source_mode":"novel",
		"agent_reply":"我可以帮你加强这句话。","reason":"misclassified"
	}`}
	controller := New(client, "control-model")
	decision := controller.Decide(context.Background(), Context{
		Request:        MessageRequest{Message: "把这句话的冲突加强一点", SelectedArtifactID: "script_2", SelectedText: "旧台词"},
		Run:            &agent.Run{RunID: "run_done", Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
		FocusedContext: &FocusedContext{ArtifactID: "script_2", ArtifactType: "script_unit", EpisodeID: "2", SceneID: "scene_2", NodeID: "line_6", SelectedText: "旧台词"},
	})
	if decision.NextAction != ActionReviseCheckpoint || decision.Intent != IntentReviseCheckpoint {
		t.Fatalf("expected completed-run revision action, got %s", formatDecisionForDebug(decision))
	}
	if decision.RevisionIntent != RevisionIntentPatchScriptSpan {
		t.Fatalf("expected script span patch, got %q", decision.RevisionIntent)
	}
}

func TestDecideUsesModelForGenerationIntent(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"generate_from_material",
			"confidence":0.91,
			"next_action":"start_run",
			"source_mode":"non_novel",
			"agent_reply":"我会启动非小说素材生成流程。",
			"requires_approval":true,
			"reason":"semantic_generation_request",
			"generation_config":{"target_episode_count":3,"episode_duration_minutes":1.5}
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "帮我安排一个追妻火葬场剧情并生成剧本", SourceMode: agent.SourceModeAuto},
	})

	if !client.called {
		t.Fatal("expected model to be called")
	}
	if decision.Runtime != "model" {
		t.Fatalf("expected model runtime, got %s", decision.Runtime)
	}
	if decision.NextAction != ActionStartRun || decision.SourceMode != agent.SourceModeNonNovel {
		t.Fatalf("expected non-novel start_run, got %s", formatDecisionForDebug(decision))
	}
}

func TestDecideUsesModelForCasualChat(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"chat_idle",
			"confidence":0.88,
			"next_action":"reply",
			"source_mode":"auto",
			"agent_reply":"你好，我在。",
			"requires_approval":false,
			"reason":"casual_chat"
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "你好呀", SourceMode: agent.SourceModeAuto},
	})

	if !client.called {
		t.Fatal("expected model to be called for casual chat")
	}
	if decision.NextAction != ActionReply || decision.Intent != IntentChatIdle {
		t.Fatalf("expected idle reply, got %s", formatDecisionForDebug(decision))
	}
}

func TestExplicitNoGenerationPreservesModelReply(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"chat_idle",
			"confidence":0.88,
			"next_action":"reply",
			"source_mode":"auto",
			"agent_reply":"我可以介绍完整流程，现在不会启动任何生成。",
			"requires_approval":false,
			"reason":"explicit_no_generation"
		}`,
	}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "你好，请介绍功能，不要开始生成。", SourceMode: agent.SourceModeAuto},
	})
	if decision.NextAction != ActionReply || decision.AgentReply != "我可以介绍完整流程，现在不会启动任何生成。" {
		t.Fatalf("explicit no-generation request was replaced: %s", formatDecisionForDebug(decision))
	}
	if decision.Trace == nil || decision.Trace.GuardApplied {
		t.Fatalf("no guard should replace an already safe model reply: %#v", decision.Trace)
	}
}

func TestNaturalNoGenerationPhrasePreservesModelReply(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"chat_idle",
			"confidence":0.9,
			"next_action":"reply",
			"source_mode":"auto",
			"agent_reply":"你好，有需要时直接告诉我。",
			"requires_approval":false,
			"reason":"ordinary_greeting"
		}`,
	}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "你好，这是一次普通问候，不需要生成或分析附件。", SourceMode: agent.SourceModeAuto},
	})
	if decision.NextAction != ActionReply || decision.AgentReply != "你好，有需要时直接告诉我。" {
		t.Fatalf("natural no-generation request was replaced: %s", formatDecisionForDebug(decision))
	}
	if decision.Trace == nil || decision.Trace.GuardApplied {
		t.Fatalf("no guard should replace an explicit natural-language rejection: %#v", decision.Trace)
	}
}

func TestInvalidApproveWithoutRunIsGuarded(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"approve_checkpoint",
			"confidence":0.9,
			"next_action":"approve_run",
			"source_mode":"non_novel",
			"agent_reply":"继续。",
			"requires_approval":false,
			"reason":"model_misread"
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "ok", SourceMode: agent.SourceModeAuto},
	})

	if decision.NextAction != ActionReply {
		t.Fatalf("expected guarded reply, got %s", formatDecisionForDebug(decision))
	}
	if !strings.Contains(decision.Reason, "guarded_invalid_action") {
		t.Fatalf("expected guard reason, got %s", decision.Reason)
	}
}

func TestStartRunRequiresConcreteSourceMode(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"generate_from_material",
			"confidence":0.82,
			"next_action":"start_run",
			"source_mode":"auto",
			"agent_reply":"开始生成。",
			"requires_approval":true,
			"reason":"missing_mode"
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "生成剧本", SourceMode: agent.SourceModeAuto},
	})

	if decision.NextAction != ActionReply {
		t.Fatalf("expected clarification reply, got %s", formatDecisionForDebug(decision))
	}
	if !strings.Contains(decision.AgentReply, "小说") {
		t.Fatalf("expected source-mode clarification, got %q", decision.AgentReply)
	}
}

func TestForcedNovelModeCanGuardMissingModelMode(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"generate_from_novel",
			"confidence":0.86,
			"next_action":"start_run",
			"source_mode":"auto",
			"agent_reply":"开始小说改编。",
			"requires_approval":true,
			"reason":"uses_ui_mode",
			"generation_config":{"target_episode_count":3,"episode_duration_minutes":1.5}
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "生成剧本", SourceMode: agent.SourceModeNovel},
	})

	if decision.NextAction != ActionStartRun || decision.SourceMode != agent.SourceModeNovel {
		t.Fatalf("expected forced novel start_run, got %s", formatDecisionForDebug(decision))
	}
}

func TestRequestNovelEvidenceOverridesModelNonNovelGuess(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"generate_from_material",
			"confidence":0.86,
			"next_action":"start_run",
			"source_mode":"non_novel",
			"agent_reply":"开始非小说素材生成。",
			"requires_approval":true,
			"reason":"model_guess",
			"generation_config":{"target_episode_count":3,"episode_duration_minutes":1.5}
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{
			Message:    "生成剧本\n[附件] 测试用书.txt",
			SourceMode: agent.SourceModeNovel,
		},
	})

	if decision.NextAction != ActionStartRun || decision.SourceMode != agent.SourceModeNovel {
		t.Fatalf("expected request novel evidence to override model guess, got %s", formatDecisionForDebug(decision))
	}
}

func TestConfigConfirmationUsesRecentGenerationIntentMode(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"generate_from_novel",
			"confidence":0.9,
			"next_action":"start_run",
			"source_mode":"auto",
			"agent_reply":"starting",
			"requires_approval":false,
			"reason":"config_confirmed"
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{
			Message:    "generate script",
			SourceMode: agent.SourceModeAuto,
			GenerationConfig: &agent.GenerationConfig{
				TargetEpisodeCount:     3,
				EpisodeDurationMinutes: 1.5,
			},
		},
		Conversation: &ConversationContext{
			RecentTurns: []ConversationTurn{
				{Role: "user", Content: "generate script"},
				{Role: "agent", Content: "need config", Intent: string(IntentGenerateFromNovel)},
			},
		},
	})

	if decision.NextAction != ActionStartRun || decision.SourceMode != agent.SourceModeNovel {
		t.Fatalf("expected recent novel intent to allow start_run, got %s", formatDecisionForDebug(decision))
	}
}

func TestStartRunWithoutGenerationConfigReturnsConfigPrompt(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"generate_from_novel",
			"confidence":0.86,
			"next_action":"start_run",
			"source_mode":"novel",
			"agent_reply":"start novel generation",
			"requires_approval":true,
			"reason":"generation_request_without_config"
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "generate script from this novel", SourceMode: agent.SourceModeNovel},
	})

	if decision.NextAction != ActionReply {
		t.Fatalf("expected reply instead of start_run, got %s", formatDecisionForDebug(decision))
	}
	if !decision.RequiresGenerationConfig {
		t.Fatalf("expected requires_generation_config=true, got %+v", decision)
	}
	if decision.SourceMode != agent.SourceModeNovel {
		t.Fatalf("expected novel source mode, got %s", decision.SourceMode)
	}
}

func TestGenerationRequestMisclassifiedAsChatStillReturnsConfigPrompt(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"chat_idle",
			"confidence":0.76,
			"next_action":"reply",
			"source_mode":"novel",
			"agent_reply":"Please provide episode count and duration.",
			"requires_generation_config":false,
			"requires_approval":false,
			"reason":"misclassified_generation_config_question"
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{
			Message:    "generate script\n[attachment] source.txt",
			SourceMode: agent.SourceModeAuto,
			Attachments: []FileAttachment{{
				FileName:    "source.txt",
				MimeType:    "text/plain",
				TextContent: "source content",
			}},
		},
	})

	if decision.NextAction != ActionReply {
		t.Fatalf("expected reply with config prompt, got %s", formatDecisionForDebug(decision))
	}
	if !decision.RequiresGenerationConfig {
		t.Fatalf("expected requires_generation_config=true, got %+v", decision)
	}
	if decision.Intent != IntentGenerateFromNovel {
		t.Fatalf("expected generate_from_novel intent, got %s", decision.Intent)
	}
	if decision.SourceMode != agent.SourceModeNovel {
		t.Fatalf("expected novel source mode, got %s", decision.SourceMode)
	}
}

func TestGenerationKeywordMisclassifiedAsChatStillRequiresConfig(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"chat_idle",
			"confidence":0.4,
			"next_action":"reply",
			"source_mode":"auto",
			"agent_reply":"我没太看懂你的意思。",
			"requires_approval":false,
			"reason":"misclassified_generation"
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "生成剧本", SourceMode: agent.SourceModeNovel},
	})

	if decision.NextAction != ActionReply {
		t.Fatalf("expected config prompt reply, got %s", formatDecisionForDebug(decision))
	}
	if !decision.RequiresGenerationConfig {
		t.Fatalf("expected requires_generation_config=true, got %+v", decision)
	}
	if decision.Intent != IntentGenerateFromNovel {
		t.Fatalf("expected generate_from_novel intent, got %s", decision.Intent)
	}
	if decision.SourceMode != agent.SourceModeNovel {
		t.Fatalf("expected novel source mode, got %s", decision.SourceMode)
	}
}

func TestFailedRunRetryUsesFailedStepTarget(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"rerun_step",
			"confidence":0.9,
			"next_action":"rerun_step",
			"source_mode":"non_novel",
			"agent_reply":"我会从失败任务继续。",
			"requires_approval":false,
			"reason":"resume_failed_task"
		}`,
	}
	agentController := New(client, "control-model")
	run := &agent.Run{
		Status:        agent.RunFailed,
		SourceMode:    agent.SourceModeNonNovel,
		CurrentStepID: "step_generate_script_unit",
		NextAction: map[string]any{
			"step_id":     "step_generate_script_unit",
			"task_id":     "task_generate_script_episode_3",
			"task_cursor": 2,
			"task_total":  8,
			"episode_id":  3,
		},
	}

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "从失败任务继续重跑", SourceMode: agent.SourceModeAuto},
		Run:     run,
	})

	if decision.NextAction != ActionRerunStep || decision.TargetArtifact != run.CurrentStepID {
		t.Fatalf("expected rerun current failed step, got %s target=%s", formatDecisionForDebug(decision), decision.TargetArtifact)
	}
}

func TestCheckpointRevisionScalarFieldNarrowsToFieldPatch(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"revise_checkpoint",
			"confidence":0.9,
			"next_action":"reply",
			"source_mode":"novel",
			"agent_reply":"I will update the protagonist goal.",
			"requires_approval":false,
			"reason":"checkpoint_revision",
			"target_artifact":"artifact_story_bible",
			"revision_intent":"patch_artifact_entity",
			"revision_target":{
				"artifact_id":"artifact_story_bible",
				"artifact_type":"story_bible",
				"field_path":"characters[name=protagonist].goal"
			}
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "make the protagonist goal clearer", SourceMode: agent.SourceModeAuto},
		Run: &agent.Run{
			Status:     agent.RunWaitingApproval,
			SourceMode: agent.SourceModeNovel,
		},
		CurrentStepContext: &CurrentStepContext{
			ArtifactID:   "artifact_story_bible",
			ArtifactType: "story_bible",
			Status:       agent.ArtifactPendingApproval,
		},
	})

	if decision.NextAction != ActionReviseCheckpoint {
		t.Fatalf("expected checkpoint revision, got %s", formatDecisionForDebug(decision))
	}
	if decision.RevisionIntent != RevisionIntentPatchArtifactField {
		t.Fatalf("expected scalar field patch, got %s target=%+v", decision.RevisionIntent, decision.RevisionTarget)
	}
	if decision.RevisionTarget == nil || decision.RevisionTarget.Scope != "field" {
		t.Fatalf("expected field scope target, got %+v", decision.RevisionTarget)
	}
}

func TestModelFailureDoesNotUseKeywordFallback(t *testing.T) {
	client := &fakeChatClient{configured: true, err: errors.New("timeout")}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "帮我生成短剧剧本", SourceMode: agent.SourceModeNonNovel},
	})

	if decision.NextAction != ActionReply || decision.Runtime != "fallback" {
		t.Fatalf("expected safe fallback reply, got %s runtime=%s", formatDecisionForDebug(decision), decision.Runtime)
	}
	if strings.Contains(decision.Reason, "generation") {
		t.Fatalf("fallback should not do semantic generation routing, got reason=%s", decision.Reason)
	}
}

func TestRevisionDecisionKeepsStructuredIntentAndTarget(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"revise_checkpoint",
			"confidence":0.88,
			"next_action":"revise_checkpoint",
			"source_mode":"novel",
			"agent_reply":"我会修改主角目标。",
			"requires_approval":false,
			"reason":"local_artifact_field_revision",
			"target_artifact":"story_bible",
			"revision_intent":"patch_artifact_field",
			"revision_target":{"artifact_type":"story_bible","field_path":"characters[0].goal","scope":"field"}
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "把主角的goal描述得更细致", SourceMode: agent.SourceModeNovel},
		Run: &agent.Run{
			Status:        agent.RunWaitingApproval,
			SourceMode:    agent.SourceModeNovel,
			CurrentStepID: "step_story_bible",
		},
		CurrentStepContext: &CurrentStepContext{
			ArtifactID:   "artifact_story_bible_1",
			ArtifactType: "story_bible",
		},
	})

	if decision.NextAction != ActionReviseCheckpoint {
		t.Fatalf("expected revise checkpoint, got %s", formatDecisionForDebug(decision))
	}
	if decision.RevisionIntent != RevisionIntentPatchArtifactField {
		t.Fatalf("expected patch_artifact_field, got %s", decision.RevisionIntent)
	}
	if decision.RevisionTarget == nil || decision.RevisionTarget.FieldPath != "characters[0].goal" || decision.RevisionTarget.ArtifactID != "artifact_story_bible_1" {
		t.Fatalf("expected enriched revision target, got %+v", decision.RevisionTarget)
	}
}

func TestHistoricalRevisionTargetDoesNotBorrowCurrentArtifactID(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"revise_checkpoint",
			"confidence":0.9,
			"next_action":"revise_checkpoint",
			"source_mode":"novel",
			"agent_reply":"我会修改故事圣经中的主角目标。",
			"requires_approval":false,
			"revision_intent":"patch_artifact_field",
			"revision_target":{"artifact_type":"story_bible","field_path":"characters[0].goal","scope":"field"}
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "把故事圣经里主角的目标改得更明确", SourceMode: agent.SourceModeNovel},
		Run:     &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
		CurrentStepContext: &CurrentStepContext{
			ArtifactID: "artifact_scripts_current", ArtifactType: "scripts",
		},
	})

	if decision.NextAction != ActionReviseCheckpoint || decision.RevisionTarget == nil {
		t.Fatalf("expected historical artifact revision, got %s", formatDecisionForDebug(decision))
	}
	if decision.RevisionTarget.ArtifactType != "story_bible" || decision.RevisionTarget.ArtifactID != "" {
		t.Fatalf("current scripts ID leaked into story_bible target: %+v", decision.RevisionTarget)
	}
}

func TestStructuredRevisionTargetStaysOnRequestedNodeAcrossBothFlows(t *testing.T) {
	cases := []struct {
		name         string
		mode         agent.SourceMode
		artifactType string
		intent       RevisionIntent
		episodeID    string
	}{
		{"novel_story_bible", agent.SourceModeNovel, "story_bible", RevisionIntentRegenerateArtifact, ""},
		{"novel_episode_split", agent.SourceModeNovel, "episode_split", RevisionIntentRegenerateArtifact, ""},
		{"novel_episode_cards", agent.SourceModeNovel, "episode_cards", RevisionIntentRegenerateArtifact, ""},
		{"novel_script_episode", agent.SourceModeNovel, "script_unit", RevisionIntentRegenerateScriptEpisode, "2"},
		{"nonnovel_material_bank", agent.SourceModeNonNovel, "material_bank", RevisionIntentRegenerateArtifact, ""},
		{"nonnovel_story_seed", agent.SourceModeNonNovel, "story_seed", RevisionIntentRegenerateArtifact, ""},
		{"nonnovel_series_blueprint", agent.SourceModeNonNovel, "series_blueprint", RevisionIntentRegenerateArtifact, ""},
		{"nonnovel_episode_cards", agent.SourceModeNonNovel, "episode_cards", RevisionIntentRegenerateArtifact, ""},
		{"nonnovel_script_episode", agent.SourceModeNonNovel, "script_unit", RevisionIntentRegenerateScriptEpisode, "2"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			episodeJSON := ""
			if testCase.episodeID != "" {
				episodeJSON = `,"episode_id":"` + testCase.episodeID + `"`
			}
			client := &fakeChatClient{configured: true, response: `{
				"intent":"revise_checkpoint",
				"confidence":0.92,
				"next_action":"revise_checkpoint",
				"source_mode":"` + string(testCase.mode) + `",
				"agent_reply":"ok",
				"requires_approval":false,
				"reason":"structured_revision",
				"target_artifact":"` + testCase.artifactType + `",
				"revision_intent":"` + string(testCase.intent) + `",
				"revision_target":{"artifact_type":"` + testCase.artifactType + `","scope":"artifact"` + episodeJSON + `}
			}`}
			controller := New(client, "control-model")
			decision := controller.Decide(context.Background(), Context{
				Request: MessageRequest{Message: "regenerate requested artifact", SourceMode: testCase.mode},
				Run:     &agent.Run{Status: agent.RunCompleted, SourceMode: testCase.mode},
				CurrentStepContext: &CurrentStepContext{
					ArtifactID: "artifact_current_scripts", ArtifactType: "scripts",
				},
			})
			if decision.NextAction != ActionReviseCheckpoint || decision.RevisionTarget == nil {
				t.Fatalf("expected executable structured revision, got %s", formatDecisionForDebug(decision))
			}
			if decision.RevisionTarget.ArtifactType != testCase.artifactType || decision.RevisionTarget.ArtifactID != "" || decision.RevisionTarget.EpisodeID != testCase.episodeID {
				t.Fatalf("requested target was contaminated by current view: %+v", decision.RevisionTarget)
			}
		})
	}
}

func TestRevisionQuestionNeverExecutesWorkerAction(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"revise_checkpoint",
			"confidence":0.85,
			"next_action":"revise_checkpoint",
			"source_mode":"novel",
			"agent_reply":"你想新增、替换还是删除哪一条来源证据？请补充说明。",
			"requires_approval":false,
			"reason":"target_detail_requires_clarification",
			"target_artifact":"story_bible",
			"revision_intent":"patch_artifact_field",
			"revision_target":{"artifact_type":"story_bible","field_path":"characters[0].source_evidence","scope":"field"}
		}`,
	}
	agentController := New(client, "control-model")
	decision := agentController.Decide(context.Background(), Context{
		Request:            MessageRequest{Message: "修改主角的来源证据", SourceMode: agent.SourceModeNovel},
		Run:                &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
		CurrentStepContext: &CurrentStepContext{ArtifactID: "artifact_story_bible_2", ArtifactType: "story_bible"},
	})
	if decision.NextAction != ActionReply {
		t.Fatalf("expected clarification-only reply, got %s", formatDecisionForDebug(decision))
	}
	if decision.AgentReply != "你想新增、替换还是删除哪一条来源证据？请补充说明。" {
		t.Fatalf("expected original clarification to stay visible, got %q", decision.AgentReply)
	}
}

func TestRevisionDecisionKeepsEntityIntentAndScope(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"revise_checkpoint",
			"confidence":0.9,
			"next_action":"revise_checkpoint",
			"source_mode":"novel",
			"agent_reply":"ok",
			"requires_approval":false,
			"reason":"local_artifact_entity_revision",
			"target_artifact":"story_bible",
			"revision_intent":"patch_artifact_entity",
			"revision_target":{"artifact_type":"story_bible","field_path":"characters[0].goal","scope":"entity"}
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "rewrite protagonist profile", SourceMode: agent.SourceModeNovel},
		Run: &agent.Run{
			Status:        agent.RunWaitingApproval,
			SourceMode:    agent.SourceModeNovel,
			CurrentStepID: "step_story_bible",
		},
		CurrentStepContext: &CurrentStepContext{
			ArtifactID:   "artifact_story_bible_1",
			ArtifactType: "story_bible",
		},
	})

	if decision.NextAction != ActionReviseCheckpoint {
		t.Fatalf("expected revise checkpoint, got %s", formatDecisionForDebug(decision))
	}
	if decision.RevisionIntent != RevisionIntentPatchArtifactEntity {
		t.Fatalf("expected patch_artifact_entity, got %s", decision.RevisionIntent)
	}
	if decision.RevisionTarget == nil || decision.RevisionTarget.Scope != "entity" || decision.RevisionTarget.ArtifactID != "artifact_story_bible_1" {
		t.Fatalf("expected enriched entity revision target, got %+v", decision.RevisionTarget)
	}
}

func TestEpisodeCardRegenerationDoesNotKeepScriptRevisionIntent(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"revise_checkpoint",
			"confidence":0.86,
			"next_action":"revise_checkpoint",
			"source_mode":"novel",
			"agent_reply":"ok",
			"requires_approval":false,
			"target_artifact":"episode_cards",
			"revision_intent":"regenerate_script_episode",
			"revision_target":{"artifact_type":"episode_cards","episode_id":"2","scope":"episode"}
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "regenerate episode 2 card", SourceMode: agent.SourceModeNovel},
		Run: &agent.Run{
			Status:        agent.RunWaitingApproval,
			SourceMode:    agent.SourceModeNovel,
			CurrentStepID: "step_approval_episode_cards",
		},
		CurrentStepContext: &CurrentStepContext{
			ArtifactID:   "artifact_episode_cards_1",
			ArtifactType: "episode_cards",
		},
	})

	if decision.NextAction != ActionReviseCheckpoint {
		t.Fatalf("expected revise checkpoint, got %s", formatDecisionForDebug(decision))
	}
	if decision.RevisionIntent != RevisionIntentPatchArtifactEntity {
		t.Fatalf("expected non-script episode target to normalize to entity patch, got %s", decision.RevisionIntent)
	}
	if decision.RevisionTarget == nil || decision.RevisionTarget.ArtifactType != "episode_cards" || decision.RevisionTarget.EpisodeID != "2" || decision.RevisionTarget.Scope != "entity" {
		t.Fatalf("expected episode_cards episode target, got %+v", decision.RevisionTarget)
	}
}

func TestWaitingApprovalAmbiguousRevisionIsClarifiedBeforeExecution(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"chat_idle",
			"confidence":0.5,
			"next_action":"reply",
			"source_mode":"auto",
			"agent_reply":"收到。",
			"requires_approval":false,
			"reason":"model_underclassified_revision"
		}`,
	}
	agentController := New(client, "control-model")

	decision := agentController.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "修改这个人物动机，让他更狠一点", SourceMode: agent.SourceModeNovel},
		Run: &agent.Run{
			Status:        agent.RunWaitingApproval,
			SourceMode:    agent.SourceModeNovel,
			CurrentStepID: "step_story_bible",
		},
		CurrentStepContext: &CurrentStepContext{
			ArtifactID:   "artifact_story_bible_1",
			ArtifactType: "story_bible",
		},
	})

	if decision.NextAction != ActionReply {
		t.Fatalf("expected ambiguous revision to ask before execution, got %s", formatDecisionForDebug(decision))
	}
	if !strings.Contains(decision.Reason, "guarded_invalid_action") {
		t.Fatalf("expected revision validation guard, got %s", decision.Reason)
	}
}

func TestSelectedScriptRevisionPromiseIsExecutedInsteadOfLeftAsChat(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"chat_idle",
		"confidence":0.8,
		"next_action":"reply",
		"source_mode":"novel",
		"agent_reply":"好的，我现在就实际修改这句台词并强化情绪，其余内容保持不变。",
		"requires_approval":false,
		"reason":"model_promised_revision_without_action"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "这句话你倒是改呀", SourceMode: agent.SourceModeNovel},
		Run:     &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
		FocusedContext: &FocusedContext{
			ArtifactID: "script_2", ArtifactType: "script_unit", EpisodeID: "2", SceneID: "scene_2_1",
			NodeID: "line_6", SelectedText: "陈知: （揉眼睛装傻）你谁啊？",
		},
	})
	if decision.NextAction != ActionReviseCheckpoint || decision.RevisionIntent != RevisionIntentPatchScriptSpan {
		t.Fatalf("expected selected script revision to execute, got %+v", decision)
	}
	if decision.RevisionTarget == nil || decision.RevisionTarget.ArtifactID != "script_2" || decision.RevisionTarget.NodeID != "line_6" {
		t.Fatalf("expected precise selected line target, got %+v", decision.RevisionTarget)
	}
}

func TestCurrentScriptSelectionOverridesConflictingModelTarget(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"revise_checkpoint","confidence":0.91,"next_action":"revise_checkpoint","source_mode":"novel",
		"agent_reply":"我会修改当前产物。","reason":"model_used_aggregate_target",
		"revision_intent":"regenerate_artifact",
		"revision_target":{"artifact_id":"scripts_1","artifact_type":"scripts","scope":"artifact"}
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "加强这句话的情绪", SourceMode: agent.SourceModeNovel},
		Run:     &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
		FocusedContext: &FocusedContext{
			ArtifactID: "artifact_001110", ArtifactType: "script_unit", EpisodeID: "14", SceneID: "scene_14_1",
			NodeID: "scene_14_1-line-8", SelectedText: "别打了！都是一家人！", SelectionScope: "line",
		},
	})
	if decision.NextAction != ActionReviseCheckpoint || decision.RevisionIntent != RevisionIntentPatchScriptSpan {
		t.Fatalf("expected precise selected-line patch, got %+v", decision)
	}
	if decision.RevisionTarget == nil || decision.RevisionTarget.ArtifactID != "artifact_001110" || decision.RevisionTarget.NodeID != "scene_14_1-line-8" {
		t.Fatalf("model target overrode current selection: %+v", decision.RevisionTarget)
	}
	if !strings.Contains(decision.AgentReply, "只修改第14集当前选中的内容") {
		t.Fatalf("execution reply is not scoped to the selected line: %q", decision.AgentReply)
	}
}

func TestImpatientFollowupRecoversRecentUnconsumedSelection(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"chat_idle","confidence":0.7,"next_action":"reply","source_mode":"novel",
		"agent_reply":"好的。","reason":"underclassified_followup"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "改啊你倒是", SourceMode: agent.SourceModeNovel},
		Run:     &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
		Conversation: &ConversationContext{RecentTurns: []ConversationTurn{
			{Role: "user", Content: "加强这句话的情绪", SelectionContext: map[string]any{
				"artifact_id": "artifact_001110", "artifact_type": "script_unit", "episode_id": "14",
				"scene_id": "scene_14_1", "node_id": "scene_14_1-line-8", "selected_text": "别打了！都是一家人！",
			}},
			{Role: "agent", Content: "未执行的泛化回复", Intent: string(IntentChatIdle)},
		}},
	})
	if decision.NextAction != ActionReviseCheckpoint || decision.Reason != "forced_revision_from_recent_selection" {
		t.Fatalf("expected recent selection revision, got %+v", decision)
	}
	if decision.RevisionTarget == nil || decision.RevisionTarget.NodeID != "scene_14_1-line-8" {
		t.Fatalf("recent selected line was not recovered: %+v", decision.RevisionTarget)
	}
}

func TestNewSelectionCanReplaceFailedLocalRevision(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"revise_checkpoint","confidence":0.9,"next_action":"revise_checkpoint","source_mode":"novel",
		"agent_reply":"放大动作。","reason":"new_selection_after_failed_patch","revision_intent":"patch_script_span"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "动作变大一些", SourceMode: agent.SourceModeNovel},
		Run: &agent.Run{Status: agent.RunFailed, SourceMode: agent.SourceModeNovel, Metadata: map[string]any{
			"active_revision": map[string]any{"revision_intent": "patch_script_span", "artifact_type": "script_unit"},
		}},
		FocusedContext: &FocusedContext{
			ArtifactID: "artifact_001110", ArtifactType: "script_unit", EpisodeID: "14", SceneID: "scene_14_1",
			NodeID: "scene_14_1-line-9", SelectedText: "△刘德贵冲上去拉架，伸手拽刘大山。", SelectionScope: "line",
		},
	})
	if decision.NextAction != ActionReviseCheckpoint || decision.RevisionTarget == nil || decision.RevisionTarget.NodeID != "scene_14_1-line-9" {
		t.Fatalf("new selection did not replace failed local revision: %+v", decision)
	}
	if !strings.Contains(decision.AgentReply, "放弃上一次失败的局部修改") {
		t.Fatalf("reply did not explain failed revision replacement: %q", decision.AgentReply)
	}
}

func TestEmotionIncreaseCanReplaceFailedLocalRevision(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"rerun_step","confidence":0.88,"next_action":"rerun_step","source_mode":"novel",
		"agent_reply":"我会重试失败步骤。","reason":"model_confused_new_revision_with_retry"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "情绪增加", SourceMode: agent.SourceModeNovel},
		Run: &agent.Run{Status: agent.RunFailed, SourceMode: agent.SourceModeNovel, Metadata: map[string]any{
			"active_revision": map[string]any{"revision_intent": "patch_script_span", "artifact_type": "script_unit"},
		}},
		FocusedContext: &FocusedContext{
			ArtifactID: "artifact_001110", ArtifactType: "script_unit", EpisodeID: "14", SceneID: "scene_14_1",
			NodeID: "scene_14_1-line-8", SelectedText: "别打了！都是一家人！", SelectionScope: "line",
		},
	})
	if decision.NextAction != ActionReviseCheckpoint || decision.RevisionIntent != RevisionIntentPatchScriptSpan {
		t.Fatalf("explicit new revision was mistaken for retry: %+v", decision)
	}
}

func TestShortConfirmationInheritsRecentScriptSelectionAndExecutes(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"chat_idle",
		"confidence":0.78,
		"next_action":"reply",
		"source_mode":"novel",
		"agent_reply":"好的，我现在就实际修改第2集这句台词，强化装傻挑衅下暗藏的怒火，其余内容保持不变。",
		"requires_approval":false,
		"reason":"confirmation_followup"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "ok", SourceMode: agent.SourceModeNovel},
		Run:     &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
		Conversation: &ConversationContext{RecentTurns: []ConversationTurn{
			{Role: "user", Content: "加强这句话的情绪", SelectionContext: map[string]any{
				"artifact_id": "script_2", "artifact_type": "script_unit", "episode_id": "2", "scene_id": "scene_2_1",
				"node_id": "line_6", "line_ids": []any{"line_6"}, "selected_text": "陈知: （揉眼睛装傻）你谁啊？",
			}},
			{Role: "agent", Content: "请确认是否修改", Intent: string(IntentExplainState)},
		}},
	})
	if decision.NextAction != ActionReviseCheckpoint || decision.Reason != "forced_revision_from_conversation_continuation" {
		t.Fatalf("expected short confirmation to execute pending revision, got %+v", decision)
	}
	if decision.RevisionTarget == nil || decision.RevisionTarget.ArtifactID != "script_2" || decision.RevisionTarget.NodeID != "line_6" {
		t.Fatalf("expected recent selected line to be inherited, got %+v", decision.RevisionTarget)
	}
}

func TestGreetingDoesNotReuseConsumedHistoricalSelection(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"chat_idle","confidence":0.91,"next_action":"reply","source_mode":"novel",
		"agent_reply":"你好，故事圣经的新版本正在等你确认。","requires_approval":false,"reason":"greeting"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "你好", SourceMode: agent.SourceModeNovel},
		Run:     &agent.Run{Status: agent.RunWaitingApproval, SourceMode: agent.SourceModeNovel},
		Conversation: &ConversationContext{RecentTurns: []ConversationTurn{
			{Role: "user", Content: "加强这句话", SelectionContext: map[string]any{
				"artifact_id": "script_1", "artifact_type": "script_unit", "selected_text": "旧台词",
			}},
			{Role: "agent", Content: "已完成修改。", Intent: string(IntentReviseCheckpoint)},
		}},
	})
	if decision.NextAction != ActionReply || decision.Intent != IntentChatIdle {
		t.Fatalf("greeting reused a consumed historical selection: %+v", decision)
	}
}

func TestBareSelectedRevisionAsksForDirection(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"revise_checkpoint","confidence":0.88,"next_action":"revise_checkpoint","source_mode":"novel",
		"agent_reply":"我会加强这句话的情绪。","requires_approval":false,"reason":"inferred_change",
		"revision_intent":"patch_script_span",
		"revision_target":{"artifact_id":"script_1","artifact_type":"script_unit","scene_id":"scene_1","node_id":"line_1","scope":"selection"}
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request:        MessageRequest{Message: "修改这句话", SourceMode: agent.SourceModeNovel},
		Run:            &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
		FocusedContext: &FocusedContext{ArtifactID: "script_1", ArtifactType: "script_unit", SceneID: "scene_1", NodeID: "line_1", SelectedText: "旧台词"},
	})
	if decision.NextAction != ActionReply {
		t.Fatalf("bare revision should clarify instead of executing: %+v", decision)
	}
	if !strings.Contains(decision.AgentReply, "希望改成什么方向") {
		t.Fatalf("missing useful clarification: %q", decision.AgentReply)
	}
}

func TestPendingApprovalBlocksRevisionOfDifferentArtifact(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"revise_checkpoint","confidence":0.9,"next_action":"revise_checkpoint","source_mode":"novel",
		"agent_reply":"我会修改剧本。","requires_approval":false,"reason":"revision",
		"revision_intent":"patch_script_span",
		"revision_target":{"artifact_id":"script_1","artifact_type":"script_unit","scene_id":"scene_1","node_id":"line_1","scope":"selection"}
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request:         MessageRequest{Message: "把这句台词改得更愤怒", SourceMode: agent.SourceModeNovel},
		Run:             &agent.Run{Status: agent.RunWaitingApproval, SourceMode: agent.SourceModeNovel},
		ApprovalRequest: &agent.ApprovalRequest{Status: "pending", ProposedAction: map[string]any{"approved_artifact": "story_bible"}},
		FocusedContext:  &FocusedContext{ArtifactID: "script_1", ArtifactType: "script_unit", SceneID: "scene_1", NodeID: "line_1", SelectedText: "旧台词"},
	})
	if decision.NextAction != ActionReply || !strings.Contains(decision.AgentReply, "故事圣经等待确认") {
		t.Fatalf("cross-artifact revision replaced pending approval: %+v", decision)
	}
}

func TestModelRevisionActionDoesNotInheritSelectionWithoutPendingClarification(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"revise_checkpoint",
		"confidence":0.9,
		"next_action":"revise_checkpoint",
		"source_mode":"novel",
		"agent_reply":"好的，我现在就实际修改第2集这句台词并强化情绪，其余内容保持不变。",
		"requires_approval":false,
		"reason":"confirmed_revision",
		"revision_intent":"patch_script_span",
		"revision_target":{"artifact_type":"script_unit","episode_id":"2","scope":"selection"}
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "ok", SourceMode: agent.SourceModeNovel},
		Run:     &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
		Conversation: &ConversationContext{RecentTurns: []ConversationTurn{{Role: "user", SelectionContext: map[string]any{
			"artifact_id": "script_2", "artifact_type": "script_unit", "episode_id": "2", "scene_id": "scene_2_1",
			"node_id": "line_6", "selected_text": "陈知: （揉眼睛装傻）你谁啊？",
		}}}},
	})
	if decision.NextAction != ActionReply {
		t.Fatalf("unconfirmed historical selection should not execute, got %+v", decision)
	}
	if decision.RevisionTarget != nil {
		t.Fatalf("historical selection leaked into guarded decision: %+v", decision.RevisionTarget)
	}
}

func TestQuotedQuestionInSelectedDialogueDoesNotCancelRevision(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"revise_checkpoint",
		"confidence":0.9,
		"next_action":"revise_checkpoint",
		"source_mode":"novel",
		"agent_reply":"我现在修改这句台词「你谁啊？」，强化暗藏的怒火，其余内容不动。改完请你核对。",
		"requires_approval":false,
		"reason":"confirmed_revision",
		"revision_intent":"patch_script_span",
		"revision_target":{"artifact_id":"script_2","artifact_type":"script_unit","episode_id":"2","scene_id":"scene_2_1","node_id":"line_6","scope":"selection"}
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "ok", SourceMode: agent.SourceModeNovel},
		Run:     &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
	})
	if decision.NextAction != ActionReviseCheckpoint {
		t.Fatalf("quoted dialogue question must not cancel execution: %+v", decision)
	}
	if agentReplyAsksQuestion(decision.AgentReply) {
		t.Fatalf("quoted dialogue was incorrectly treated as an agent question: %q", decision.AgentReply)
	}
}

func TestAgentClarificationQuestionStillCancelsRevision(t *testing.T) {
	if !agentReplyAsksQuestion("你希望我现在修改这句台词吗？") {
		t.Fatal("expected an actual clarification question to be detected")
	}
}

func TestExplicitFocusedRevisionSkipsControlModel(t *testing.T) {
	client := &fakeChatClient{configured: true, err: errors.New("control model must not be called")}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "这句话情绪更丰富一些", SourceMode: agent.SourceModeNovel},
		Run:     &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
		FocusedContext: &FocusedContext{
			ArtifactID: "script_1", ArtifactType: "script_unit", EpisodeID: "1", SceneID: "scene_1_3",
			NodeID: "line_8", SelectedText: "旧动作",
		},
	})
	if client.called {
		t.Fatal("explicit focused revision should not send the full project context to the control model")
	}
	if decision.NextAction != ActionReviseCheckpoint || decision.RevisionIntent != RevisionIntentPatchScriptSpan {
		t.Fatalf("expected deterministic selected revision, got %+v", decision)
	}
}

func TestSelectedRevisionNegationDoesNotUseFastExecutionPath(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"chat_idle","confidence":0.96,"next_action":"reply","source_mode":"novel",
		"agent_reply":"好的，我只分析这句动作，不执行修改。","requires_approval":false,"reason":"user_prohibited_revision"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "先不要修改，只分析这句话的问题", SourceMode: agent.SourceModeNovel},
		Run:     &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
		FocusedContext: &FocusedContext{
			ArtifactID: "script_1", ArtifactType: "script_unit", EpisodeID: "1", SceneID: "scene_1", NodeID: "line_1", SelectedText: "旧动作",
		},
	})
	if !client.called {
		t.Fatal("negated selected request should be interpreted by the control model")
	}
	if decision.NextAction != ActionReply {
		t.Fatalf("negated selected request executed a revision: %+v", decision)
	}
}

func TestSelectedRevisionAdviceDoesNotUseFastExecutionPath(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"explain_current_state","confidence":0.94,"next_action":"reply","source_mode":"novel",
		"agent_reply":"这句可以增加肢体反应，但我暂时不会修改。","requires_approval":false,"reason":"revision_advice"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "你觉得这句话应该怎么改", SourceMode: agent.SourceModeNovel},
		Run:     &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
		FocusedContext: &FocusedContext{
			ArtifactID: "script_1", ArtifactType: "script_unit", EpisodeID: "1", SceneID: "scene_1", NodeID: "line_1", SelectedText: "旧动作",
		},
	})
	if !client.called {
		t.Fatal("revision advice should be interpreted rather than executed deterministically")
	}
	if decision.NextAction != ActionReply {
		t.Fatalf("revision advice executed a revision: %+v", decision)
	}
}

type inspectCorrectionClient struct {
	responses []string
	calls     int
}

func (c *inspectCorrectionClient) Configured() bool { return true }

func (c *inspectCorrectionClient) Complete(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	index := c.calls
	c.calls++
	if index >= len(c.responses) {
		return llm.ChatResponse{}, errors.New("unexpected control-model call")
	}
	return llm.ChatResponse{Content: c.responses[index]}, nil
}

func TestInspectArtifactDeferredReplyIsCorrectedImmediately(t *testing.T) {
	client := &inspectCorrectionClient{responses: []string{
		`{"intent":"inspect_artifact","confidence":0.98,"next_action":"inspect_artifact","source_mode":"novel","agent_reply":"我来看看这句话的问题，稍等。","reason":"inspect_selection"}`,
		`{"intent":"inspect_artifact","confidence":0.98,"next_action":"inspect_artifact","source_mode":"novel","agent_reply":"这句动作交代了人物、状态和关键道具，但信息集中在一句里，镜头重点不够突出；可以重点检查动作节奏与录音笔的视觉强调。","reason":"inspect_selection_direct_answer"}`,
	}}
	agentUnderTest := New(client, "control-model")
	decision := agentUnderTest.Decide(context.Background(), Context{
		Request: MessageRequest{Message: "先不要修改，只分析这句话的问题", SourceMode: agent.SourceModeNovel, SelectedArtifactID: "artifact_1", SelectedText: "她从箱底翻出一支录音笔。"},
		Run:     &agent.Run{RunID: "run_1", SourceMode: agent.SourceModeNovel, Status: agent.RunCompleted},
	})
	if client.calls != 2 {
		t.Fatalf("expected one correction call, got %d", client.calls)
	}
	if decision.NextAction != ActionInspectArtifact || inspectReplyDefersWork(decision) || !strings.Contains(decision.AgentReply, "镜头重点") {
		t.Fatalf("expected immediate analysis, got %#v", decision)
	}
}

func TestInspectArtifactDeferredReplyFailsClosedWithoutPromise(t *testing.T) {
	client := &inspectCorrectionClient{responses: []string{
		`{"intent":"inspect_artifact","confidence":0.98,"next_action":"inspect_artifact","source_mode":"novel","agent_reply":"我来看看，稍等。","reason":"inspect_selection"}`,
		`{"intent":"inspect_artifact","confidence":0.98,"next_action":"inspect_artifact","source_mode":"novel","agent_reply":"我会分析后再告诉你。","reason":"still_deferred"}`,
	}}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "只分析这句话", SourceMode: agent.SourceModeNovel, SelectedArtifactID: "artifact_1", SelectedText: "她抬起头。"},
		Run:     &agent.Run{RunID: "run_1", SourceMode: agent.SourceModeNovel, Status: agent.RunCompleted},
	})
	if decision.NextAction != ActionReply || inspectReplyDefersWork(decision) || strings.Contains(decision.AgentReply, "稍等") {
		t.Fatalf("deferred inspection must fail closed without a promise: %#v", decision)
	}
}

func TestRetryAfterControlTimeoutReplaysSelectedRevision(t *testing.T) {
	client := &fakeChatClient{configured: true, err: errors.New("control model must not be called")}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "重试", SourceMode: agent.SourceModeNovel},
		Run:     &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
		Conversation: &ConversationContext{RecentTurns: []ConversationTurn{
			{Role: "user", Content: "这句话情绪更丰富一些", SelectionContext: map[string]any{
				"artifact_id": "script_1", "artifact_type": "script_unit", "episode_id": "1", "scene_id": "scene_1_3",
				"node_id": "line_8", "selected_text": "旧动作",
			}},
			{Role: "agent", Intent: string(IntentUnsupported), Content: "控制模型请求超时，所以本次没有执行。"},
		}},
	})
	if client.called {
		t.Fatal("retrying a preserved selected request should not call the control model again")
	}
	if decision.NextAction != ActionReviseCheckpoint || decision.Reason != "forced_revision_retry_after_control_failure" {
		t.Fatalf("expected the failed selected request to be replayed, got %+v", decision)
	}
}

func TestQuestionReplyBlocksStartRunAcrossAllActions(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"generate_from_novel","confidence":0.96,"next_action":"start_run","source_mode":"novel",
		"agent_reply":"我会开始生成。请问你希望做几集？","requires_approval":false,"reason":"contradictory_question"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "生成剧本", SourceMode: agent.SourceModeNovel, GenerationConfig: &agent.GenerationConfig{
			TargetEpisodeCount: 2, EpisodeDurationMinutes: 1.5,
		}},
	})
	if decision.NextAction != ActionReply {
		t.Fatalf("a question must never execute start_run, got %+v", decision)
	}
}

func TestInputRequestWithoutQuestionMarkBlocksExecution(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"generate_from_novel","confidence":0.96,"next_action":"start_run","source_mode":"novel",
		"agent_reply":"开始前请补充目标风格","requires_approval":false,"reason":"asks_without_question_mark"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "生成剧本", SourceMode: agent.SourceModeNovel, GenerationConfig: &agent.GenerationConfig{
			TargetEpisodeCount: 2, EpisodeDurationMinutes: 1.5,
		}},
	})
	if decision.NextAction != ActionReply {
		t.Fatalf("a request for more user input must not execute, got %+v", decision)
	}
}

func TestControlModelReceivesCompactedRunMetadata(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"explain_current_state","confidence":0.9,"next_action":"reply","source_mode":"novel",
		"agent_reply":"当前流程已完成。","requires_approval":false,"reason":"state"
	}`}
	large := strings.Repeat("历史检查点", 30000)
	New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "当前到哪了", SourceMode: agent.SourceModeNovel},
		Run: &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel, Metadata: map[string]any{
			"active_task":      map[string]any{"task_id": "task_14", "large_internal_payload": large},
			"batch_checkpoint": large,
		}},
		CurrentStepContext: &CurrentStepContext{PayloadExcerpt: map[string]any{"script_text": large}},
	})
	if !client.called || len(client.request.Messages) < 2 {
		t.Fatal("control model request was not captured")
	}
	payload := client.request.Messages[1].Content
	if len([]rune(payload)) > 12000 {
		t.Fatalf("control model context was not compacted: %d chars", len([]rune(payload)))
	}
	if !strings.Contains(payload, "task_14") {
		t.Fatalf("compaction dropped the active task locator: %s", payload)
	}
}

func TestReplyCannotClaimRevisionWasStarted(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"chat_idle","confidence":0.8,"next_action":"reply","source_mode":"novel",
		"agent_reply":"我会按你的要求修订当前待确认产物，生成新版后再让你确认。","requires_approval":false,"reason":"false_promise"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "重试", SourceMode: agent.SourceModeNovel},
		Run:     &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
	})
	if decision.NextAction != ActionReply || strings.Contains(decision.AgentReply, "生成新版") {
		t.Fatalf("reply-only decision still claims execution: %+v", decision)
	}
}

func TestControlTimeoutUsesUnsupportedIntentAndSpecificReason(t *testing.T) {
	client := &fakeChatClient{configured: true, err: errors.New("context deadline exceeded")}
	decision := New(client, "control-model").Decide(context.Background(), Context{Request: MessageRequest{Message: "解释当前状态"}})
	if decision.Intent != IntentUnsupported || decision.Reason != "control_model_timeout" {
		t.Fatalf("control timeout classification is inaccurate: %+v", decision)
	}
	if !strings.Contains(decision.AgentReply, "请求超时") {
		t.Fatalf("control timeout reply is not explicit: %q", decision.AgentReply)
	}
}

func TestRevisionStatusQuestionDoesNotExecuteRecentSelection(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"explain_current_state",
		"confidence":0.9,
		"next_action":"reply",
		"source_mode":"novel",
		"agent_reply":"当前这句台词还没有修改，你要我现在修改吗？",
		"requires_approval":false,
		"reason":"status_question"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "改了吗", SourceMode: agent.SourceModeNovel},
		Run:     &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
		Conversation: &ConversationContext{RecentTurns: []ConversationTurn{{Role: "user", SelectionContext: map[string]any{
			"artifact_id": "script_2", "artifact_type": "script_unit", "selected_text": "旧台词",
		}}}},
	})
	if decision.NextAction != ActionReply || decision.Intent != IntentExplainState {
		t.Fatalf("expected a status-only reply, got %s", formatDecisionForDebug(decision))
	}
}

func TestRevisionStatusQuestionWithModifyKeywordDoesNotExecute(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"explain_current_state","confidence":0.92,"next_action":"reply","source_mode":"novel",
		"agent_reply":"已经修改完成，当前在等待你确认。","requires_approval":false,"reason":"status_question"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "刚才修改完成了吗？", SourceMode: agent.SourceModeNovel},
		Run:     &agent.Run{Status: agent.RunWaitingApproval, SourceMode: agent.SourceModeNovel},
	})
	if decision.NextAction != ActionReply || decision.Intent != IntentExplainState {
		t.Fatalf("status question was incorrectly executed as revision: %+v", decision)
	}
}

func TestDecisionPromptStatesSemanticIntentPolicy(t *testing.T) {
	prompt := decisionSystemPrompt()
	for _, required := range []string{
		"用户表达是开放集合",
		"禁止把中文或英文短语枚举成意图词典",
		"根据上下文整体理解用户意图",
		"next_action 是有限动作合同",
		"rerun_step 必须指向失败的具体任务所在 step",
		"revision_intent",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("expected system prompt to contain %q", required)
		}
	}
}

func TestAttachmentTextIsPassedAsTruncatedContext(t *testing.T) {
	client := &fakeChatClient{
		configured: true,
		response: `{
			"intent":"chat_idle",
			"confidence":0.7,
			"next_action":"reply",
			"source_mode":"auto",
			"agent_reply":"收到附件。",
			"requires_approval":false,
			"reason":"attachment_context"
		}`,
	}
	agentController := New(client, "control-model")

	originalText := strings.Repeat("正文", 2000)
	input := Context{
		Request: MessageRequest{
			Message:    "看一下这个附件",
			SourceMode: agent.SourceModeAuto,
			Attachments: []FileAttachment{{
				FileName:      "source.txt",
				TextContent:   originalText,
				ContentBase64: "secret",
			}},
		},
	}
	agentController.Decide(context.Background(), input)

	payload := client.request.Messages[1].Content
	if !strings.Contains(payload, "...[truncated]") {
		t.Fatalf("expected truncated attachment context, got %s", payload)
	}
	if strings.Contains(payload, "secret") {
		t.Fatal("content_base64 must not be sent to control model")
	}
	if input.Request.Attachments[0].TextContent != originalText || input.Request.Attachments[0].ContentBase64 != "secret" {
		t.Fatal("control-model context compaction must not mutate the generation attachment")
	}
}

func TestScriptFormatQuestionDoesNotForceGeneration(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"chat_idle","confidence":0.95,"next_action":"reply","source_mode":"auto",
		"agent_reply":"我可以解释短剧剧本格式。","reason":"format_question"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "短剧剧本格式有哪些要求？", SourceMode: agent.SourceModeNovel},
	})
	if decision.NextAction != ActionReply || decision.RequiresGenerationConfig {
		t.Fatalf("format question must remain chat, got %s", formatDecisionForDebug(decision))
	}
}

func TestPauseActionIsAllowedWhileRunIsGenerating(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"pause_run","confidence":0.98,"next_action":"pause_run","source_mode":"novel",
		"agent_reply":"已暂停当前流程。","reason":"user_pause"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "先暂停一下", SourceMode: agent.SourceModeAuto},
		Run:     &agent.Run{RunID: "run_running", Status: agent.RunRunning, SourceMode: agent.SourceModeNovel},
	})
	if decision.NextAction != ActionPauseRun {
		t.Fatalf("running run should accept pause action, got %s", formatDecisionForDebug(decision))
	}
}

func TestResumeActionIsAllowedForUserPausedRun(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"resume_run","confidence":0.98,"next_action":"resume_run","source_mode":"novel",
		"agent_reply":"继续执行。","reason":"user_resume"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "继续执行", SourceMode: agent.SourceModeAuto},
		Run: &agent.Run{RunID: "run_paused", Status: agent.RunPaused, SourceMode: agent.SourceModeNovel,
			Metadata: map[string]any{"paused_by_user": true}},
	})
	if decision.NextAction != ActionResumeRun || decision.Intent != IntentResumeRun {
		t.Fatalf("user-paused run should accept resume action, got %s", formatDecisionForDebug(decision))
	}
}

func TestApproveActionCannotResumeUserPausedRunWithoutApproval(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"approve_checkpoint","confidence":0.98,"next_action":"approve_run","source_mode":"novel",
		"agent_reply":"继续执行。","reason":"incorrect_approval"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "继续执行", SourceMode: agent.SourceModeAuto},
		Run:     &agent.Run{RunID: "run_paused", Status: agent.RunPaused, SourceMode: agent.SourceModeNovel},
	})
	if decision.NextAction != ActionReply {
		t.Fatalf("paused run without approval must not use approve action, got %s", formatDecisionForDebug(decision))
	}
}

func TestResumeActionCannotBypassPausedApproval(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"resume_run","confidence":0.98,"next_action":"resume_run","source_mode":"novel",
		"agent_reply":"继续执行。","reason":"incorrect_resume"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "继续执行", SourceMode: agent.SourceModeAuto},
		Run: &agent.Run{
			RunID: "run_paused_approval", Status: agent.RunPaused, SourceMode: agent.SourceModeNovel,
			ApprovalRequestID: "approval_1",
		},
	})
	if decision.NextAction != ActionReply {
		t.Fatalf("paused approval must not use resume action, got %s", formatDecisionForDebug(decision))
	}
}

func TestResumeActionRestoresUserPausedApprovalWithoutApprovingIt(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"resume_run","confidence":0.98,"next_action":"resume_run","source_mode":"novel",
		"agent_reply":"恢复到故事圣经确认。","reason":"user_resume"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "继续", SourceMode: agent.SourceModeAuto},
		Run: &agent.Run{
			RunID: "run_paused_approval", Status: agent.RunPaused, SourceMode: agent.SourceModeNovel,
			ApprovalRequestID: "approval_1", Metadata: map[string]any{"paused_by_user": true},
		},
	})
	if decision.NextAction != ActionResumeRun {
		t.Fatalf("user-paused approval should resume to the confirmation point, got %s", formatDecisionForDebug(decision))
	}
}

func TestFailedStatusQuestionCannotTriggerRerun(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"rerun_step","confidence":0.94,"next_action":"rerun_step","source_mode":"novel",
		"agent_reply":"我现在再试一次。","reason":"inferred_retry"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "又失败了？", SourceMode: agent.SourceModeAuto},
		Run: &agent.Run{RunID: "run_failed", Status: agent.RunFailed, SourceMode: agent.SourceModeNovel,
			CurrentStepID: "step_split_episodes"},
	})
	if decision.NextAction != ActionReply || decision.Intent != IntentChatIdle {
		t.Fatalf("a status question must not execute rerun, got %s", formatDecisionForDebug(decision))
	}
}

func TestExplicitFailedRetryStillExecutes(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"rerun_step","confidence":0.94,"next_action":"rerun_step","source_mode":"novel",
		"agent_reply":"从失败处继续。","reason":"explicit_retry"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "从失败处继续", SourceMode: agent.SourceModeAuto},
		Run: &agent.Run{RunID: "run_failed", Status: agent.RunFailed, SourceMode: agent.SourceModeNovel,
			CurrentStepID: "step_split_episodes"},
	})
	if decision.NextAction != ActionRerunStep {
		t.Fatalf("an explicit retry command should rerun, got %s", formatDecisionForDebug(decision))
	}
}

func TestFocusedRevisionPreservesSpecificModelReply(t *testing.T) {
	decision := focusedRevisionDecision(Decision{
		AgentReply:     "我会把第8集这句台词改得更令人反感，其他内容保持不变。",
		RevisionIntent: RevisionIntentPatchScriptSpan,
	}, Context{Run: &agent.Run{Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel}}, &FocusedContext{
		ArtifactID: "script_8", ArtifactType: "script_unit", EpisodeID: "8", SelectedText: "旧句", NodeID: "line_4",
	}, false)
	if decision.AgentReply != "我会把第8集这句台词改得更令人反感，其他内容保持不变。" {
		t.Fatalf("specific controller reply was replaced: %q", decision.AgentReply)
	}
}

func TestRestoredFailedRevisionCanBeRetriedFromWaitingApproval(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"rerun_step","confidence":0.94,"next_action":"rerun_step","source_mode":"novel",
		"agent_reply":"重新执行刚才失败的修改。","reason":"explicit_retry"
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request: MessageRequest{Message: "重新试一下"},
		Run: &agent.Run{RunID: "run_restored", Status: agent.RunWaitingApproval, SourceMode: agent.SourceModeNovel,
			CurrentStepID: "step_approval_episode_split", Metadata: map[string]any{"last_failed_task": map[string]any{
				"task_id": "task_split_patch", "step_id": "step_split_episodes", "status": "failed",
			}}},
	})
	if decision.NextAction != ActionRerunStep || decision.TargetArtifact != "step_split_episodes" {
		t.Fatalf("restored failed revision was not retryable: %s target=%s", formatDecisionForDebug(decision), decision.TargetArtifact)
	}
}

func TestEpisodeSplitRequestUsesCollectionRevisionContract(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"revise_checkpoint","confidence":0.96,"next_action":"revise_checkpoint","source_mode":"novel",
		"agent_reply":"I will split episode 2 into two episodes.","reason":"structural_episode_change",
		"revision_intent":"patch_artifact_entity",
		"revision_target":{"artifact_type":"episode_split","episode_id":"2","scope":"entity"}
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request:            MessageRequest{Message: "split episode 2 into two"},
		Run:                &agent.Run{RunID: "run_split", Status: agent.RunCompleted, SourceMode: agent.SourceModeNovel},
		CurrentStepContext: &CurrentStepContext{ArtifactID: "split_v1", ArtifactType: "episode_split"},
	})
	if decision.RevisionIntent != RevisionIntentPatchArtifactCollection {
		t.Fatalf("structural episode request kept scalar/entity intent: %s", formatDecisionForDebug(decision))
	}
	if decision.RevisionTarget == nil || decision.RevisionTarget.FieldPath != "episodes" || decision.RevisionTarget.Scope != "collection" {
		t.Fatalf("collection target was not normalized: %+v", decision.RevisionTarget)
	}
}

func TestCompactDecisionInputBoundsConversationAndEvents(t *testing.T) {
	turns := make([]ConversationTurn, 10)
	for index := range turns {
		turns[index] = ConversationTurn{Role: "user", Content: strings.Repeat("x", 1200)}
	}
	events := make([]EventDigest, 12)
	for index := range events {
		events[index] = EventDigest{Message: strings.Repeat("e", 700), Payload: map[string]any{"large": strings.Repeat("p", 900)}}
	}
	input := compactDecisionInput(Context{
		Conversation: &ConversationContext{RecentTurns: turns, Summary: strings.Repeat("s", 2400)},
		EventDigest:  events,
		Events:       []agent.RunEvent{{Message: "raw event should be omitted"}},
	})
	if len(input.Conversation.RecentTurns) != 6 || len([]rune(input.Conversation.RecentTurns[0].Content)) > 820 || len([]rune(input.Conversation.Summary)) > 1620 {
		t.Fatalf("conversation was not bounded: %#v", input.Conversation)
	}
	if len(input.EventDigest) != 8 || len([]rune(input.EventDigest[0].Message)) > 420 || len(input.Events) != 0 {
		t.Fatalf("event context was not bounded or deduplicated: digest=%d raw=%d", len(input.EventDigest), len(input.Events))
	}
}

func TestScalarStrengtheningLanguageDoesNotBecomeCollectionPatch(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"revise_checkpoint","confidence":0.95,"next_action":"revise_checkpoint","source_mode":"non_novel",
		"agent_reply":"I will strengthen the pressure.","reason":"scalar_change",
		"revision_intent":"patch_artifact_field",
		"revision_target":{"artifact_type":"story_seed","field_path":"protagonist.pressure","scope":"field"}
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request:            MessageRequest{Message: "增加主角目标的紧迫感"},
		Run:                &agent.Run{RunID: "run_scalar", Status: agent.RunCompleted, SourceMode: agent.SourceModeNonNovel},
		CurrentStepContext: &CurrentStepContext{ArtifactID: "seed_v1", ArtifactType: "story_seed"},
	})
	if decision.RevisionIntent != RevisionIntentPatchArtifactField || decision.RevisionTarget == nil || decision.RevisionTarget.Scope != "field" {
		t.Fatalf("scalar strengthening was promoted to a collection change: %s target=%+v", formatDecisionForDebug(decision), decision.RevisionTarget)
	}
}

func TestExplicitNewCharacterBecomesCollectionPatch(t *testing.T) {
	client := &fakeChatClient{configured: true, response: `{
		"intent":"revise_checkpoint","confidence":0.95,"next_action":"revise_checkpoint","source_mode":"non_novel",
		"agent_reply":"I will add one character.","reason":"list_change",
		"revision_intent":"patch_artifact_entity",
		"revision_target":{"artifact_type":"story_seed","field_path":"main_characters","scope":"entity"}
	}`}
	decision := New(client, "control-model").Decide(context.Background(), Context{
		Request:            MessageRequest{Message: "新增一个人物，作为主角的竞争对手"},
		Run:                &agent.Run{RunID: "run_character", Status: agent.RunCompleted, SourceMode: agent.SourceModeNonNovel},
		CurrentStepContext: &CurrentStepContext{ArtifactID: "seed_v1", ArtifactType: "story_seed"},
	})
	if decision.RevisionIntent != RevisionIntentPatchArtifactCollection || decision.RevisionTarget == nil || decision.RevisionTarget.Scope != "collection" {
		t.Fatalf("explicit list addition was not promoted to collection change: %s target=%+v", formatDecisionForDebug(decision), decision.RevisionTarget)
	}
}
