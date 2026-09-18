package scriptsandbox

import (
	"bufio"
	"context"
	"errors"
	"strings"
	"testing"
)

type computerFrameFixture struct {
	response []byte
	err      error
	calls    int
	closed   bool
}

func (f *computerFrameFixture) Exchange(context.Context, []byte) ([]byte, error) {
	f.calls++
	return f.response, f.err
}
func (f *computerFrameFixture) Close() error { f.closed = true; return nil }

func TestComputerFramesDoNotRaisePTYLimit(t *testing.T) {
	frame := strings.Repeat("x", workspacePTYFrameBytes) + "\n"
	if _, err := readWorkspacePTYFrame(bufio.NewReader(strings.NewReader(frame))); err == nil {
		t.Fatal("PTY limit was expanded")
	}
	if got, err := readWorkspaceProcessFrame(bufio.NewReader(strings.NewReader(frame)), computerResponseFrameBytes); err != nil || len(got) != len(frame) {
		t.Fatalf("computer frame rejected: %d %v", len(got), err)
	}
}

func TestComputerStreamClosesOnUnconfirmedReplyWithoutReplay(t *testing.T) {
	for _, fixture := range []*computerFrameFixture{
		{response: []byte("truncated")}, {response: []byte("{}\n{}\n")}, {err: errors.New("transport failed")},
	} {
		stream := &computerProcessStream{inner: fixture, gate: make(chan struct{}, 1)}
		if _, err := stream.Exchange(context.Background(), []byte("{}\n")); err == nil {
			t.Fatal("unconfirmed reply accepted")
		}
		if _, err := stream.Exchange(context.Background(), []byte("{}\n")); err == nil || fixture.calls != 1 || !fixture.closed {
			t.Fatal("closed stream replayed an action")
		}
	}
}

func TestComputerStreamRejectsMultipleOrUnboundedRequestFrames(t *testing.T) {
	fixture := &computerFrameFixture{}
	stream := &computerProcessStream{inner: fixture, gate: make(chan struct{}, 1)}
	for _, frame := range []string{"", "{}", "{}\n{}\n", "{}\r\n", strings.Repeat("x", computerRequestFrameBytes) + "\n"} {
		if _, err := stream.Exchange(context.Background(), []byte(frame)); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if fixture.calls != 0 {
		t.Fatal("invalid request reached process")
	}
}
