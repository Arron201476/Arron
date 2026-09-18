package videoscript

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// NormalizeUnitPayload treats scenes as the authority and rebuilds the
// script_text mirror deterministically before the artifact is committed.
func NormalizeUnitPayload(payload json.RawMessage) (json.RawMessage, error) {
	unit, err := decodeObject(payload)
	if err != nil {
		return nil, err
	}
	episode := integerValue(unit["episode_order"])
	if episodeNo := integerValue(unit["episode_no"]); episodeNo > 0 {
		episode = episodeNo
	}
	if episode <= 0 {
		return nil, errors.New("video script episode number is missing")
	}
	scenes, ok := unit["scenes"].([]any)
	if !ok || len(scenes) == 0 {
		return nil, errors.New("video script requires at least one scene")
	}

	var rendered strings.Builder
	fmt.Fprintf(&rendered, "第%d集", episode)
	contentBlocks := 0
	for sceneIndex, sceneValue := range scenes {
		scene, ok := sceneValue.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("video script scene %d is invalid", sceneIndex+1)
		}
		heading := strings.TrimSpace(stringValue(scene["heading"]))
		if heading == "" {
			return nil, fmt.Errorf("video script scene %d heading is empty", sceneIndex+1)
		}
		blocks, ok := scene["blocks"].([]any)
		if !ok || len(blocks) == 0 {
			return nil, fmt.Errorf("video script scene %d has no content blocks", sceneIndex+1)
		}
		rendered.WriteString("\n\n")
		rendered.WriteString(heading)
		if characters := stringSlice(scene["characters"]); len(characters) > 0 {
			rendered.WriteString("\n人物：")
			rendered.WriteString(strings.Join(characters, "、"))
		}
		for blockIndex, blockValue := range blocks {
			block, ok := blockValue.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("video script scene %d block %d is invalid", sceneIndex+1, blockIndex+1)
			}
			text := strings.TrimSpace(stringValue(block["text"]))
			if text == "" {
				return nil, fmt.Errorf("video script scene %d block %d text is empty", sceneIndex+1, blockIndex+1)
			}
			contentBlocks++
			rendered.WriteByte('\n')
			switch stringValue(block["block_type"]) {
			case "dialogue":
				speaker := strings.TrimSpace(stringValue(block["speaker"]))
				if speaker == "" {
					return nil, fmt.Errorf("video script scene %d dialogue %d speaker is empty", sceneIndex+1, blockIndex+1)
				}
				rendered.WriteString(speaker)
				if delivery := strings.TrimSpace(stringValue(block["delivery"])); delivery != "" {
					rendered.WriteString("（")
					rendered.WriteString(delivery)
					rendered.WriteString("）")
				}
				rendered.WriteString("：")
				rendered.WriteString(text)
			case "action":
				if !strings.HasPrefix(text, "△") {
					rendered.WriteString("△")
				}
				rendered.WriteString(text)
			default:
				rendered.WriteString(text)
			}
		}
	}
	if contentBlocks == 0 {
		return nil, errors.New("video script contains no renderable content")
	}
	if strings.TrimSpace(stringValue(unit["source_file_name"])) != "" &&
		!hasMeaningfulVideoTimeRangeReference(unit) {
		return nil, errors.New("video script contains no video timeline evidence")
	}
	unit["script_text"] = rendered.String()
	return json.Marshal(unit)
}

func hasMeaningfulVideoTimeRangeReference(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		if stringValue(typed["source_type"]) == "video_time_range" {
			if timeRange, ok := typed["time_range"].(map[string]any); ok &&
				integerValue(timeRange["end_ms"])-integerValue(timeRange["start_ms"]) >= 1000 {
				return true
			}
		}
		for _, child := range typed {
			if hasMeaningfulVideoTimeRangeReference(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasMeaningfulVideoTimeRangeReference(child) {
				return true
			}
		}
	}
	return false
}

// RewriteCharacterNames rewrites scene character lists and dialogue speakers,
// then regenerates script_text from the rewritten structured scenes.
func RewriteCharacterNames(
	payload json.RawMessage,
	resolve func(string) (string, error),
) (json.RawMessage, error) {
	unit, err := decodeObject(payload)
	if err != nil {
		return nil, err
	}
	scenes, _ := unit["scenes"].([]any)
	for sceneIndex, sceneValue := range scenes {
		scene, ok := sceneValue.(map[string]any)
		if !ok {
			continue
		}
		if values, ok := scene["characters"].([]any); ok {
			seen := map[string]struct{}{}
			normalized := make([]any, 0, len(values))
			for _, value := range values {
				name, resolveErr := resolve(strings.TrimSpace(stringValue(value)))
				if resolveErr != nil {
					return nil, fmt.Errorf("scene %d character: %w", sceneIndex+1, resolveErr)
				}
				if name == "" {
					continue
				}
				if _, exists := seen[name]; exists {
					continue
				}
				seen[name] = struct{}{}
				normalized = append(normalized, name)
			}
			scene["characters"] = normalized
		}
		blocks, _ := scene["blocks"].([]any)
		for blockIndex, blockValue := range blocks {
			block, ok := blockValue.(map[string]any)
			if !ok || stringValue(block["block_type"]) != "dialogue" {
				continue
			}
			name, resolveErr := resolve(strings.TrimSpace(stringValue(block["speaker"])))
			if resolveErr != nil {
				return nil, fmt.Errorf("scene %d dialogue %d speaker: %w", sceneIndex+1, blockIndex+1, resolveErr)
			}
			block["speaker"] = name
		}
	}
	encoded, err := json.Marshal(unit)
	if err != nil {
		return nil, err
	}
	return NormalizeUnitPayload(encoded)
}

func CharacterNames(payload json.RawMessage) ([]string, error) {
	unit, err := decodeObject(payload)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	result := make([]string, 0)
	appendName := func(raw string) {
		name := strings.TrimSpace(raw)
		if name == "" {
			return
		}
		if _, exists := seen[name]; exists {
			return
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	scenes, _ := unit["scenes"].([]any)
	for _, sceneValue := range scenes {
		scene, _ := sceneValue.(map[string]any)
		for _, name := range stringSlice(scene["characters"]) {
			appendName(name)
		}
		blocks, _ := scene["blocks"].([]any)
		for _, blockValue := range blocks {
			block, _ := blockValue.(map[string]any)
			if stringValue(block["block_type"]) == "dialogue" {
				appendName(stringValue(block["speaker"]))
			}
		}
	}
	return result, nil
}

func decodeObject(payload json.RawMessage) (map[string]any, error) {
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode video script payload: %w", err)
	}
	if result == nil {
		return nil, errors.New("video script payload is not an object")
	}
	return result, nil
}

func integerValue(value any) int {
	switch typed := value.(type) {
	case json.Number:
		result, _ := typed.Int64()
		return int(result)
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func stringSlice(value any) []string {
	values, _ := value.([]any)
	result := make([]string, 0, len(values))
	for _, item := range values {
		if text := strings.TrimSpace(stringValue(item)); text != "" {
			result = append(result, text)
		}
	}
	return result
}
