package agent

import (
	"regexp"
	"strconv"
	"strings"
)

type EpisodeMarker struct {
	EpisodeID  int    `json:"episode_id"`
	RuneOffset int    `json:"rune_offset"`
	Label      string `json:"label"`
}

var (
	headingEpisodeMarker = regexp.MustCompile(`^\s*第\s*([0-9一二三四五六七八九十百零〇两]+)\s*[章节集回话](?:\s+|[：:、.\-]|$).*$`)
	plainEpisodeMarker   = regexp.MustCompile(`^\s*([0-9]{1,4})\s*[\.、]?\s*$`)
)

// DetectEpisodeMarkers accepts only a complete, consecutive marker sequence
// beginning at 1. This avoids presenting numbered lists as source episodes.
func DetectEpisodeMarkers(text string) []EpisodeMarker {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) == 0 {
		return nil
	}
	headings := collectEpisodeMarkers(runes, headingEpisodeMarker)
	if consecutiveEpisodeMarkers(headings) {
		return headings
	}
	plain := collectEpisodeMarkers(runes, plainEpisodeMarker)
	if consecutiveEpisodeMarkers(plain) {
		return plain
	}
	return nil
}

func collectEpisodeMarkers(runes []rune, pattern *regexp.Regexp) []EpisodeMarker {
	markers := []EpisodeMarker{}
	lineStart := 0
	for lineStart <= len(runes) {
		lineEnd := lineStart
		for lineEnd < len(runes) && runes[lineEnd] != '\n' && runes[lineEnd] != '\r' {
			lineEnd++
		}
		line := string(runes[lineStart:lineEnd])
		match := pattern.FindStringSubmatch(line)
		if len(match) == 2 {
			if number := parseEpisodeMarkerNumber(match[1]); number > 0 {
				markers = append(markers, EpisodeMarker{EpisodeID: number, RuneOffset: lineStart, Label: strings.TrimSpace(line)})
			}
		}
		if lineEnd >= len(runes) {
			break
		}
		lineStart = lineEnd + 1
		if lineStart < len(runes) && runes[lineEnd] == '\r' && runes[lineStart] == '\n' {
			lineStart++
		}
	}
	return markers
}

func consecutiveEpisodeMarkers(markers []EpisodeMarker) bool {
	if len(markers) < 2 {
		return false
	}
	for index, marker := range markers {
		if marker.EpisodeID != index+1 {
			return false
		}
	}
	return true
}

func parseEpisodeMarkerNumber(value string) int {
	if number, err := strconv.Atoi(value); err == nil {
		return number
	}
	value = strings.ReplaceAll(value, "两", "二")
	value = strings.ReplaceAll(value, "〇", "零")
	digits := map[rune]int{'零': 0, '一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	total, section, number := 0, 0, 0
	for _, token := range []rune(value) {
		if digit, ok := digits[token]; ok {
			number = digit
			continue
		}
		switch token {
		case '十':
			if number == 0 {
				number = 1
			}
			section += number * 10
			number = 0
		case '百':
			if number == 0 {
				number = 1
			}
			section += number * 100
			number = 0
		default:
			return 0
		}
	}
	total += section + number
	return total
}
