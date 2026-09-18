package runtime

import "testing"

func TestContextInputCharacterLimitUsesModelCapacity(t *testing.T) {
	tests := []struct {
		modelID string
		want    int
	}{
		{modelID: "gemini-3p5-flash-routerhub", want: maxGemini3InputCharacters},
		{modelID: "GEMINI-3-flash", want: maxGemini3InputCharacters},
		{modelID: "claude-opus-4p8-apipro", want: maxContextInputCharacters},
		{modelID: "", want: maxContextInputCharacters},
	}
	for _, test := range tests {
		if got := contextInputCharacterLimit(test.modelID); got != test.want {
			t.Fatalf("contextInputCharacterLimit(%q) = %d, want %d", test.modelID, got, test.want)
		}
	}
}
