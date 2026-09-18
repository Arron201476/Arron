package videoscript

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeUnitPayloadRebuildsScriptTextFromScenes(t *testing.T) {
	payload := json.RawMessage(`{
		"episode_no":7,"episode_order":7,"script_text":"第7集",
		"scenes":[{"heading":"场7-1 村口 日 外","characters":["李虹燕"],"blocks":[
			{"block_type":"action","text":"她走进村口。"},
			{"block_type":"dialogue","speaker":"李虹燕","delivery":"坚定","text":"开始吧。"}
		]}]
	}`)
	got, err := NormalizeUnitPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ScriptText string `json:"script_text"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"第7集", "场7-1 村口 日 外", "△她走进村口。", "李虹燕（坚定）：开始吧。"} {
		if !strings.Contains(decoded.ScriptText, want) {
			t.Fatalf("script_text = %q, missing %q", decoded.ScriptText, want)
		}
	}
}

func TestNormalizeUnitPayloadRejectsEmptyShell(t *testing.T) {
	_, err := NormalizeUnitPayload(json.RawMessage(`{
		"episode_no":7,"episode_order":7,"script_text":"第7集",
		"scenes":[{"heading":"场7-1 村口 日 外","characters":[],"blocks":[]}]
	}`))
	if err == nil || !strings.Contains(err.Error(), "no content blocks") {
		t.Fatalf("error = %v", err)
	}
}

func TestNormalizeUnitPayloadRejectsUngroundedVideoFallback(t *testing.T) {
	_, err := NormalizeUnitPayload(json.RawMessage(`{
		"episode_no":1,"episode_order":1,"source_file_name":"第1集.mp4",
		"source_refs":[{"source_type":"user_message","message_id":"msg_1"}],
		"scenes":[{"heading":"场1-1 视频内容未取得 时间未确认 内外","characters":[],"blocks":[
			{"block_type":"action","text":"无法读取视频。","source_refs":[{"source_type":"user_message","message_id":"msg_1"}]}
		]}]
	}`))
	if err == nil || !strings.Contains(err.Error(), "no video timeline evidence") {
		t.Fatalf("error = %v", err)
	}
}

func TestNormalizeUnitPayloadRejectsFakeOneMillisecondEvidence(t *testing.T) {
	_, err := NormalizeUnitPayload(json.RawMessage(`{
		"episode_no":1,"episode_order":1,"source_file_name":"第1集.mp4",
		"source_refs":[{"source_type":"video_time_range","time_range":{"start_ms":0,"end_ms":1}}],
		"scenes":[{"heading":"场1-1 视频内容待确认 未知 内外","characters":[],"blocks":[
			{"block_type":"action","text":"完整视频分析接口未返回结果。","source_refs":[{"source_type":"video_time_range","time_range":{"start_ms":0,"end_ms":1}}]}
		]}]
	}`))
	if err == nil || !strings.Contains(err.Error(), "no video timeline evidence") {
		t.Fatalf("error = %v", err)
	}
}

func TestRewriteCharacterNamesUpdatesStructuredAndRenderedContent(t *testing.T) {
	payload := json.RawMessage(`{
		"episode_no":2,"episode_order":2,"script_text":"stale",
		"scenes":[{"heading":"场2-1 店内 夜 内","characters":["虹燕"],"blocks":[
			{"block_type":"dialogue","speaker":"虹燕","text":"我回来了。"}
		]}]
	}`)
	got, err := RewriteCharacterNames(payload, func(name string) (string, error) {
		if name == "虹燕" {
			return "李虹燕", nil
		}
		return name, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), `"虹燕"`) || !strings.Contains(string(got), "李虹燕：我回来了。") {
		t.Fatalf("payload = %s", got)
	}
}
