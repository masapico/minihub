package filestore_test

import (
	"context"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage/filestore"
	"os"
	"path/filepath"
	"testing"
)

func TestPollRecoveryIgnoresIncompleteTail(t *testing.T) {
	root := t.TempDir()
	s, err := filestore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.SavePoll(ctx, &domain.Poll{ID: "p", ChannelID: "c", Question: "Q"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPollResponse(ctx, "p", domain.PollResponse{UserID: "u", OptionIDs: []string{"o1"}}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "polls", "p", "responses.jsonl")
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"seq":2`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := filestore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	answers, err := restarted.ListPollResponses(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) != 1 || answers[0].OptionIDs[0] != "o1" {
		t.Fatal(answers)
	}
	r, err := restarted.AddPollResponse(ctx, "p", domain.PollResponse{UserID: "u", OptionIDs: []string{"o2"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Seq != 2 {
		t.Fatal(r.Seq)
	}
}
