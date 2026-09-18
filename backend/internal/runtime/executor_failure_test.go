package runtime

import "testing"

func TestAutomaticallyRetryableExecutionErrors(t *testing.T) {
	retryable := []string{
		"MODEL_TIMEOUT",
		"MODEL_RATE_LIMIT",
		"PROVIDER_TEMPORARY_FAILURE",
		"SUBTITLE_OCR_TIMEOUT",
		"SUBTITLE_OCR_QUERY_FAILED",
		"SUBTITLE_OCR_UNAVAILABLE",
	}
	for _, code := range retryable {
		if !isAutomaticallyRetryableExecutionError(code) {
			t.Fatalf("%s must be automatically retryable", code)
		}
	}

	deterministic := []string{
		"PROVIDER_RESPONSE_INVALID",
		"OUTPUT_REPAIR_FAILED",
		"MEDIA_PROCESSING_FAILED",
		"MEDIA_PROBE_FAILED",
		"SUBTITLE_OCR_RESULT_INVALID",
		"PROVIDER_AUTH_FAILED",
	}
	for _, code := range deterministic {
		if isAutomaticallyRetryableExecutionError(code) {
			t.Fatalf("%s must require an explicit user retry", code)
		}
	}
}
