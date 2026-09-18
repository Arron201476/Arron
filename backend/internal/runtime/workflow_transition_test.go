package runtime

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"content-agent/backend/internal/capability"
)

func TestWorkflowTransitionMatchesFactsInsteadOfPosition(t *testing.T) {
	for _, test := range []struct {
		name  string
		next  []capability.TransitionRef
		facts []string
		want  string
		code  string
	}{
		{"approved", []capability.TransitionRef{{When: "failure", To: "failed"}, {When: "approved", To: "accepted"}}, []string{"completed", "approved"}, "accepted", ""},
		{"sufficient", []capability.TransitionRef{{When: "expansion_strategy_confirmed", To: "expand"}, {When: "volume_fit_sufficient", To: "write"}}, []string{"completed", "volume_fit_sufficient"}, "write", ""},
		{"expansion", []capability.TransitionRef{{When: "volume_fit_sufficient", To: "write"}, {When: "expansion_strategy_confirmed", To: "expand"}}, []string{"completed", "approved", "expansion_strategy_confirmed"}, "expand", ""},
		{"adaptation", []capability.TransitionRef{{When: "failure", To: "failed"}, {When: "adaptation_selection_and_creation_config_confirmed", To: "brief"}}, []string{"completed", "approved", "adaptation_selection_and_creation_config_confirmed"}, "brief", ""},
		{"same-target", []capability.TransitionRef{{When: "completed", To: "next"}, {When: "approved", To: "next"}}, []string{"completed", "approved"}, "next", ""},
		{"no-match", []capability.TransitionRef{{When: "failure", To: "failed"}}, []string{"completed", "approved"}, "", "WORKFLOW_TRANSITION_UNMATCHED"},
		{"unknown-condition", []capability.TransitionRef{{When: "model says done", To: "next"}}, []string{"completed"}, "", "WORKFLOW_TRANSITION_UNMATCHED"},
		{"ambiguous", []capability.TransitionRef{{When: "completed", To: "automatic"}, {When: "approved", To: "manual"}}, []string{"completed", "approved"}, "", "WORKFLOW_TRANSITION_AMBIGUOUS"},
		{"empty-target", []capability.TransitionRef{{When: "completed"}}, []string{"completed"}, "", "WORKFLOW_TRANSITION_AMBIGUOUS"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, reverse := range []bool{false, true} {
				next := slices.Clone(test.next)
				if reverse {
					slices.Reverse(next)
				}
				got, err := selectWorkflowTransition(capability.CompiledStep{ID: "current", Next: next}, test.facts...)
				if test.code != "" {
					assertDomainCode(t, err, test.code)
				} else if err != nil || got != test.want {
					t.Fatalf("reverse=%t got=%q want=%q err=%v", reverse, got, test.want, err)
				}
			}
		})
	}
}

func TestOriginalWorkflowTransitionsAcceptTheirRuntimeFacts(t *testing.T) {
	registry := loadTestRegistry(t)
	for _, id := range []string{"novel_to_script", "non_novel_to_script", "video_reference_creation", "script_continuation"} {
		entry, ok := registry.Get(id)
		if !ok || entry.Definition == nil {
			t.Fatalf("missing original workflow %s", id)
		}
		for _, step := range entry.Definition.Steps {
			if len(step.Next) == 0 {
				continue
			}
			facts := []string{"completed"}
			if step.Approval.Required {
				facts = append(facts, "approved")
			}
			if step.Batch != nil {
				facts = append(facts, "all_batch_items_succeeded")
			}
			variants := [][]string{facts}
			if step.ExecutorRef == "runtime.review_volume_fit" {
				variants = [][]string{{"completed", "volume_fit_sufficient"}, {"completed", "approved", "expansion_strategy_confirmed"}}
			} else if slices.Contains(step.Approval.AllowedActions, "select_adaptation_strategy") {
				variants = [][]string{{"completed", "approved", "adaptation_selection_and_creation_config_confirmed"}}
			}
			for _, facts := range variants {
				t.Run(id+"/"+step.ID+"/"+strings.Join(facts, "+"), func(t *testing.T) {
					got, err := selectWorkflowTransition(step, facts...)
					if err != nil || got != step.Next[0].To {
						t.Fatalf("original transition facts=%v: got=%q want=%q err=%v", facts, got, step.Next[0].To, err)
					}
				})
			}
		}
	}
}

func TestApprovalUsesFactBranchAndRollsBackUnmatchedTransitions(t *testing.T) {
	for _, scenario := range []string{"approved", "unmatched", "ambiguous"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			registry := loadTestRegistry(t)
			entry, _ := registry.Get("novel_to_script")
			for index := range entry.Definition.Steps {
				step := &entry.Definition.Steps[index]
				if step.ID != "build_story_bible" {
					continue
				}
				step.Next = []capability.TransitionRef{{When: "failure", To: "ingest_source"}}
				if scenario == "approved" {
					step.Next = append(step.Next, capability.TransitionRef{When: "approved", To: "review_volume_fit"})
				} else if scenario == "ambiguous" {
					step.Next = []capability.TransitionRef{{When: "completed", To: "ingest_source"}, {When: "approved", To: "review_volume_fit"}}
				}
			}
			store, err := Open(filepath.Join(t.TempDir(), "transition.db"), registry)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			_, initial, _, _ := prepareStoryBibleForImpact(t, store, "conditional approval")
			approval, err := store.pendingApprovalForRun(ctx, initial.Run.RunID)
			if err != nil || approval == nil {
				t.Fatalf("pending approval: %v %v", approval, err)
			}
			command := ResolveApprovalCommand{CommandMeta: CommandMeta{Scope: approval.ProjectID, CommandType: "resolve_approval", IdempotencyKey: "approve-branch", RequestHash: "approve-branch-v1"}, ApprovalRequestID: approval.ApprovalRequestID, Action: "approve", ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash}
			before := episodeApprovalState(t, store, *approval)
			result, err := store.ResolveApproval(ctx, command)
			if scenario != "approved" {
				code := "WORKFLOW_TRANSITION_UNMATCHED"
				if scenario == "ambiguous" {
					code = "WORKFLOW_TRANSITION_AMBIGUOUS"
				}
				assertDomainCode(t, err, code)
				if after := episodeApprovalState(t, store, *approval); after != before {
					t.Fatal("failed transition committed approval, outputs or successor")
				}
				return
			}
			if err != nil || result.Run.Status != "paused" || currentStepID(result) != "review_volume_fit" {
				t.Fatalf("approval selected wrong successor: %+v %v", result, err)
			}
			before = episodeApprovalState(t, store, *approval)
			retry, err := store.ResolveApproval(ctx, command)
			if err != nil || retry.Run.CurrentStepRunID == nil || *retry.Run.CurrentStepRunID != *result.Run.CurrentStepRunID || episodeApprovalState(t, store, *approval) != before {
				t.Fatalf("original approval receipt changed state: %+v %v", retry, err)
			}
		})
	}
}

func TestVolumeFitRoutesSufficientAndConfirmedExpansionSeparately(t *testing.T) {
	for _, sufficient := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed-expansion", true: "sufficient"}[sufficient], func(t *testing.T) {
			ctx := context.Background()
			registry := loadTestRegistry(t)
			entry, _ := registry.Get("novel_to_script")
			for index := range entry.Definition.Steps {
				if step := &entry.Definition.Steps[index]; step.ID == "review_volume_fit" {
					step.Next = []capability.TransitionRef{{When: "volume_fit_sufficient", To: "build_story_bible"}, {When: "expansion_strategy_confirmed", To: "split_episodes"}}
				}
			}
			store, err := Open(filepath.Join(t.TempDir(), "volume-branch.db"), registry)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			content := "Short source."
			if sufficient {
				content = strings.Repeat("Source text. ", 4000)
			}
			_, initial, _, _ := prepareStoryBibleForImpactWithContent(t, store, "volume branches", content)
			approval, err := store.pendingApprovalForRun(ctx, initial.Run.RunID)
			if err != nil || approval == nil {
				t.Fatalf("pending story approval: %v %v", approval, err)
			}
			if _, err := store.ResolveApproval(ctx, ResolveApprovalCommand{ApprovalRequestID: approval.ApprovalRequestID, Action: "approve", ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash}); err != nil {
				t.Fatal(err)
			}
			result, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: initial.Run.RunID})
			if err != nil {
				t.Fatal(err)
			}
			if sufficient {
				if currentStepID(result) != "build_story_bible" || result.Run.Status != "running" || result.CurrentApproval != nil {
					t.Fatalf("sufficient branch = %+v", result)
				}
				return
			}
			approval = result.CurrentApproval
			if approval == nil || approval.SubjectKind != "transition" {
				t.Fatalf("missing expansion confirmation: %+v", result)
			}
			result, err = store.ResolveApproval(ctx, ResolveApprovalCommand{ApprovalRequestID: approval.ApprovalRequestID, Action: "approve", ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash})
			if err != nil || result.Run.Status != "paused" || currentStepID(result) != "split_episodes" {
				t.Fatalf("confirmed expansion selected first branch: %+v %v", result, err)
			}
		})
	}
}

func TestBatchTransitionRequiresActualSuccessfulTaskSet(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "batch-branch.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, approval, _, _ := prepareEpisodeCheckpoint(t, store)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	step := capability.CompiledStep{Batch: &capability.BatchPolicy{}, Next: []capability.TransitionRef{{When: "all_batch_items_succeeded", To: "next"}}}
	_, err = completedWorkflowTransitionTx(ctx, tx, approval.StepRunID, step)
	assertDomainCode(t, err, "WORKFLOW_TRANSITION_UNMATCHED")
	for _, status := range []string{"pending", "failed", "cancelled", "succeeded"} {
		if _, err := tx.ExecContext(ctx, `UPDATE task_items SET status=? WHERE step_run_id=?`, status, approval.StepRunID); err != nil {
			t.Fatal(err)
		}
		next, err := completedWorkflowTransitionTx(ctx, tx, approval.StepRunID, step)
		if status == "succeeded" {
			if err != nil || next != "next" {
				t.Fatalf("successful batch: next=%q err=%v", next, err)
			}
		} else {
			assertDomainCode(t, err, "WORKFLOW_TRANSITION_UNMATCHED")
		}
	}
	_, err = completedWorkflowTransitionTx(ctx, tx, "missing-step", step)
	assertDomainCode(t, err, "WORKFLOW_TRANSITION_UNMATCHED")
}
