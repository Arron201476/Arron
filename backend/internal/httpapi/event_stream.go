package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

const (
	eventStreamBatchSize = 256
	eventStreamPoll      = 500 * time.Millisecond
	eventStreamKeepalive = 15 * time.Second
)

type eventStreamSource struct {
	scope      string
	currentSeq func() (int64, error)
	list       func(afterSeq int64) (businessruntime.EventBatch, error)
	eventSeq   func(event businessruntime.Event) int64
}

func (s *Server) streamProjectEvents(writer http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("project_id")
	s.streamEvents(writer, request, eventStreamSource{
		scope: "project",
		currentSeq: func() (int64, error) {
			return s.runtime.CurrentProjectEventSeq(request.Context(), projectID)
		},
		list: func(afterSeq int64) (businessruntime.EventBatch, error) {
			return s.runtime.ListProjectEvents(request.Context(), projectID, afterSeq, eventStreamBatchSize)
		},
		eventSeq: func(event businessruntime.Event) int64 {
			return event.ProjectEventSeq
		},
	})
}

func (s *Server) streamRunEvents(writer http.ResponseWriter, request *http.Request) {
	runID := request.PathValue("run_id")
	s.streamEvents(writer, request, eventStreamSource{
		scope: "run",
		currentSeq: func() (int64, error) {
			return s.runtime.CurrentRunEventSeq(request.Context(), runID)
		},
		list: func(afterSeq int64) (businessruntime.EventBatch, error) {
			return s.runtime.ListRunEvents(request.Context(), runID, afterSeq, eventStreamBatchSize)
		},
		eventSeq: func(event businessruntime.Event) int64 {
			if event.RunEventSeq == nil {
				return 0
			}
			return *event.RunEventSeq
		},
	})
}

func (s *Server) streamEvents(
	writer http.ResponseWriter,
	request *http.Request,
	source eventStreamSource,
) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, "SSE_UNSUPPORTED", "当前连接不支持事件流。")
		return
	}
	afterSeq, hasCursor, ok := parseEventStreamCursor(writer, request)
	if !ok {
		return
	}
	if err := s.authorizeEventStream(request); err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	currentSeq, err := source.currentSeq()
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	if !hasCursor {
		afterSeq = currentSeq
	}

	writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-cache, no-transform")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(writer, "retry: 3000\n\n"); err != nil {
		return
	}

	if afterSeq > currentSeq {
		writeStreamControl(writer, "stream.reset_required", map[string]any{
			"reason":            "cursor_ahead",
			"requested_seq":     afterSeq,
			"min_available_seq": minimumAvailableSeq(currentSeq),
			"current_seq":       currentSeq,
			"stream_scope":      source.scope,
			"requires_snapshot": true,
		})
		flusher.Flush()
		return
	}

	pollTimer := time.NewTimer(0)
	defer pollTimer.Stop()
	keepaliveTicker := time.NewTicker(eventStreamKeepalive)
	defer keepaliveTicker.Stop()

	cursor := afterSeq
	for {
		select {
		case <-request.Context().Done():
			return
		case <-keepaliveTicker.C:
			if err := s.authorizeEventStream(request); err != nil {
				writeStreamControl(writer, "stream.access_revoked", map[string]any{"stream_scope": source.scope})
				flusher.Flush()
				return
			}
			if _, err := fmt.Fprint(writer, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-pollTimer.C:
			for {
				if err := s.authorizeEventStream(request); err != nil {
					writeStreamControl(writer, "stream.access_revoked", map[string]any{"stream_scope": source.scope})
					flusher.Flush()
					return
				}
				batch, err := source.list(cursor)
				if err != nil {
					var domain *businessruntime.DomainError
					if errors.As(err, &domain) && domain.Code == "EVENT_CURSOR_AHEAD" {
						writeStreamControl(writer, "stream.reset_required", map[string]any{
							"reason":            "cursor_ahead",
							"requested_seq":     cursor,
							"stream_scope":      source.scope,
							"requires_snapshot": true,
						})
					} else if errors.As(err, &domain) && (domain.Code == "AUTHENTICATION_REQUIRED" || domain.Code == "WORKSPACE_ACCESS_DENIED" || domain.Code == "PROJECT_NOT_FOUND" || domain.Code == "RUN_NOT_FOUND") {
						writeStreamControl(writer, "stream.access_revoked", map[string]any{"stream_scope": source.scope})
					} else {
						s.logger.Error("event stream replay failed", "scope", source.scope, "error", err)
						writeStreamControl(writer, "stream.error", map[string]any{
							"code":         "EVENT_STREAM_FAILED",
							"stream_scope": source.scope,
						})
					}
					flusher.Flush()
					return
				}
				var changedProject string
				var changedSeq int64
				for _, event := range batch.Items {
					seq := source.eventSeq(event)
					if seq <= cursor {
						continue
					}
					if err := writeEventFrame(writer, seq, event); err != nil {
						return
					}
					cursor = seq
					if source.scope == "project" && event.EventType != "agent.output.delta" {
						changedProject, changedSeq = event.ProjectID, event.ProjectEventSeq
					}
				}
				if changedProject != "" {
					// Notification only: the original event frames retain replay IDs.
					writeStreamControl(writer, "stream.snapshot_invalidated", map[string]any{
						"stream_scope": "project", "project_id": changedProject, "project_event_seq": changedSeq,
					})
				}
				if len(batch.Items) > 0 {
					flusher.Flush()
				}
				if !batch.HasMore {
					break
				}
			}
			pollTimer.Reset(eventStreamPoll)
		}
	}
}

func (s *Server) authorizeEventStream(request *http.Request) error {
	original, present := identity.FromContext(request.Context())
	current, _, authenticated := s.authenticateRequest(request)
	if !present || !authenticated || current.Kind != original.Kind || current.UserID != original.UserID ||
		current.WorkspaceID != original.WorkspaceID || current.AuthMethod != original.AuthMethod {
		return &businessruntime.DomainError{Code: "AUTHENTICATION_REQUIRED", Message: "事件流登录状态已失效。"}
	}
	if activity, bound := businessruntime.AgentActivityFromContext(request.Context()); bound {
		_, err := s.runtime.ResolveAgentActivityPrincipal(request.Context(), activity)
		return err
	}
	return nil
}

func parseEventStreamCursor(
	writer http.ResponseWriter,
	request *http.Request,
) (afterSeq int64, hasCursor bool, ok bool) {
	raw := request.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = request.URL.Query().Get("after_seq")
	}
	if raw == "" {
		return 0, false, true
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < 0 {
		writeError(writer, http.StatusBadRequest, "EVENT_CURSOR_INVALID", "事件游标必须是非负整数。")
		return 0, false, false
	}
	return parsed, true, true
}

func writeEventFrame(writer http.ResponseWriter, seq int64, event businessruntime.Event) error {
	data, err := json.Marshal(eventEnvelope(event))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		writer,
		"id: %d\nevent: %s\ndata: %s\n\n",
		seq,
		event.EventType,
		data,
	)
	return err
}

func writeStreamControl(writer http.ResponseWriter, eventType string, payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	cursorReset := ""
	if eventType == "stream.reset_required" {
		// An empty SSE id clears Last-Event-ID on the next native reconnect.
		cursorReset = "id:\n"
	}
	_, _ = fmt.Fprintf(writer, "%sevent: %s\ndata: %s\n\n", cursorReset, eventType, data)
}

func eventEnvelope(event businessruntime.Event) map[string]any {
	var projected struct {
		AgentEvent struct {
			SchemaVersion  string          `json:"schema_version"`
			ConversationID string          `json:"conversation_id"`
			TurnID         string          `json:"turn_id"`
			Terminal       bool            `json:"terminal"`
			Payload        json.RawMessage `json:"payload"`
		} `json:"agent_event"`
	}
	if json.Unmarshal(event.Payload, &projected) == nil &&
		projected.AgentEvent.SchemaVersion != "" &&
		projected.AgentEvent.TurnID != "" {
		return map[string]any{
			"schema_version":    projected.AgentEvent.SchemaVersion,
			"event_id":          event.EventID,
			"event_type":        event.EventType,
			"project_id":        event.ProjectID,
			"conversation_id":   projected.AgentEvent.ConversationID,
			"turn_id":           projected.AgentEvent.TurnID,
			"terminal":          projected.AgentEvent.Terminal,
			"payload":           projected.AgentEvent.Payload,
			"occurred_at":       event.OccurredAt,
			"project_event_seq": event.ProjectEventSeq,
		}
	}
	return map[string]any{
		"event_id":          event.EventID,
		"event_type":        event.EventType,
		"schema_version":    event.SchemaVersion,
		"project_id":        event.ProjectID,
		"run_id":            event.RunID,
		"step_run_id":       event.StepRunID,
		"project_event_seq": event.ProjectEventSeq,
		"run_event_seq":     event.RunEventSeq,
		"actor": map[string]any{
			"kind": event.ActorKind,
			"ref":  event.ActorRef,
		},
		"subject": map[string]any{
			"resource_type": event.SubjectType,
			"resource_id":   event.SubjectID,
		},
		"payload":     event.Payload,
		"occurred_at": event.OccurredAt,
	}
}

func minimumAvailableSeq(currentSeq int64) int64 {
	if currentSeq == 0 {
		return 0
	}
	return 1
}
