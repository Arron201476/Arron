package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	businessruntime "content-agent/backend/internal/runtime"
)

func TestWorkflowTransitionErrorsRemainVisibleConflicts(t *testing.T) {
	for _, code := range []string{"WORKFLOW_TRANSITION_UNMATCHED", "WORKFLOW_TRANSITION_AMBIGUOUS",
		"WORKFLOW_FAILURE_TRANSITION_CHANGED", "WORKFLOW_FAILURE_NOT_SETTLED", "WORKFLOW_FAILURE_REQUIRES_REVIEW"} {
		t.Run(code, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeRuntimeError(response, &businessruntime.DomainError{Code: code, Message: "Workflow could not select a successor."})
			var payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Error.Code != code {
				t.Fatalf("transition error: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
