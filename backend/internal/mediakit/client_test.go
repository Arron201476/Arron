package mediakit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAvailableFromEnvRequiresValidConfiguration(t *testing.T) {
	t.Setenv("CONTENT_AGENT_MEDIAKIT_ENDPOINT", "https://mediakit.example.com")
	t.Setenv("CONTENT_AGENT_MEDIAKIT_API_KEY", "")
	if AvailableFromEnv() {
		t.Fatal("MediaKit must be unavailable without an API key")
	}
	t.Setenv("CONTENT_AGENT_MEDIAKIT_API_KEY", "test-key")
	if !AvailableFromEnv() {
		t.Fatal("MediaKit must be available with a valid endpoint and API key")
	}
}

func TestClientExtractUploadsSubmitsAndPolls(t *testing.T) {
	var server *httptest.Server
	var queryCount atomic.Int32
	server = httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/tools-sync/request-media-upload-url":
			assertBearer(t, request)
			writeJSON(t, response, map[string]any{
				"success": true,
				"result": map[string]any{
					"file_id": "file-1", "method": "PUT", "upload_url": server.URL + "/upload",
					"upload_headers": []map[string]string{{"key": "X-Upload-Test", "value": "yes"}},
				},
			})
		case "/upload":
			if request.Method != http.MethodPut || request.Header.Get("Authorization") != "" ||
				request.Header.Get("X-Upload-Test") != "yes" || request.Header.Get("Content-Type") != "video/mp4" {
				t.Fatalf("upload request = %+v", request)
			}
			data, _ := io.ReadAll(request.Body)
			if string(data) != "video-data" {
				t.Fatalf("upload body = %q", data)
			}
			response.WriteHeader(http.StatusOK)
		case "/api/v1/tools/video-ocr":
			assertBearer(t, request)
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil ||
				body["video_url"] != "mediakit://file-1" || body["mode"] != "Subtitle" || body["client_token"] != "token-1" {
				t.Fatalf("submit body = %+v, error = %v", body, err)
			}
			writeJSON(t, response, map[string]any{"success": true, "task_id": "task-1"})
		case "/api/v1/tasks/task-1":
			assertBearer(t, request)
			if queryCount.Add(1) == 1 {
				writeJSON(t, response, map[string]any{"success": true, "task_id": "task-1", "status": "running"})
				return
			}
			writeJSON(t, response, map[string]any{
				"success": true, "task_id": "task-1", "request_id": "request-1", "status": "completed",
				"result": map[string]any{"duration": 2.5, "subtitles": []map[string]any{
					{"start_time": 0.1, "end_time": 1.1, "subtitle_text": "人物对白", "text_label": "Subtitle"},
					{"start_time": 1.2, "end_time": 2.2, "subtitle_text": "系统提示", "text_label": "Others",
						"text_location": map[string]int{"top_left_x": 1, "top_left_y": 2, "bottom_right_x": 3, "bottom_right_y": 4}},
				}},
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := New(Config{
		Endpoint: server.URL, APIKey: "secret", PollInterval: time.Millisecond,
	}, server.Client())
	if err == nil {
		t.Fatal("poll interval below the production minimum must be rejected")
	}
	client, err = New(Config{
		Endpoint: server.URL, APIKey: "secret", PollInterval: 500 * time.Millisecond,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := client.Extract(ctx, Input{MIMEType: "video/mp4", Data: []byte("video-data"), ClientToken: "token-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.TaskID != "task-1" || result.RequestID != "request-1" || len(result.Subtitles) != 2 ||
		result.Subtitles[0].Text != "人物对白" || result.Subtitles[1].Text != "系统提示" {
		t.Fatalf("result = %+v", result)
	}
}

func TestClientClassifiesAuthenticationFailure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusUnauthorized)
		writeJSON(t, response, map[string]any{
			"success": false, "error": map[string]string{"code": "AuthenticationFailed", "message": "invalid key"},
		})
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, APIKey: "bad"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Extract(context.Background(), Input{MIMEType: "video/mp4", Data: []byte("video")})
	if ErrorCode(err) != "SUBTITLE_OCR_AUTH_FAILED" || strings.Contains(err.Error(), "bad") {
		t.Fatalf("error = %v, code = %s", err, ErrorCode(err))
	}
}

func assertBearer(t *testing.T, request *http.Request) {
	t.Helper()
	if request.Header.Get("Authorization") != "Bearer secret" {
		t.Fatalf("authorization header is missing")
	}
}

func writeJSON(t *testing.T, response http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(response).Encode(value); err != nil {
		t.Fatal(err)
	}
}
