package scriptsandbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestComputerReceiptRejectsAmbiguousIdentity(t *testing.T) {
	for _, raw := range []string{
		`{"session_id":"s","session_id":"s","generation":1,"sequence":1,"status":"completed"}`,
		`{"session_id":"s","generation":true,"sequence":1,"status":"completed"}`,
		`{"session_id":"s","generation":1,"sequence":null,"status":"completed"}`,
		`{"session_id":"s","generation":1,"status":"completed"}`,
		`{"session_id":"s","generation":1,"sequence":1,"status":"completed","authorized":true}`,
		`{"session_id":"s","generation":1,"sequence":1,"status":"completed"} {}`,
	} {
		if _, err := decodeComputerReceipt([]byte(raw)); err == nil {
			t.Fatal("ambiguous receipt accepted")
		}
	}
}

func TestComputerProtocolBindsStartupAndEveryAction(t *testing.T) {
	fixture := &computerFrameFixture{response: []byte(`{"session_id":"s","generation":1,"sequence":0,"status":"ready","width":100,"height":100}`)}
	client, err := startComputerProtocol(context.Background(), fixture, ComputerBinding{"s", 1, 100, 100}, "https://example.test/", computerProxyConfig{"172.30.0.1", 3128, strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	fixture.response = []byte(`{"session_id":"s","generation":1,"sequence":1,"status":"completed"}`)
	if _, err := client.Execute(context.Background(), json.RawMessage(`{"type":"wait"}`)); err != nil {
		t.Fatal(err)
	}
	fixture.response = []byte(`{"session_id":"s","generation":1,"sequence":1,"status":"completed"}`)
	if _, err := client.Execute(context.Background(), json.RawMessage(`{"type":"wait"}`)); err == nil {
		t.Fatal("replayed response accepted")
	}
	if !fixture.closed {
		t.Fatal("unconfirmed execution did not close")
	}
	before := fixture.calls
	if _, err := client.Execute(context.Background(), json.RawMessage(`{"type":"wait"}`)); err == nil || fixture.calls != before {
		t.Fatal("closed protocol replayed action")
	}
}

func TestComputerStartupFailureClosesTransferredStream(t *testing.T) {
	for _, reply := range []string{
		`{"status":"failed","error_code":"BROWSER_SESSION_FAILED"}`,
		`{"session_id":"foreign","generation":1,"sequence":0,"status":"ready","width":100,"height":100}`,
		`{"session_id":"s","generation":1,"sequence":0,"status":"ready","width":101,"height":100}`,
	} {
		fixture := &computerFrameFixture{response: []byte(reply)}
		if _, err := startComputerProtocol(context.Background(), fixture, ComputerBinding{"s", 1, 100, 100}, "https://example.test/", computerProxyConfig{"172.30.0.1", 3128, strings.Repeat("a", 64)}); err == nil || !fixture.closed {
			t.Fatal("startup failure retained stream")
		}
	}
}

func TestComputerStartupRejectsInvalidProxyBeforeSendingFrame(t *testing.T) {
	for _, config := range []computerProxyConfig{
		{},
		{"proxy.test", 3128, strings.Repeat("a", 64)},
		{"0.0.0.0", 3128, strings.Repeat("a", 64)},
		{"224.0.0.1", 3128, strings.Repeat("a", 64)},
		{"fe80::1%eth0", 3128, strings.Repeat("a", 64)},
		{"172.30.0.1", 0, strings.Repeat("a", 64)},
		{"172.30.0.1", 65536, strings.Repeat("a", 64)},
		{"172.30.0.1", 3128, strings.Repeat("z", 64)},
		{"172.30.0.1", 3128, "short"},
	} {
		fixture := &computerFrameFixture{}
		_, err := startComputerProtocol(context.Background(), fixture, ComputerBinding{"s", 1, 100, 100}, "https://example.test/", config)
		if err == nil || fixture.calls != 0 || !fixture.closed {
			t.Fatal("invalid proxy launched browser or retained stream")
		}
	}
}
