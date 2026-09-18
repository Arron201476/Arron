package runtime

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateContinuationScriptLength(t *testing.T) {
	config := json.RawMessage(`{"config_ref":"continuation","payload":{"target_length_chars":15000}}`)

	err := validateContinuationScriptLength(config, mustJSON(t, map[string]any{
		"script_text": strings.Repeat("字 ", 13499),
	}))
	assertDomainCode(t, err, "OUTPUT_LENGTH_INSUFFICIENT")

	if err := validateContinuationScriptLength(config, mustJSON(t, map[string]any{
		"script_text": strings.Repeat("字 \n", 13500),
	})); err != nil {
		t.Fatalf("validateContinuationScriptLength() error = %v", err)
	}
}
