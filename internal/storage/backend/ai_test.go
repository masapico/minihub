package backend

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage/filestore"
)

func TestAIMetadataMigrationRestoreAndRetention(t *testing.T) {
	root, file := fixture(t)
	ctx := context.Background()
	meta := &domain.AIMessage{ID: "helper", Name: "社内AI", RequestID: "request1", TriggerMessageID: "trigger1", RequestedBy: "u", Kind: "answer"}
	m, err := file.AddMessage(ctx, "c", domain.Message{UserID: "ai_helper", Text: "AI回答 @ai:helper @u", ThreadRootSeq: 1, MentionUserIDs: []string{}, AI: meta, Timestamp: time.Date(2026, 1, 8, 0, 0, 0, 0, time.UTC)})
	must(t, err)
	restarted, err := filestore.New(root)
	must(t, err)
	got, err := restarted.GetMessage(ctx, "c", m.Seq)
	must(t, err)
	if !reflect.DeepEqual(got.AI, meta) {
		t.Fatal("file attribution lost")
	}
	before := sourceHashes(t, root)
	_, err = Migrate(ctx, root, false)
	must(t, err)
	if !reflect.DeepEqual(before, sourceHashes(t, root)) {
		t.Fatal("migration changed source")
	}
	backup := t.TempDir()
	must(t, os.CopyFS(backup, os.DirFS(root)))
	h, err := Open(backup, "sqlite")
	must(t, err)
	got, err = h.GetMessage(ctx, "c", m.Seq)
	must(t, err)
	if !reflect.DeepEqual(got.AI, meta) || got.UserID != "ai_helper" {
		t.Fatal("migration or restore lost AI attribution")
	}
	must(t, h.Close())
	_, err = Prune(ctx, backup, "sqlite", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), true)
	must(t, err)
	h, err = Open(backup, "sqlite")
	must(t, err)
	defer h.Close()
	if _, err = h.GetMessage(ctx, "c", m.Seq); !os.IsNotExist(err) {
		t.Fatalf("AI thread reply retained without root: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "channels", "c", "meta.json")); err != nil {
		t.Fatal(err)
	}
}
