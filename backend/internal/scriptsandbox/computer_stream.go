package scriptsandbox

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
)

const computerRequestFrameBytes = 256 << 10
const computerResponseFrameBytes = (24 << 20) + (64 << 10)

// computerProcessStream serializes frames independently of the PTY registry.
// Closing this bridge is not proof that its owning OCI container was removed.
type computerProcessStream struct {
	inner  workspacePTYStream
	gate   chan struct{}
	closed atomic.Bool
}

func (execCommandRunner) StartComputerStream(ctx context.Context, name string, args []string) (*computerProcessStream, error) {
	stream, err := startWorkspaceProcessStream(ctx, name, args, computerResponseFrameBytes)
	if err != nil {
		return nil, err
	}
	return &computerProcessStream{inner: stream, gate: make(chan struct{}, 1)}, nil
}

func (stream *computerProcessStream) Exchange(ctx context.Context, frame []byte) ([]byte, error) {
	if len(frame) == 0 || len(frame) > computerRequestFrameBytes || frame[len(frame)-1] != '\n' || bytes.ContainsAny(frame[:len(frame)-1], "\r\n") {
		return nil, errors.New("invalid computer request frame")
	}
	select {
	case stream.gate <- struct{}{}:
		defer func() { <-stream.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if stream.closed.Load() {
		return nil, errors.New("computer stream is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := stream.inner.Exchange(ctx, frame)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if stream.closed.Load() || len(result) == 0 || len(result) > computerResponseFrameBytes || result[len(result)-1] != '\n' || bytes.ContainsAny(result[:len(result)-1], "\r\n") {
		_ = stream.Close()
		return nil, errors.New("unconfirmed computer response frame")
	}
	return result, nil
}

func (stream *computerProcessStream) Close() error {
	stream.closed.Store(true)
	return stream.inner.Close()
}
