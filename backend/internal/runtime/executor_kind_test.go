package runtime

import "testing"

func TestWorkerExecutableStepKind(t *testing.T) {
	tests := map[string]bool{
		"model":           true,
		"tool":            true,
		"aggregate":       true,
		"shared_workflow": true,
		"system":          false,
		"review":          false,
	}
	for kind, expected := range tests {
		if actual := workerExecutableStepKind(kind); actual != expected {
			t.Fatalf("workerExecutableStepKind(%q) = %t, want %t", kind, actual, expected)
		}
	}
}
