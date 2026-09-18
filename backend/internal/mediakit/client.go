package mediakit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultEndpoint     = "https://mediakit.cn-beijing.volces.com"
	defaultPollInterval = 2 * time.Second
	maxResponseBytes    = 8 << 20
)

type Config struct {
	Endpoint     string
	APIKey       string
	PollInterval time.Duration
}

func ConfigFromEnv() (Config, error) {
	config := Config{
		Endpoint: strings.TrimSpace(os.Getenv("CONTENT_AGENT_MEDIAKIT_ENDPOINT")),
		APIKey:   strings.TrimSpace(os.Getenv("CONTENT_AGENT_MEDIAKIT_API_KEY")),
	}
	if config.Endpoint == "" {
		config.Endpoint = defaultEndpoint
	}
	if raw := strings.TrimSpace(os.Getenv("CONTENT_AGENT_MEDIAKIT_POLL_INTERVAL")); raw != "" {
		interval, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, errors.New("mediakit poll interval is invalid")
		}
		config.PollInterval = interval
	}
	return config, config.Validate()
}

func AvailableFromEnv() bool {
	_, err := ConfigFromEnv()
	return err == nil
}

func (c Config) Validate() error {
	parsed, err := url.Parse(c.Endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || strings.TrimSpace(c.APIKey) == "" {
		return errors.New("mediakit endpoint and api key are required")
	}
	if c.PollInterval != 0 && (c.PollInterval < 500*time.Millisecond || c.PollInterval > 30*time.Second) {
		return errors.New("mediakit poll interval must be between 500ms and 30s")
	}
	return nil
}

type Client struct {
	config Config
	http   *http.Client
}

func New(config Config, httpClient *http.Client) (*Client, error) {
	if config.PollInterval == 0 {
		config.PollInterval = defaultPollInterval
	}
	config.Endpoint = strings.TrimRight(config.Endpoint, "/")
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 90 * time.Second}
	}
	return &Client{config: config, http: httpClient}, nil
}

type Input struct {
	MIMEType    string
	Data        []byte
	ClientToken string
}

type Subtitle struct {
	Text      string
	StartTime float64
	EndTime   float64
}

type Result struct {
	TaskID    string
	RequestID string
	Duration  float64
	Subtitles []Subtitle
}

func (c *Client) Extract(ctx context.Context, input Input) (Result, error) {
	if c == nil || len(input.Data) == 0 || (input.MIMEType != "video/mp4" && input.MIMEType != "video/quicktime") {
		return Result{}, newError("SUBTITLE_OCR_INPUT_INVALID", errors.New("mediakit video input is invalid"))
	}
	upload, err := c.requestUpload(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := c.upload(ctx, upload, input); err != nil {
		return Result{}, err
	}
	token := strings.TrimSpace(input.ClientToken)
	if token == "" {
		hash := sha256.Sum256(input.Data)
		token = "video-ocr-" + hex.EncodeToString(hash[:20])
	}
	task, err := c.submit(ctx, upload.FileID, token)
	if err != nil {
		return Result{}, err
	}
	return c.wait(ctx, task.TaskID)
}

type uploadCredential struct {
	FileID        string
	Method        string
	UploadURL     string
	UploadHeaders []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
}

func (c *Client) requestUpload(ctx context.Context) (uploadCredential, error) {
	var response struct {
		apiResponse
		Result struct {
			FileID        string `json:"file_id"`
			Method        string `json:"method"`
			UploadURL     string `json:"upload_url"`
			UploadHeaders []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"upload_headers"`
		} `json:"result"`
	}
	if err := c.authorizedJSON(ctx, http.MethodPost, "/api/v1/tools-sync/request-media-upload-url", []byte("{}"), &response); err != nil {
		return uploadCredential{}, err
	}
	if !response.Success || response.Result.FileID == "" || response.Result.Method != http.MethodPut || response.Result.UploadURL == "" {
		return uploadCredential{}, response.failure("SUBTITLE_OCR_UPLOAD_PREPARE_FAILED")
	}
	if !strings.Contains(response.Result.FileID, "://") {
		response.Result.FileID = "mediakit://" + response.Result.FileID
	}
	return uploadCredential{
		FileID: response.Result.FileID, Method: response.Result.Method, UploadURL: response.Result.UploadURL,
		UploadHeaders: response.Result.UploadHeaders,
	}, nil
}

func (c *Client) upload(ctx context.Context, credential uploadCredential, input Input) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, credential.UploadURL, bytes.NewReader(input.Data))
	if err != nil {
		return newError("SUBTITLE_OCR_UPLOAD_FAILED", err)
	}
	for _, header := range credential.UploadHeaders {
		if strings.TrimSpace(header.Key) != "" {
			request.Header.Set(header.Key, header.Value)
		}
	}
	if request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", input.MIMEType)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return classifyTransportError("SUBTITLE_OCR_UPLOAD_FAILED", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.CopyN(io.Discard, response.Body, maxResponseBytes)
		return newError("SUBTITLE_OCR_UPLOAD_FAILED", fmt.Errorf("mediakit upload returned status %d", response.StatusCode))
	}
	return nil
}

type submittedTask struct {
	TaskID string
}

func (c *Client) submit(ctx context.Context, fileID, clientToken string) (submittedTask, error) {
	payload, err := json.Marshal(map[string]string{
		"video_url": fileID, "mode": "Subtitle", "client_token": clientToken,
	})
	if err != nil {
		return submittedTask{}, newError("SUBTITLE_OCR_REQUEST_INVALID", err)
	}
	var response apiResponse
	if err := c.authorizedJSON(ctx, http.MethodPost, "/api/v1/tools/video-ocr", payload, &response); err != nil {
		return submittedTask{}, err
	}
	if !response.Success || response.TaskID == "" {
		return submittedTask{}, response.failure("SUBTITLE_OCR_SUBMIT_FAILED")
	}
	return submittedTask{TaskID: response.TaskID}, nil
}

func (c *Client) wait(ctx context.Context, taskID string) (Result, error) {
	ticker := time.NewTicker(c.config.PollInterval)
	defer ticker.Stop()
	for {
		result, done, err := c.query(ctx, taskID)
		if err != nil {
			return Result{}, err
		}
		if done {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return Result{}, classifyTransportError("SUBTITLE_OCR_TIMEOUT", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (c *Client) query(ctx context.Context, taskID string) (Result, bool, error) {
	var response struct {
		apiResponse
		Status string `json:"status"`
		Result struct {
			Duration  float64 `json:"duration"`
			Subtitles []struct {
				StartTime    float64 `json:"start_time"`
				EndTime      float64 `json:"end_time"`
				SubtitleText string  `json:"subtitle_text"`
			} `json:"subtitles"`
		} `json:"result"`
	}
	path := "/api/v1/tasks/" + url.PathEscape(taskID)
	if err := c.authorizedJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return Result{}, false, err
	}
	if !response.Success {
		return Result{}, false, response.failure("SUBTITLE_OCR_QUERY_FAILED")
	}
	switch response.Status {
	case "running", "processing", "pending", "queued":
		return Result{}, false, nil
	case "failed":
		return Result{}, false, response.failure("SUBTITLE_OCR_TASK_FAILED")
	case "completed":
		result := Result{TaskID: response.TaskID, RequestID: response.RequestID, Duration: response.Result.Duration}
		for index, item := range response.Result.Subtitles {
			text := strings.TrimSpace(item.SubtitleText)
			if text == "" || item.StartTime < 0 || item.EndTime < item.StartTime {
				return Result{}, false, newError("SUBTITLE_OCR_RESULT_INVALID", fmt.Errorf(
					"mediakit returned invalid subtitle segment %d: empty=%t start=%.3f end=%.3f",
					index, text == "", item.StartTime, item.EndTime,
				))
			}
			result.Subtitles = append(result.Subtitles, Subtitle{
				Text: text, StartTime: item.StartTime, EndTime: item.EndTime,
			})
		}
		if len(result.Subtitles) == 0 {
			return Result{}, false, newError("SUBTITLE_OCR_RESULT_INVALID", errors.New("mediakit returned no subtitles"))
		}
		return result, true, nil
	default:
		return Result{}, false, newError("SUBTITLE_OCR_RESULT_INVALID", fmt.Errorf("unknown mediakit task status %q", response.Status))
	}
}

type apiResponse struct {
	Success   bool   `json:"success"`
	TaskID    string `json:"task_id"`
	RequestID string `json:"request_id"`
	Error     *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Param   string `json:"param"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (r apiResponse) failure(fallback string) error {
	if r.Error == nil {
		return newError(fallback, errors.New("mediakit request failed"))
	}
	code := fallback
	switch r.Error.Code {
	case "AuthenticationFailed", "Unauthorized":
		code = "SUBTITLE_OCR_AUTH_FAILED"
	case "InvalidParameter", "ParameterMissing":
		code = "SUBTITLE_OCR_INPUT_INVALID"
	}
	message := strings.TrimSpace(r.Error.Message)
	if message == "" {
		message = r.Error.Code
	}
	return newError(code, errors.New(message))
}

func (c *Client) authorizedJSON(ctx context.Context, method, path string, body []byte, output any) error {
	request, err := http.NewRequestWithContext(ctx, method, c.config.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		return newError("SUBTITLE_OCR_REQUEST_INVALID", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return classifyTransportError("SUBTITLE_OCR_UNAVAILABLE", err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil || len(data) > maxResponseBytes {
		return newError("SUBTITLE_OCR_RESPONSE_INVALID", errors.New("mediakit response could not be read"))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failed apiResponse
		if json.Unmarshal(data, &failed) == nil && failed.Error != nil {
			return failed.failure("SUBTITLE_OCR_UNAVAILABLE")
		}
		return newError("SUBTITLE_OCR_UNAVAILABLE", fmt.Errorf("mediakit returned status %d", response.StatusCode))
	}
	if err := json.Unmarshal(data, output); err != nil {
		return newError("SUBTITLE_OCR_RESPONSE_INVALID", err)
	}
	return nil
}

type Error struct {
	Code string
	Err  error
}

func (e *Error) Error() string { return e.Code + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func newError(code string, err error) error { return &Error{Code: code, Err: err} }

func classifyTransportError(code string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return newError("SUBTITLE_OCR_TIMEOUT", err)
	}
	return newError(code, err)
}

func ErrorCode(err error) string {
	var providerError *Error
	if errors.As(err, &providerError) {
		return providerError.Code
	}
	return "SUBTITLE_OCR_FAILED"
}
