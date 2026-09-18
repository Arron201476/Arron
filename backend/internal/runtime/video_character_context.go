package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"content-agent/backend/internal/videoscript"
)

type priorVideoCharacterUnit struct {
	Order   int
	Payload json.RawMessage
}

const (
	maxVideoCharacterRegistryEntries = 200
	maxVideoCharacterAliases         = 8
	maxVideoPreviousEndRunes         = 2_000
)

type videoCharacterRegistryEntry struct {
	CanonicalName    string   `json:"canonical_name"`
	Aliases          []string `json:"aliases"`
	FirstSeenEpisode int      `json:"first_seen_episode"`
	LastSeenEpisode  int      `json:"last_seen_episode"`
	MentionCount     int      `json:"mention_count"`
}

func (s *Store) videoCharacterContinuityCursorTx(
	ctx context.Context,
	tx *sql.Tx,
	request stepContextBuildRequest,
) (json.RawMessage, error) {
	currentOrder := episodeOrderFromItemKey(request.ItemKey)
	if currentOrder <= 1 {
		return request.TaskCursor, nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT a.scope_key, av.payload_json
		FROM artifacts a
		JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
		WHERE a.run_id = ? AND a.step_run_id = ?
			AND a.artifact_type = 'video_script_unit'`,
		request.RunID,
		request.StepRunID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	units := make([]priorVideoCharacterUnit, 0, currentOrder-1)
	for rows.Next() {
		var scopeKey, payload string
		if err := rows.Scan(&scopeKey, &payload); err != nil {
			return nil, err
		}
		order := episodeOrderFromItemKey(scopeKey)
		if order > 0 && order < currentOrder {
			units = append(units, priorVideoCharacterUnit{Order: order, Payload: json.RawMessage(payload)})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return enrichVideoCharacterCursor(request.TaskCursor, units)
}

func enrichVideoCharacterCursor(
	cursorJSON json.RawMessage,
	units []priorVideoCharacterUnit,
) (json.RawMessage, error) {
	var cursor map[string]any
	if err := json.Unmarshal(cursorJSON, &cursor); err != nil {
		return nil, err
	}
	video, _ := cursor["video"].(map[string]any)
	if video == nil {
		return cursorJSON, nil
	}
	sort.Slice(units, func(i, j int) bool { return units[i].Order < units[j].Order })
	registry := make([]videoCharacterRegistryEntry, 0)
	previousEnd := ""
	for _, unit := range units {
		var payload struct {
			PlotSummary string `json:"plot_summary"`
		}
		if err := json.Unmarshal(unit.Payload, &payload); err != nil {
			return nil, err
		}
		names, err := videoscript.CharacterNames(unit.Payload)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			registry = registerVideoCharacter(registry, name, unit.Order)
		}
		if strings.TrimSpace(payload.PlotSummary) != "" {
			previousEnd = strings.TrimSpace(payload.PlotSummary)
		}
	}
	characters := make([]string, 0, len(registry))
	registry = boundedVideoCharacterRegistry(registry, maxVideoCharacterRegistryEntries)
	for _, entry := range registry {
		characters = append(characters, entry.CanonicalName)
	}
	video["known_characters"] = characters
	video["character_registry"] = registry
	video["previous_episode_end"] = tailRunes(previousEnd, maxVideoPreviousEndRunes)
	cursor["video"] = video
	return json.Marshal(cursor)
}

func normalizeVideoScriptCharacterContinuity(
	cursorJSON json.RawMessage,
	payload json.RawMessage,
) (json.RawMessage, error) {
	var cursor struct {
		Video struct {
			EpisodeOrder      int                           `json:"episode_order"`
			CharacterRegistry []videoCharacterRegistryEntry `json:"character_registry"`
		} `json:"video"`
	}
	if err := json.Unmarshal(cursorJSON, &cursor); err != nil {
		return nil, fmt.Errorf("decode video character continuity cursor: %w", err)
	}
	registry := append([]videoCharacterRegistryEntry(nil), cursor.Video.CharacterRegistry...)
	names, err := videoscript.CharacterNames(payload)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		registry = registerVideoCharacter(registry, name, cursor.Video.EpisodeOrder)
	}
	resolve := func(raw string) (string, error) {
		name := strings.TrimSpace(raw)
		if ignoredVideoCharacter(name) {
			return name, nil
		}
		matches := make([]string, 0, 1)
		for _, entry := range registry {
			if videoCharacterExactAlias(entry, name) || safeVideoCharacterAlias(entry.CanonicalName, name) {
				matches = appendUniqueString(matches, entry.CanonicalName)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return "", fmt.Errorf("character name %q matches multiple canonical characters: %s", name, strings.Join(matches, ", "))
		}
		return name, nil
	}
	return videoscript.RewriteCharacterNames(payload, resolve)
}

func videoCharacterMatchesRegistry(registry []videoCharacterRegistryEntry, name string) bool {
	for _, entry := range registry {
		if videoCharacterExactAlias(entry, name) || safeVideoCharacterAlias(entry.CanonicalName, name) {
			return true
		}
	}
	return false
}

func registerVideoCharacter(
	registry []videoCharacterRegistryEntry,
	raw string,
	episode int,
) []videoCharacterRegistryEntry {
	name := strings.TrimSpace(raw)
	if ignoredVideoCharacter(name) {
		return registry
	}
	for index := range registry {
		entry := &registry[index]
		if videoCharacterExactAlias(*entry, name) || safeVideoCharacterAlias(entry.CanonicalName, name) {
			entry.Aliases = appendUniqueString(entry.Aliases, name)
			if preferVideoCanonicalName(name, entry.CanonicalName) {
				entry.Aliases = appendUniqueString(entry.Aliases, entry.CanonicalName)
				entry.CanonicalName = name
			}
			if episode > 0 {
				if entry.FirstSeenEpisode == 0 || episode < entry.FirstSeenEpisode {
					entry.FirstSeenEpisode = episode
				}
				if episode > entry.LastSeenEpisode {
					entry.LastSeenEpisode = episode
				}
			}
			entry.Aliases = keepLastStrings(entry.Aliases, maxVideoCharacterAliases)
			entry.MentionCount++
			return registry
		}
	}
	return append(registry, videoCharacterRegistryEntry{
		CanonicalName:    name,
		Aliases:          []string{name},
		FirstSeenEpisode: episode,
		LastSeenEpisode:  episode,
		MentionCount:     1,
	})
}

func boundedVideoCharacterRegistry(
	registry []videoCharacterRegistryEntry,
	limit int,
) []videoCharacterRegistryEntry {
	if limit <= 0 || len(registry) <= limit {
		return registry
	}
	sort.SliceStable(registry, func(i, j int) bool {
		if registry[i].MentionCount != registry[j].MentionCount {
			return registry[i].MentionCount > registry[j].MentionCount
		}
		return registry[i].LastSeenEpisode > registry[j].LastSeenEpisode
	})
	return registry[:limit]
}

func keepLastStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	return append([]string(nil), values[len(values)-limit:]...)
}

func tailRunes(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[len(runes)-limit:])
}

func videoCharacterExactAlias(entry videoCharacterRegistryEntry, name string) bool {
	if entry.CanonicalName == name {
		return true
	}
	for _, alias := range entry.Aliases {
		if alias == name {
			return true
		}
	}
	return false
}

func safeVideoCharacterAlias(left, right string) bool {
	left = normalizedVideoCharacterMatchName(left)
	right = normalizedVideoCharacterMatchName(right)
	if left == "" || right == "" || left == right {
		return left != "" && left == right
	}
	leftRunes, rightRunes := []rune(left), []rune(right)
	shorter, longer := left, right
	if len(leftRunes) > len(rightRunes) {
		shorter, longer = right, left
		leftRunes, rightRunes = rightRunes, leftRunes
	}
	if len(rightRunes)-len(leftRunes) != 1 || len(leftRunes) < 2 || genericVideoCharacterAlias(shorter) {
		return false
	}
	return strings.HasPrefix(longer, shorter) || strings.HasSuffix(longer, shorter)
}

func preferVideoCanonicalName(candidate, current string) bool {
	return utf8.RuneCountInString(normalizedVideoCharacterMatchName(candidate)) >
		utf8.RuneCountInString(normalizedVideoCharacterMatchName(current))
}

func normalizedVideoCharacterMatchName(name string) string {
	name = strings.TrimSpace(name)
	for _, suffix := range []string{"VO", "OS", "V.O.", "O.S."} {
		name = strings.TrimSpace(strings.TrimSuffix(name, suffix))
	}
	return name
}

func ignoredVideoCharacter(name string) bool {
	switch normalizedVideoCharacterMatchName(name) {
	case "", "未知人物", "旁白", "系统", "画外音":
		return true
	default:
		return false
	}
}

func genericVideoCharacterAlias(name string) bool {
	switch name {
	case "师傅", "老板", "书记", "主任", "村民", "男人", "女人", "男子", "女子",
		"先生", "小姐", "妈妈", "爸爸", "爷爷", "奶奶", "老人", "年轻人", "顾客":
		return true
	default:
		return false
	}
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func episodeOrderFromItemKey(value string) int {
	raw := strings.TrimPrefix(strings.TrimSpace(value), "episode:")
	order, _ := strconv.Atoi(raw)
	return order
}
