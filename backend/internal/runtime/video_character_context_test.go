package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestEnrichVideoCharacterCursorUsesPriorConfirmedSpellings(t *testing.T) {
	cursor := json.RawMessage(`{"video":{"episode_order":3}}`)
	got, err := enrichVideoCharacterCursor(cursor, []priorVideoCharacterUnit{
		{Order: 2, Payload: json.RawMessage(`{"plot_summary":"第二集结尾","scenes":[{"characters":["李虹燕","新角色"],"blocks":[{"speaker":"未知人物"}]}]}`)},
		{Order: 1, Payload: json.RawMessage(`{"plot_summary":"第一集结尾","scenes":[{"characters":["李虹燕","苏晓棠"],"blocks":[{"speaker":"李虹燕"},{"speaker":"旁白"}]}]}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Video struct {
			KnownCharacters    []string                      `json:"known_characters"`
			CharacterRegistry  []videoCharacterRegistryEntry `json:"character_registry"`
			PreviousEpisodeEnd string                        `json:"previous_episode_end"`
		} `json:"video"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	want := []string{"李虹燕", "苏晓棠", "新角色"}
	if len(decoded.Video.KnownCharacters) != len(want) {
		t.Fatalf("known characters = %#v", decoded.Video.KnownCharacters)
	}
	for index := range want {
		if decoded.Video.KnownCharacters[index] != want[index] {
			t.Fatalf("known characters = %#v", decoded.Video.KnownCharacters)
		}
	}
	if decoded.Video.PreviousEpisodeEnd != "第二集结尾" {
		t.Fatalf("previous episode end = %q", decoded.Video.PreviousEpisodeEnd)
	}
	if len(decoded.Video.CharacterRegistry) != 3 || decoded.Video.CharacterRegistry[0].CanonicalName != "李虹燕" {
		t.Fatalf("character registry = %#v", decoded.Video.CharacterRegistry)
	}
}

func TestVideoCharacterRegistryPromotesSafeFullNameAlias(t *testing.T) {
	registry := registerVideoCharacter(nil, "虹燕", 1)
	registry = registerVideoCharacter(registry, "李虹燕", 2)
	if len(registry) != 1 || registry[0].CanonicalName != "李虹燕" {
		t.Fatalf("registry = %#v", registry)
	}
	if len(registry[0].Aliases) != 2 {
		t.Fatalf("aliases = %#v", registry[0].Aliases)
	}
}

func TestEnrichVideoCharacterCursorBoundsLongSeriesContext(t *testing.T) {
	units := make([]priorVideoCharacterUnit, 0, maxVideoCharacterRegistryEntries+50)
	for episode := 1; episode <= maxVideoCharacterRegistryEntries+50; episode++ {
		units = append(units, priorVideoCharacterUnit{
			Order: episode,
			Payload: json.RawMessage(fmt.Sprintf(
				`{"plot_summary":"%s-第%d集结尾","scenes":[{"characters":["角色%03d"]}]}`,
				strings.Repeat("长", maxVideoPreviousEndRunes+100),
				episode,
				episode,
			)),
		})
	}
	got, err := enrichVideoCharacterCursor(
		json.RawMessage(`{"video":{"episode_order":251}}`),
		units,
	)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Video struct {
			KnownCharacters    []string                      `json:"known_characters"`
			CharacterRegistry  []videoCharacterRegistryEntry `json:"character_registry"`
			PreviousEpisodeEnd string                        `json:"previous_episode_end"`
		} `json:"video"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Video.KnownCharacters) != maxVideoCharacterRegistryEntries ||
		len(decoded.Video.CharacterRegistry) != maxVideoCharacterRegistryEntries {
		t.Fatalf("bounded character counts = %d/%d", len(decoded.Video.KnownCharacters), len(decoded.Video.CharacterRegistry))
	}
	if len([]rune(decoded.Video.PreviousEpisodeEnd)) != maxVideoPreviousEndRunes {
		t.Fatalf("previous episode end runes = %d", len([]rune(decoded.Video.PreviousEpisodeEnd)))
	}
}

func TestNormalizeVideoScriptCharacterContinuityRewritesAliasAndScriptMirror(t *testing.T) {
	cursor := json.RawMessage(`{"video":{"episode_order":2,"character_registry":[{"canonical_name":"李虹燕","aliases":["李虹燕","虹燕"],"first_seen_episode":1,"last_seen_episode":1,"mention_count":2}]}}`)
	payload := json.RawMessage(`{"episode_no":2,"episode_order":2,"script_text":"旧镜像","scenes":[{"heading":"场2-1 村口 日 外","characters":["虹燕"],"blocks":[{"block_type":"dialogue","speaker":"虹燕","text":"开始采收。"}]}]}`)
	got, err := normalizeVideoScriptCharacterContinuity(cursor, payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), `"虹燕"`) || !strings.Contains(string(got), "李虹燕：开始采收。") {
		t.Fatalf("payload = %s", got)
	}
}

func TestNormalizeVideoScriptCharacterContinuityDoesNotBlockSimilarName(t *testing.T) {
	cursor := json.RawMessage(`{"video":{"episode_order":2,"character_registry":[{"canonical_name":"李虹燕","aliases":["李虹燕"],"first_seen_episode":1,"last_seen_episode":1,"mention_count":1}]}}`)
	payload := json.RawMessage(`{"episode_no":2,"episode_order":2,"script_text":"旧镜像","scenes":[{"heading":"场2-1 村口 日 外","characters":["李红燕"],"blocks":[{"block_type":"dialogue","speaker":"李红燕","text":"开始采收。"}]}]}`)
	got, err := normalizeVideoScriptCharacterContinuity(cursor, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "李红燕：开始采收。") {
		t.Fatalf("payload = %s", got)
	}
}

func TestNormalizeVideoScriptCharacterContinuityAllowsDistinctRolesInSameEpisode(t *testing.T) {
	cursor := json.RawMessage(`{"video":{"episode_order":1}}`)
	payload := json.RawMessage(`{"episode_no":1,"episode_order":1,"script_text":"旧镜像","scenes":[{"heading":"场1-1 饭馆 日 内","characters":["食客甲","食客乙"],"blocks":[{"block_type":"dialogue","speaker":"食客甲","text":"一份面。"},{"block_type":"dialogue","speaker":"食客乙","text":"我也要。"}]}]}`)
	got, err := normalizeVideoScriptCharacterContinuity(cursor, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "食客甲：一份面。") || !strings.Contains(string(got), "食客乙：我也要。") {
		t.Fatalf("payload = %s", got)
	}
}

func TestNormalizeVideoScriptCharacterContinuityAllowsNewEnumeratedRoleAcrossEpisodes(t *testing.T) {
	cursor := json.RawMessage(`{"video":{"episode_order":2,"character_registry":[{"canonical_name":"食客甲","aliases":["食客甲"],"first_seen_episode":1,"last_seen_episode":1,"mention_count":1}]}}`)
	payload := json.RawMessage(`{"episode_no":2,"episode_order":2,"script_text":"旧镜像","scenes":[{"heading":"场2-1 饭馆 日 内","characters":["食客乙"],"blocks":[{"block_type":"dialogue","speaker":"食客乙","text":"结账。"}]}]}`)
	got, err := normalizeVideoScriptCharacterContinuity(cursor, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "食客乙：结账。") {
		t.Fatalf("payload = %s", got)
	}
}
