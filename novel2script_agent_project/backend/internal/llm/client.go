package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Role string

const (
	RoleSystem Role = "system"
	RoleUser   Role = "user"
)

type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model        string            `json:"model"`
	Messages     []Message         `json:"messages"`
	Temperature  *float64          `json:"temperature,omitempty"`
	TraceContext map[string]string `json:"-"`
}

type ChatResponse struct {
	Content string `json:"content"`
	Model   string `json:"model,omitempty"`
}

type ChatClient interface {
	Complete(ctx context.Context, req ChatRequest) (ChatResponse, error)
	Configured() bool
}

type HTTPError struct {
	StatusCode int
	Body       string
	Headers    map[string]string
}

func (e *HTTPError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("llm request failed: status=%d headers=%v body=%s", e.StatusCode, e.Headers, e.Body)
}

type Config struct {
	BaseURL              string
	APIKey               string
	ControlModel         string
	ControlFallbackModel string
	GenerationModel      string
	RequestTimeout       time.Duration
}

func ConfigFromEnv() Config {
	loadEnvFile(".env")
	loadEnvFile(filepath.Join("..", ".env"))
	return Config{
		BaseURL:              strings.TrimSpace(os.Getenv("N2S_LLM_BASE_URL")),
		APIKey:               strings.TrimSpace(os.Getenv("N2S_LLM_API_KEY")),
		ControlModel:         strings.TrimSpace(os.Getenv("N2S_CONTROL_MODEL")),
		ControlFallbackModel: strings.TrimSpace(os.Getenv("N2S_CONTROL_FALLBACK_MODEL")),
		GenerationModel:      strings.TrimSpace(os.Getenv("N2S_GENERATION_MODEL")),
		RequestTimeout:       requestTimeoutFromEnv(),
	}
}

func requestTimeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("N2S_LLM_TIMEOUT_SECONDS"))
	if raw == "" {
		return 180 * time.Second
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 10 {
		return 180 * time.Second
	}
	return time.Duration(seconds) * time.Second
}

func loadEnvFile(path string) {
	content, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		line = strings.TrimPrefix(line, "\ufeff")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		_ = os.Setenv(key, value)
	}
}

func (c Config) Configured() bool {
	return c.BaseURL != "" && c.APIKey != "" && c.ControlModel != ""
}

type OpenAICompatibleClient struct {
	config Config
	http   *http.Client
}

func NewOpenAICompatibleClient(config Config) *OpenAICompatibleClient {
	timeout := config.RequestTimeout
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &OpenAICompatibleClient{
		config: config,
		http: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
	}
}

func (c *OpenAICompatibleClient) Configured() bool {
	return c != nil && c.config.Configured()
}

func (c *OpenAICompatibleClient) Complete(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		response, err := c.completeAttempt(ctx, req)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if !retryableLLMError(err) || attempt == 2 {
			return ChatResponse{}, err
		}
		if err := sleepContext(ctx, time.Duration(400*(1<<attempt))*time.Millisecond); err != nil {
			return ChatResponse{}, err
		}
	}
	return ChatResponse{}, lastErr
}

func (c *OpenAICompatibleClient) completeAttempt(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if !c.Configured() {
		return ChatResponse{}, errors.New("llm client is not configured")
	}
	if strings.TrimSpace(req.Model) == "" {
		req.Model = c.config.ControlModel
	}
	if modelDisallowsTemperature(req.Model) {
		req.Temperature = nil
	}
	startedAt := time.Now().UTC()

	body, err := json.Marshal(req)
	if err != nil {
		return ChatResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.BaseURL, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		writeTrace(req, ChatResponse{}, err, startedAt, time.Since(startedAt), nil)
		return ChatResponse{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		writeTrace(req, ChatResponse{}, err, startedAt, time.Since(startedAt), relevantResponseHeaders(resp.Header))
		return ChatResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		httpErr := &HTTPError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(respBody)),
			Headers:    relevantResponseHeaders(resp.Header),
		}
		writeTrace(req, ChatResponse{}, httpErr, startedAt, time.Since(startedAt), relevantResponseHeaders(resp.Header))
		return ChatResponse{}, httpErr
	}

	var payload struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &payload); err != nil {
		writeTrace(req, ChatResponse{}, err, startedAt, time.Since(startedAt), relevantResponseHeaders(resp.Header))
		return ChatResponse{}, err
	}
	if len(payload.Choices) == 0 {
		err := errors.New("llm response has no choices")
		writeTrace(req, ChatResponse{}, err, startedAt, time.Since(startedAt), relevantResponseHeaders(resp.Header))
		return ChatResponse{}, err
	}

	out := ChatResponse{
		Content: strings.TrimSpace(payload.Choices[0].Message.Content),
		Model:   payload.Model,
	}
	writeTrace(req, out, nil, startedAt, time.Since(startedAt), relevantResponseHeaders(resp.Header))
	return out, nil
}

func retryableLLMError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= 500
	}
	return true
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *OpenAICompatibleClient) ControlModel() string {
	if c == nil {
		return ""
	}
	return c.config.ControlModel
}

func (c *OpenAICompatibleClient) GenerationModel() string {
	if c == nil {
		return ""
	}
	return c.config.GenerationModel
}

func writeTrace(req ChatRequest, resp ChatResponse, err error, startedAt time.Time, duration time.Duration, headers map[string]string) {
	level := traceLevel()
	if level == "off" {
		return
	}
	path := tracePath()
	if path == "" {
		return
	}
	requestChars := 0
	for _, message := range req.Messages {
		requestChars += len([]rune(message.Content))
	}
	entry := map[string]any{
		"timestamp":   startedAt.Format(time.RFC3339Nano),
		"duration_ms": duration.Milliseconds(),
		"trace_level": level,
		"model":       req.Model,
		"request": map[string]any{
			"message_count": len(req.Messages),
			"content_chars": requestChars,
		},
		"response": map[string]any{
			"model":         resp.Model,
			"content_chars": len([]rune(resp.Content)),
		},
		"headers": headers,
	}
	if len(req.TraceContext) > 0 {
		entry["trace_context"] = req.TraceContext
	}
	if err != nil {
		entry["error"] = traceError(err)
	}
	if level == "full" {
		entry["request_full"] = req
		entry["response_full"] = resp
	}
	line, marshalErr := json.Marshal(entry)
	if marshalErr != nil {
		log.Printf("failed to marshal llm trace: %v", marshalErr)
		return
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr != nil {
		log.Printf("failed to create llm trace dir: %v", mkdirErr)
		return
	}
	if rotateErr := rotateTrace(path, traceMaxBytes()); rotateErr != nil {
		log.Printf("failed to rotate llm trace: %v", rotateErr)
	}
	file, openErr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if openErr != nil {
		log.Printf("failed to open llm trace: %v", openErr)
		return
	}
	defer file.Close()
	_ = file.Chmod(0o600)
	if _, writeErr := file.Write(append(line, '\n')); writeErr != nil {
		log.Printf("failed to write llm trace: %v", writeErr)
	}
}

func traceLevel() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("N2S_LLM_TRACE_LEVEL"))) {
	case "off", "none", "disabled":
		return "off"
	case "full", "debug":
		return "full"
	default:
		return "metadata"
	}
}

func traceError(err error) map[string]any {
	result := map[string]any{"type": fmt.Sprintf("%T", err), "message": "llm request failed"}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		result["status_code"] = httpErr.StatusCode
	}
	return result
}

func traceMaxBytes() int64 {
	const defaultMax = int64(10 << 20)
	raw := strings.TrimSpace(os.Getenv("N2S_LLM_TRACE_MAX_BYTES"))
	if raw == "" {
		return defaultMax
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 64<<10 {
		return defaultMax
	}
	return value
}

func rotateTrace(path string, maxBytes int64) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Size() < maxBytes {
		return err
	}
	rotated := path + ".1"
	_ = os.Remove(rotated)
	return os.Rename(path, rotated)
}

func tracePath() string {
	if dir := strings.TrimSpace(os.Getenv("N2S_TRACE_DIR")); dir != "" {
		return filepath.Join(dir, "llm_trace.jsonl")
	}
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		if filepath.Base(exeDir) == ".tmp" && filepath.Base(filepath.Dir(exeDir)) == "backend" {
			return filepath.Join(filepath.Dir(filepath.Dir(exeDir)), "runs", "llm_trace.jsonl")
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return filepath.Join("runs", "llm_trace.jsonl")
	}
	if filepath.Base(cwd) == "backend" {
		return filepath.Join(cwd, "..", "runs", "llm_trace.jsonl")
	}
	return filepath.Join(cwd, "runs", "llm_trace.jsonl")
}

func modelDisallowsTemperature(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "claude") || strings.Contains(model, "opus")
}

func Temperature(value float64) *float64 {
	return &value
}

func relevantResponseHeaders(headers http.Header) map[string]string {
	names := []string{
		"retry-after",
		"x-ratelimit-limit",
		"x-ratelimit-remaining",
		"x-ratelimit-reset",
		"x-ratelimit-reset-requests",
		"x-ratelimit-reset-tokens",
		"x-request-id",
		"cf-ray",
	}
	result := map[string]string{}
	for _, name := range names {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			result[name] = value
		}
	}
	return result
}
