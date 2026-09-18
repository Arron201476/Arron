package main

import (
	"strings"
	"testing"

	"novel2script-agent/backend/internal/agent"
	"novel2script-agent/backend/internal/mainagent"
)

func TestResolveStartRunSourceModeOverridesWrongNonNovelWhenAttachmentIsNovelSource(t *testing.T) {
	source := "生成剧本\n\n【附件：测试用书.txt】\n1\n" + strings.Repeat("大爷每天不到5点就晨练开嗓，吵得整个小区睡不着。\n谁敢骂他，他就插着心脏病下要人赔钱，警察也拿他没办法。\n我说：“5点晨练算什么，1点敲门怕不怕？”\n我能陪他玩一年。\n\n", 12)

	mode := resolveStartRunSourceMode(
		mainagent.MessageRequest{SourceMode: agent.SourceModeAuto},
		mainagent.Decision{SourceMode: agent.SourceModeNonNovel},
		source,
	)

	if mode != agent.SourceModeNovel {
		t.Fatalf("expected strong novel source evidence to force novel, got %s", mode)
	}
}

func TestResolveStartRunSourceModeKeepsNonNovelForShortMaterialIdea(t *testing.T) {
	source := "生成剧本\n\n校园恋爱短剧灵感：女主暗恋篮球队长，想做 8 集，每集 1.5 分钟。"

	mode := resolveStartRunSourceMode(
		mainagent.MessageRequest{SourceMode: agent.SourceModeAuto},
		mainagent.Decision{SourceMode: agent.SourceModeNonNovel},
		source,
	)

	if mode != agent.SourceModeNonNovel {
		t.Fatalf("expected short material idea to stay non_novel, got %s", mode)
	}
}
