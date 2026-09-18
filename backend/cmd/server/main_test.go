package main

import "testing"

func TestTypedEnvironmentRejectsMalformedValues(t *testing.T) {
	t.Setenv("CONTENT_AGENT_TEST_BOOLEAN", "sometimes")
	if _, err := booleanEnvironment("CONTENT_AGENT_TEST_BOOLEAN", false); err == nil {
		t.Fatal("malformed boolean environment was accepted")
	}
	t.Setenv("CONTENT_AGENT_TEST_INTEGER", "many")
	if _, err := integerEnvironment("CONTENT_AGENT_TEST_INTEGER", 1); err == nil {
		t.Fatal("malformed integer environment was accepted")
	}
}

func TestTypedEnvironmentUsesFallbackOnlyWhenUnset(t *testing.T) {
	t.Setenv("CONTENT_AGENT_TEST_BOOLEAN", "")
	booleanValue, err := booleanEnvironment("CONTENT_AGENT_TEST_BOOLEAN", true)
	if err != nil || !booleanValue {
		t.Fatalf("boolean fallback = %t, error = %v", booleanValue, err)
	}
	t.Setenv("CONTENT_AGENT_TEST_INTEGER", "")
	integerValue, err := integerEnvironment("CONTENT_AGENT_TEST_INTEGER", 3)
	if err != nil || integerValue != 3 {
		t.Fatalf("integer fallback = %d, error = %v", integerValue, err)
	}
}

func TestSplitEnvironmentListTrimsAndOmitsEmptyValues(t *testing.T) {
	t.Setenv("CONTENT_AGENT_TEST_LIST", " workspace_a,workspace_b, ,workspace_c ")
	values := splitEnvironmentList("CONTENT_AGENT_TEST_LIST")
	if len(values) != 3 || values[0] != "workspace_a" || values[2] != "workspace_c" {
		t.Fatalf("split environment = %#v", values)
	}
	t.Setenv("CONTENT_AGENT_TEST_LIST", "")
	if values := splitEnvironmentList("CONTENT_AGENT_TEST_LIST"); values != nil {
		t.Fatalf("empty list = %#v", values)
	}
}
