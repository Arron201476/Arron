package runtime

import "testing"

func TestTextualPayloadCharacterCount(t *testing.T) {
	payload := map[string]any{
		"title":   "青铜钥匙",
		"content": "林遥走进晨光。",
		"nested":  []any{map[string]any{"hook": "真相不会消失"}, 3.0},
	}
	want := len([]rune("青铜钥匙林遥走进晨光。真相不会消失"))
	if got := textualPayloadCharacterCount(payload); got != want {
		t.Fatalf("textualPayloadCharacterCount() = %d, want %d", got, want)
	}
}
