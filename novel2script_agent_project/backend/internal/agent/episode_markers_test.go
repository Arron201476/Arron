package agent

import "testing"

func TestDetectEpisodeMarkersAcceptsConsecutiveHeadings(t *testing.T) {
	markers := DetectEpisodeMarkers("序言\n第1章 开始\n正文\n第2章 继续\n正文\n第3章 结束")
	if len(markers) != 3 || markers[0].EpisodeID != 1 || markers[2].EpisodeID != 3 || markers[1].RuneOffset <= markers[0].RuneOffset {
		t.Fatalf("unexpected markers: %#v", markers)
	}
}

func TestDetectEpisodeMarkersAcceptsStandaloneEpisodeNumbers(t *testing.T) {
	markers := DetectEpisodeMarkers("1\n第一集正文\n2\n第二集正文\n3\n第三集正文")
	if len(markers) != 3 {
		t.Fatalf("expected three standalone markers, got %#v", markers)
	}
}

func TestDetectEpisodeMarkersRejectsSparseOrNonEpisodeNumbers(t *testing.T) {
	for _, text := range []string{
		"第1章 开始\n正文\n第3章 跳集",
		"素材有 1 个主角和 2 个配角",
		"1. 这是同一行的条目\n2. 不是独立数字行",
	} {
		if markers := DetectEpisodeMarkers(text); len(markers) != 0 {
			t.Fatalf("should reject %q, got %#v", text, markers)
		}
	}
}

func TestDetectEpisodeMarkersSupportsChineseNumbers(t *testing.T) {
	text := "第一回 开始\n正文\n第二回 继续\n正文\n第三回 结束"
	markers := DetectEpisodeMarkers(text)
	if len(markers) != 3 || markers[2].EpisodeID != 3 {
		t.Fatalf("unexpected Chinese markers: %#v", markers)
	}
}
