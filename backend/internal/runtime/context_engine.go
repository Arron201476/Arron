package runtime

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"content-agent/backend/internal/agentcontract"
)

const (
	agentRecentMessageLimit    = 12
	agentRetrievedMessageLimit = 6
	agentMemoryEntryLimit      = 12
	agentMemoryEntryTextLimit  = 1000
	agentMemoryTotalBytes      = 16 * 1024
	conversationSummaryLimit   = 3200
)

// AgentTurnContext is the single assembly result for a Main Agent turn.
// Business state remains in Runtime; memory is derived and rebuildable.
type AgentTurnContext struct {
	RecentMessages []agentcontract.ConversationMessage
	MemoryContext  agentcontract.MemoryContext
}

// BuildAgentTurnContext assembles recent conversation, historical retrieval,
// a rolling extractive summary, and rebuildable project memory.
func (s *Store) BuildAgentTurnContext(
	ctx context.Context,
	projectID string,
	conversationID string,
	messages []Message,
	request agentcontract.MessageRequest,
	runtimeContext agentcontract.RuntimeContext,
) (AgentTurnContext, error) {
	relevant := RelevantConversationMessages(
		messages,
		request,
		runtimeContext,
		len(messages)+1,
	)
	recentStart := max(0, len(relevant)-agentRecentMessageLimit)
	recent := append([]agentcontract.ConversationMessage(nil), relevant[recentStart:]...)
	historical := relevant[:recentStart]

	summary, err := s.refreshConversationSummary(ctx, projectID, conversationID, historical)
	if err != nil {
		return AgentTurnContext{}, err
	}
	if err := s.refreshDerivedProjectMemory(ctx, projectID); err != nil {
		return AgentTurnContext{}, err
	}
	entries, err := s.retrieveProjectMemory(ctx, projectID, request.Content, agentMemoryEntryLimit)
	if err != nil {
		return AgentTurnContext{}, err
	}

	return AgentTurnContext{
		RecentMessages: recent,
		MemoryContext: agentcontract.MemoryContext{
			ConversationSummary: summary,
			RetrievedMessages:   retrieveHistoricalMessages(historical, request.Content, agentRetrievedMessageLimit),
			Entries:             entries,
		},
	}, nil
}

func (s *Store) refreshConversationSummary(
	ctx context.Context,
	projectID string,
	conversationID string,
	messages []agentcontract.ConversationMessage,
) (*agentcontract.ConversationSummaryContext, error) {
	projectMessages := make([]agentcontract.ConversationMessage, 0, len(messages))
	for _, message := range messages {
		if message.Scope == "project" || message.Scope == "" {
			projectMessages = append(projectMessages, message)
		}
	}
	if len(projectMessages) == 0 {
		return nil, nil
	}

	selected := projectMessages
	if len(selected) > 12 {
		selected = append(
			append([]agentcontract.ConversationMessage(nil), selected[:4]...),
			selected[len(selected)-8:]...,
		)
	}
	var builder strings.Builder
	for _, message := range selected {
		line := strings.TrimSpace(message.Content)
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > 320 {
			line = string([]rune(line)[:320])
		}
		role := "用户"
		if message.Role == "assistant" {
			role = "Agent"
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(role + "：" + line)
		if utf8.RuneCountInString(builder.String()) >= conversationSummaryLimit {
			break
		}
	}
	summaryText := truncateUTF8(builder.String(), conversationSummaryLimit)
	covered := projectMessages[len(projectMessages)-1].MessageID
	source := strings.Builder{}
	for _, message := range projectMessages {
		source.WriteString(message.MessageID)
		source.WriteByte('\x00')
		source.WriteString(message.Content)
		source.WriteByte('\x00')
	}
	sourceHash := sha256Hex([]byte(source.String()))
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO conversation_summaries(
			conversation_summary_id, project_id, conversation_id, scope,
			covered_through_message_id, covered_message_count, summary_text,
			source_hash, created_at, updated_at
		) VALUES(?, ?, ?, 'project', ?, ?, ?, ?, ?, ?)
		ON CONFLICT(conversation_id, scope) DO UPDATE SET
			covered_through_message_id = excluded.covered_through_message_id,
			covered_message_count = excluded.covered_message_count,
			summary_text = excluded.summary_text,
			source_hash = excluded.source_hash,
			updated_at = excluded.updated_at`,
		s.newID("cs"), projectID, conversationID, covered, len(projectMessages),
		summaryText, sourceHash, formatTime(now), formatTime(now),
	); err != nil {
		return nil, err
	}
	return &agentcontract.ConversationSummaryContext{
		Scope: "project", CoveredThroughMessageID: covered,
		CoveredMessageCount: len(projectMessages), Summary: summaryText,
	}, nil
}

func (s *Store) refreshDerivedProjectMemory(ctx context.Context, projectID string) error {
	now := formatTime(s.now())
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.artifact_id, a.current_version_id, a.artifact_type, a.scope_key,
			av.payload_json
		FROM artifacts a
		JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
		WHERE a.project_id = ?
		ORDER BY a.updated_at DESC, a.artifact_id`, projectID)
	if err != nil {
		return err
	}
	type catalogItem struct {
		artifactID, versionID, artifactType, scopeKey, label, payload string
	}
	items := make([]catalogItem, 0)
	for rows.Next() {
		var item catalogItem
		if err := rows.Scan(&item.artifactID, &item.versionID, &item.artifactType, &item.scopeKey, &item.payload); err != nil {
			rows.Close()
			return err
		}
		item.label = artifactDisplayLabel(item.artifactType, json.RawMessage(item.payload))
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range items {
		payload, _ := json.Marshal(map[string]string{
			"artifact_id": item.artifactID, "artifact_version_id": item.versionID,
			"artifact_type": item.artifactType, "scope_key": item.scopeKey,
			"label": item.label,
		})
		content := strings.TrimSpace(strings.Join([]string{item.label, item.artifactType, scopeDisplay(item.scopeKey)}, " "))
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO project_memory_entries(
				memory_entry_id, project_id, kind, scope, content, payload_json,
				source_kind, source_ref_id, source_hash, confidence, status,
				created_at, updated_at
			) VALUES(?, ?, 'artifact_catalog', ?, ?, ?, 'artifact', ?, ?, 1, 'active', ?, ?)
			ON CONFLICT(project_id, kind, scope, source_kind, source_ref_id) DO UPDATE SET
				content = excluded.content, payload_json = excluded.payload_json,
				source_hash = excluded.source_hash, confidence = excluded.confidence,
				status = 'active', updated_at = excluded.updated_at, invalidated_at = NULL`,
			s.newID("mem"), projectID, "artifact:"+item.artifactID, content,
			string(payload), item.artifactID, item.versionID, now, now,
		); err != nil {
			return err
		}
	}

	decisionRows, err := s.db.QueryContext(ctx, `
		SELECT decision_snapshot_id, run_id, decision_type, payload_json, snapshot_hash
		FROM run_decision_snapshots
		WHERE project_id = ? AND status = 'sealed'
		ORDER BY created_at DESC, decision_snapshot_id DESC LIMIT 40`, projectID)
	if err != nil {
		return err
	}
	defer decisionRows.Close()
	for decisionRows.Next() {
		var id, runID, decisionType, payload, hash string
		if err := decisionRows.Scan(&id, &runID, &decisionType, &payload, &hash); err != nil {
			return err
		}
		content := decisionType + " " + truncateUTF8(payload, 1000)
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO project_memory_entries(
				memory_entry_id, project_id, kind, scope, content, payload_json,
				source_kind, source_ref_id, source_hash, confidence, status,
				created_at, updated_at
			) VALUES(?, ?, 'confirmed_decision', ?, ?, ?, 'decision_snapshot', ?, ?, 1, 'active', ?, ?)
			ON CONFLICT(project_id, kind, scope, source_kind, source_ref_id) DO UPDATE SET
				content = excluded.content, payload_json = excluded.payload_json,
				source_hash = excluded.source_hash, status = 'active',
				updated_at = excluded.updated_at, invalidated_at = NULL`,
			s.newID("mem"), projectID, "run:"+runID, content, payload, id, hash, now, now,
		); err != nil {
			return err
		}
	}
	return decisionRows.Err()
}

func (s *Store) retrieveProjectMemory(
	ctx context.Context,
	projectID string,
	query string,
	limit int,
) ([]agentcontract.MemoryEntryContext, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT kind, scope, content, payload_json, source_kind, source_ref_id, confidence
		FROM project_memory_entries
		WHERE project_id = ? AND status = 'active'
		ORDER BY updated_at DESC, memory_entry_id DESC LIMIT 200`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type scored struct {
		entry agentcontract.MemoryEntryContext
		score float64
	}
	candidates := make([]scored, 0)
	for rows.Next() {
		var item scored
		var payload string
		if err := rows.Scan(
			&item.entry.Kind, &item.entry.Scope, &item.entry.Content, &payload,
			&item.entry.SourceKind, &item.entry.SourceRef, &item.entry.Confidence,
		); err != nil {
			return nil, err
		}
		item.entry.Content = truncateUTF8(item.entry.Content, agentMemoryEntryTextLimit)
		// Artifact bodies and decision snapshots remain in their authoritative
		// stores. Main Agent memory only carries a bounded summary plus the small
		// artifact-catalog reference needed to locate content on demand.
		if item.entry.Kind == "artifact_catalog" && len(payload) <= 1024 && json.Valid([]byte(payload)) {
			item.entry.Payload = json.RawMessage(payload)
		}
		item.score = memoryTextScore(query, item.entry.Content)
		if item.entry.Kind == "confirmed_decision" {
			item.score += 0.25
		}
		if item.score > 0 {
			candidates = append(candidates, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	result := make([]agentcontract.MemoryEntryContext, 0, len(candidates))
	usedBytes := 0
	for index := range candidates {
		entry := candidates[index].entry
		entryBytes := len(entry.Kind) + len(entry.Scope) + len(entry.Content) +
			len(entry.Payload) + len(entry.SourceKind) + len(entry.SourceRef)
		if usedBytes+entryBytes > agentMemoryTotalBytes {
			continue
		}
		result = append(result, entry)
		usedBytes += entryBytes
	}
	return result, nil
}

func retrieveHistoricalMessages(
	messages []agentcontract.ConversationMessage,
	query string,
	limit int,
) []agentcontract.ConversationMessage {
	type scored struct {
		message agentcontract.ConversationMessage
		score   float64
		index   int
	}
	wantsHistory := containsHistoryCue(query)
	candidates := make([]scored, 0, len(messages))
	for index, message := range messages {
		score := memoryTextScore(query, message.Content)
		if wantsHistory && message.Role == "user" {
			score += 0.2 + 0.2*float64(len(messages)-index)/float64(max(1, len(messages)))
		}
		if score > 0 {
			candidates = append(candidates, scored{message: message, score: score, index: index})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].index < candidates[j].index
		}
		return candidates[i].score > candidates[j].score
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	result := make([]agentcontract.ConversationMessage, len(candidates))
	for index := range candidates {
		result[index] = candidates[index].message
		result[index].Content = truncateUTF8(result[index].Content, 1200)
	}
	return result
}

func memoryTextScore(query string, candidate string) float64 {
	query = strings.ToLower(strings.TrimSpace(query))
	candidate = strings.ToLower(strings.TrimSpace(candidate))
	if query == "" || candidate == "" {
		return 0
	}
	score := 0.0
	if strings.Contains(candidate, query) || strings.Contains(query, candidate) {
		score += 1
	}
	queryTerms := memoryTerms(query)
	if len(queryTerms) == 0 {
		return score
	}
	candidateTerms := memoryTerms(candidate)
	matched := 0
	for term := range queryTerms {
		if _, ok := candidateTerms[term]; ok {
			matched++
		}
	}
	return score + float64(matched)/float64(len(queryTerms))
}

func memoryTerms(value string) map[string]struct{} {
	result := make(map[string]struct{})
	var latin []rune
	var cjk []rune
	flushLatin := func() {
		if len(latin) >= 2 {
			result[string(latin)] = struct{}{}
		}
		latin = nil
	}
	flushCJK := func() {
		for index := 0; index < len(cjk); index++ {
			result[string(cjk[index])] = struct{}{}
			if index+1 < len(cjk) {
				result[string(cjk[index:index+2])] = struct{}{}
			}
		}
		cjk = nil
	}
	for _, char := range []rune(strings.ToLower(value)) {
		switch {
		case unicode.Is(unicode.Han, char):
			flushLatin()
			cjk = append(cjk, char)
		case unicode.IsLetter(char) || unicode.IsDigit(char):
			flushCJK()
			latin = append(latin, char)
		default:
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	return result
}

func containsHistoryCue(value string) bool {
	value = strings.ToLower(value)
	for _, cue := range []string{"最开始", "最初", "之前", "先前", "说过", "聊过", "还记得", "第一轮", "历史"} {
		if strings.Contains(value, cue) {
			return true
		}
	}
	return false
}

func artifactDisplayLabel(artifactType string, payload json.RawMessage) string {
	var object map[string]any
	if json.Unmarshal(payload, &object) == nil {
		for _, key := range []string{"artifact_label", "title"} {
			if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return artifactLabel(artifactType)
}
