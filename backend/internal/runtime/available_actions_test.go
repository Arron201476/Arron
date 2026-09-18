package runtime

import "testing"

func TestProjectRunAvailableActions(t *testing.T) {
	stepRunID := "step_1"
	base := Run{
		RunID:                         "run_1",
		CurrentStepRunID:              &stepRunID,
		CurrentInputSnapshotVersionID: "risv_1",
		InputSnapshotStatus:           "sealed",
	}
	step := StepRun{StepRunID: stepRunID, RunID: base.RunID}

	tests := []struct {
		name        string
		status      string
		stepStatus  string
		wantResume  bool
		wantRetry   bool
		wantPartial bool
		want        []string
	}{
		{name: "running", status: "running", stepStatus: "running", want: []string{"pause_run", "cancel_run"}},
		{name: "waiting approval", status: "waiting_approval", stepStatus: "waiting_approval", want: []string{"cancel_run"}},
		{name: "pausing", status: "pausing", stepStatus: "running", wantResume: true, want: []string{"resume_run", "cancel_run"}},
		{name: "paused", status: "paused", stepStatus: "paused", wantResume: true, want: []string{"resume_run", "cancel_run"}},
		{name: "paused without resume guard", status: "paused", stepStatus: "paused", want: []string{"cancel_run"}},
		{name: "failed", status: "failed", stepStatus: "failed", wantRetry: true, want: []string{"retry_failed_step", "cancel_run"}},
		{name: "failed partial", status: "failed", stepStatus: "failed", wantRetry: true, wantPartial: true, want: []string{"retry_failed_step", "continue_with_partial_results", "cancel_run"}},
		{name: "failed without retry guard", status: "failed", stepStatus: "failed", want: []string{"cancel_run"}},
		{name: "completed", status: "completed", stepStatus: "completed", want: []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := base
			run.Status = test.status
			currentStep := step
			currentStep.Status = test.stepStatus
			actions := projectRunAvailableActions(
				run,
				[]StepRun{currentStep},
				test.wantResume,
				test.wantRetry,
				test.wantPartial,
			)
			if len(actions) != len(test.want) {
				t.Fatalf("actions = %+v, want IDs %v", actions, test.want)
			}
			for index, actionID := range test.want {
				if actions[index].ActionID != actionID || !actions[index].Enabled {
					t.Fatalf("actions = %+v, want IDs %v", actions, test.want)
				}
			}
		})
	}
}
