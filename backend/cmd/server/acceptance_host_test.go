package main

import (
	"os"
	"testing"
)

func TestAcceptanceHost(t *testing.T) {
	if os.Getenv("CONTENT_AGENT_ACCEPTANCE_HOST") != "1" {
		t.Skip("acceptance host is disabled")
	}
	main()
}
