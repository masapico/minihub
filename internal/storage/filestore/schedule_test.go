package filestore_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage/filestore"
)

func TestScheduleResponsesRecoverLatestAndIgnoreIncompleteTail(t *testing.T) {
	root := t.TempDir()
	store, err := filestore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	schedule := &domain.Schedule{ID: "plan1", ChannelID: "general", Status: domain.ScheduleOpen, CreatedAt: time.Now()}
	if err := store.SaveSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	for _, choice := range []domain.ScheduleChoice{domain.ScheduleNo, domain.ScheduleYes} {
		if _, err := store.AddScheduleResponse(context.Background(), schedule.ID, domain.ScheduleResponse{UserID: "alice", Choices: map[string]domain.ScheduleChoice{"c1": choice}}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "schedules", "plan1", "responses.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(`{"seq":3`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	restarted, err := filestore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	responses, err := restarted.ListScheduleResponses(context.Background(), "plan1")
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != 1 || responses[0].Choices["c1"] != domain.ScheduleYes {
		t.Fatalf("unexpected responses: %#v", responses)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("response log was unexpectedly removed")
	}
}

func TestConcurrentScheduleResponsesHaveSequencesAndLatestState(t *testing.T) {
	store, err := filestore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const count = 40
	var wait sync.WaitGroup
	errors := make(chan error, count)
	for i := 0; i < count; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			_, err := store.AddScheduleResponse(context.Background(), "plan", domain.ScheduleResponse{UserID: fmt.Sprintf("u%03d", i), Choices: map[string]domain.ScheduleChoice{"c1": domain.ScheduleYes}})
			errors <- err
		}(i)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	responses, err := store.ListScheduleResponses(context.Background(), "plan")
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != count {
		t.Fatalf("responses=%d want=%d", len(responses), count)
	}
}

func TestScheduleAnnouncementMessageIsIdempotent(t *testing.T) {
	store, err := filestore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	message := domain.Message{UserID: "alice", Text: "schedule", ScheduleRef: &domain.ScheduleReference{ID: "plan", Event: "published"}}
	first, err := store.AddMessage(context.Background(), "general", message)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AddMessage(context.Background(), "general", message)
	if err != nil {
		t.Fatal(err)
	}
	if first.Seq != second.Seq || first.ID != second.ID {
		t.Fatalf("duplicate announcement: first=%#v second=%#v", first, second)
	}
}
