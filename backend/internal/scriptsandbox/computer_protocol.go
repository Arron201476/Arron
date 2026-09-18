package scriptsandbox

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
)

type ComputerBinding struct {
	SessionID  string `json:"session_id"`
	Generation int    `json:"generation"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
}

type computerReceipt struct {
	SessionID  string `json:"session_id"`
	Generation int    `json:"generation"`
	Sequence   int    `json:"sequence"`
	Status     string `json:"status"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	PNGBase64  string `json:"png_base64,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
}

// Browser receipts are flat typed objects; do not allow duplicate fields to
// produce different identities in independent JSON decoders.
func decodeComputerReceipt(raw []byte) (computerReceipt, error) {
	var result computerReceipt
	if len(raw) > computerResponseFrameBytes {
		return result, errors.New("computer receipt exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return result, errors.New("invalid computer receipt object")
	}
	fields := map[string]any{"session_id": &result.SessionID, "generation": &result.Generation,
		"sequence": &result.Sequence, "status": &result.Status, "width": &result.Width,
		"height": &result.Height, "png_base64": &result.PNGBase64, "error_code": &result.ErrorCode}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] || fields[name] == nil {
			return result, errors.New("unknown or duplicated computer receipt field")
		}
		seen[name] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return result, errors.New("invalid computer receipt value")
		}
		if err := json.Unmarshal(value, fields[name]); err != nil {
			return result, errors.New("invalid computer receipt field type")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return result, errors.New("incomplete computer receipt")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return result, errors.New("trailing computer receipt data")
	}
	if !seen["session_id"] || !seen["generation"] || !seen["sequence"] || !seen["status"] {
		return result, errors.New("computer receipt identity missing")
	}
	return result, nil
}

// The Runtime caller must authorize and audit before using this protocol.
// A successful helper receipt alone does not grant execution permission.
type computerProtocol struct {
	binding  ComputerBinding
	stream   workspacePTYStream
	sequence int
	gate     chan struct{}
	closed   atomic.Bool
}

// Only the Runtime owner supplies this over the private process pipe, never a
// model-generated action. Do not persist or log the startup frame's token.
type computerProxyConfig struct {
	Host  string `json:"host"`
	Port  int    `json:"port"`
	Token string `json:"token"`
}

func (config computerProxyConfig) valid() bool {
	address, err := netip.ParseAddr(config.Host)
	decoded, tokenErr := hex.DecodeString(config.Token)
	return err == nil && address.Zone() == "" && !address.IsUnspecified() && !address.IsMulticast() &&
		config.Port > 0 && config.Port <= 65535 && tokenErr == nil && len(decoded) == 32 && len(config.Token) == 64
}

func startComputerProtocol(ctx context.Context, stream workspacePTYStream, binding ComputerBinding, initialURL string, proxy computerProxyConfig) (_ *computerProtocol, returnedErr error) {
	if stream == nil {
		return nil, errors.New("computer stream is unavailable")
	}
	handedOff := false
	defer func() {
		if !handedOff {
			if err := stream.Close(); err != nil {
				returnedErr = errors.Join(returnedErr, errors.New("computer startup stream cleanup unconfirmed"))
			}
		}
	}()
	parsed, err := url.Parse(initialURL)
	if !proxy.valid() || binding.SessionID == "" || len(binding.SessionID) > 256 || strings.TrimSpace(binding.SessionID) != binding.SessionID ||
		binding.Generation < 1 || binding.Width < 1 || binding.Width > 4096 || binding.Height < 1 || binding.Height > 4096 ||
		err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || len(initialURL) > 8192 {
		return nil, errors.New("invalid computer startup configuration")
	}
	client := &computerProtocol{binding: binding, stream: stream, gate: make(chan struct{}, 1)}
	frame, err := json.Marshal(struct {
		ComputerBinding
		Operation  string              `json:"operation"`
		Sequence   int                 `json:"sequence"`
		InitialURL string              `json:"initial_url"`
		Proxy      computerProxyConfig `json:"proxy"`
	}{binding, "start", 0, initialURL, proxy})
	if err != nil {
		return nil, err
	}
	raw, err := stream.Exchange(ctx, append(frame, '\n'))
	if err == nil {
		var receipt computerReceipt
		receipt, err = decodeComputerReceipt(raw)
		if err == nil && (receipt.SessionID != binding.SessionID || receipt.Generation != binding.Generation || receipt.Sequence != 0 || receipt.Status != "ready" || receipt.Width != binding.Width || receipt.Height != binding.Height || receipt.ErrorCode != "" || receipt.PNGBase64 != "") {
			err = errors.New("computer startup receipt mismatch")
		}
	}
	if err != nil {
		return nil, err
	}
	handedOff = true
	return client, nil
}

func (client *computerProtocol) Execute(ctx context.Context, action json.RawMessage) (computerReceipt, error) {
	select {
	case client.gate <- struct{}{}:
		defer func() { <-client.gate }()
	case <-ctx.Done():
		return computerReceipt{}, ctx.Err()
	}
	if client.closed.Load() {
		return computerReceipt{}, errors.New("computer protocol is closed")
	}
	if err := ctx.Err(); err != nil {
		return computerReceipt{}, err
	}
	if len(action) == 0 || len(action) > computerRequestFrameBytes || !json.Valid(action) {
		return computerReceipt{}, errors.New("invalid computer action JSON")
	}
	var kind struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(action, &kind) != nil || kind.Type == "" {
		return computerReceipt{}, errors.New("computer action must be a typed object")
	}
	frame, err := json.Marshal(struct {
		SessionID  string          `json:"session_id"`
		Generation int             `json:"generation"`
		Sequence   int             `json:"sequence"`
		Action     json.RawMessage `json:"action"`
	}{client.binding.SessionID, client.binding.Generation, client.sequence + 1, action})
	if err != nil || len(frame)+1 > computerRequestFrameBytes {
		return computerReceipt{}, errors.New("computer action frame exceeds limit")
	}
	client.sequence++
	raw, err := client.stream.Exchange(ctx, append(frame, '\n'))
	var receipt computerReceipt
	if err == nil {
		receipt, err = decodeComputerReceipt(raw)
	}
	if err == nil && (client.closed.Load() || receipt.SessionID != client.binding.SessionID || receipt.Generation != client.binding.Generation || receipt.Sequence != client.sequence || receipt.Status != "completed" || receipt.ErrorCode != "" || receipt.Width != 0 || receipt.Height != 0) {
		err = errors.New("computer action receipt mismatch")
	}
	if err == nil && ((kind.Type == "screenshot") != (receipt.PNGBase64 != "")) {
		err = errors.New("computer screenshot receipt mismatch")
	}
	if err != nil {
		if closeErr := client.Close(); closeErr != nil {
			err = errors.Join(err, errors.New("computer action stream cleanup unconfirmed"))
		}
		return computerReceipt{}, err
	}
	return receipt, nil
}

func (client *computerProtocol) Close() error {
	client.closed.Store(true)
	return client.stream.Close()
}
