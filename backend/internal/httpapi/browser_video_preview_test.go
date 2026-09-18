package httpapi

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestBrowserVideoPreviewPathUsesAssetFingerprint(t *testing.T) {
	t.Setenv("CONTENT_AGENT_VIDEO_PREVIEW_CACHE_DIR", t.TempDir())
	modifiedAt := time.Unix(1_700_000_000, 123)
	first := browserVideoPreviewPath("ast_video", 100, modifiedAt)
	second := browserVideoPreviewPath("ast_video", 101, modifiedAt)
	if first == second {
		t.Fatal("preview cache path did not change with the source fingerprint")
	}
	if filepath.Ext(first) != ".mp4" || filepath.Dir(first) != os.Getenv("CONTENT_AGENT_VIDEO_PREVIEW_CACHE_DIR") {
		t.Fatalf("unexpected preview cache path %q", first)
	}
}

func TestBrowserVideoPreviewArgsProduceH264MP4(t *testing.T) {
	arguments := browserVideoPreviewArgs("input.mp4", "output.mp4")
	want := []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart"}
	for index := 0; index < len(want); index += 2 {
		found := false
		for argumentIndex := 0; argumentIndex+1 < len(arguments); argumentIndex++ {
			if reflect.DeepEqual(arguments[argumentIndex:argumentIndex+2], want[index:index+2]) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("preview arguments missing %q %q: %v", want[index], want[index+1], arguments)
		}
	}
}
