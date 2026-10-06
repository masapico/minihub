package backend

import (
	"context"
	"github.com/masapico/minihub/internal/domain"
	"os"
	"reflect"
	"testing"
)

func TestPollMigrationAndRestore(t *testing.T) {
	root, source := fixture(t)
	ctx := context.Background()
	poll := &domain.Poll{ID: "poll1", ChannelID: "c", Question: "Choice?", Options: []domain.PollOption{{ID: "o1", Text: "A"}, {ID: "o2", Text: "B"}}, Status: "open", CreatedBy: "u", Revision: 2}
	must(t, source.SavePoll(ctx, poll))
	_, err := source.AddMessage(ctx, "c", domain.Message{UserID: "u", Text: "投票を開始しました", PollRef: &domain.PollReference{ID: poll.ID}})
	must(t, err)
	first, err := source.AddPollResponse(ctx, poll.ID, domain.PollResponse{UserID: "v", OptionIDs: []string{"o1"}})
	must(t, err)
	second, err := source.AddPollResponse(ctx, poll.ID, domain.PollResponse{UserID: "v", OptionIDs: []string{"o2"}})
	must(t, err)
	if second.Seq != first.Seq+1 {
		t.Fatal("response sequence")
	}
	before := sourceHashes(t, root)
	report, err := Migrate(ctx, root, false)
	must(t, err)
	if report.Records["poll_response_events"].Count != 2 {
		t.Fatal(report)
	}
	after := sourceHashes(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("source data changed")
	}
	restored, err := Open(root, "sqlite")
	must(t, err)
	p, err := restored.GetPoll(ctx, poll.ID)
	must(t, err)
	if p.Question != poll.Question {
		t.Fatal(p)
	}
	answers, err := restored.ListPollResponses(ctx, poll.ID)
	must(t, err)
	if len(answers) != 1 || answers[0].OptionIDs[0] != "o2" {
		t.Fatal(answers)
	}
	msg, err := restored.GetMessage(ctx, "c", 7)
	must(t, err)
	if msg.PollRef == nil || msg.PollRef.ID != poll.ID {
		t.Fatal(msg)
	}
	must(t, restored.Close())
	backup := t.TempDir()
	must(t, os.CopyFS(backup, os.DirFS(root)))
	copyStore, err := Open(backup, "sqlite")
	must(t, err)
	defer copyStore.Close()
	_, err = copyStore.GetPoll(ctx, poll.ID)
	must(t, err)
}
