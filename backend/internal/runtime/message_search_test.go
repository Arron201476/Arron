package runtime

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSearchMessagesScopesAndBoundsAuthoritativeHistory(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	first, err := store.CreateProject(ctx, "第一作品")
	if err != nil {
		t.Fatalf("CreateProject(first) error = %v", err)
	}
	second, err := store.CreateProject(ctx, "第二作品")
	if err != nil {
		t.Fatalf("CreateProject(second) error = %v", err)
	}
	for _, content := range []string{"主角暂定沈砚", "第二轮无关内容", "最终确认主角是沈砚"} {
		if _, err := store.CreateUserMessage(ctx, first.PrimaryConversationID, content); err != nil {
			t.Fatalf("CreateUserMessage(first) error = %v", err)
		}
	}
	if _, err := store.CreateUserMessage(ctx, second.PrimaryConversationID, "另一个作品也提到沈砚"); err != nil {
		t.Fatalf("CreateUserMessage(second) error = %v", err)
	}

	items, err := store.SearchMessages(ctx, first.PrimaryConversationID, "沈砚", 1)
	if err != nil {
		t.Fatalf("SearchMessages() error = %v", err)
	}
	if len(items) != 1 || items[0].Content != "最终确认主角是沈砚" || items[0].ProjectID != first.ProjectID {
		t.Fatalf("SearchMessages() = %+v", items)
	}
}
