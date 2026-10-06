package sqlitestore

import (
	"context"
	"database/sql"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage/filestore"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPollStorageParity(t *testing.T) {
	ctx := context.Background()
	f, err := filestore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := testStore(t)
	poll := &domain.Poll{ID: "poll1", ChannelID: "c", Question: "Q", Options: []domain.PollOption{{ID: "o1", Text: "A"}, {ID: "o2", Text: "B"}}, Status: "open"}
	for _, st := range []interface {
		SavePoll(context.Context, *domain.Poll) error
		AddPollResponse(context.Context, string, domain.PollResponse) (domain.PollResponse, error)
		ListPollResponses(context.Context, string) ([]domain.PollResponse, error)
		GetPoll(context.Context, string) (*domain.Poll, error)
	}{f, s} {
		copy := *poll
		if err := st.SavePoll(ctx, &copy); err != nil {
			t.Fatal(err)
		}
		for _, choice := range []string{"o1", "o2"} {
			if _, err := st.AddPollResponse(ctx, poll.ID, domain.PollResponse{UserID: "u", OptionIDs: []string{choice}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	a, e := f.ListPollResponses(ctx, poll.ID)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.ListPollResponses(ctx, poll.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(a) != 1 || len(b) != 1 || a[0].Seq != 2 || b[0].Seq != 2 || a[0].OptionIDs[0] != "o2" || b[0].OptionIDs[0] != "o2" {
		t.Fatalf("file=%+v sqlite=%+v", a, b)
	}
	pa, e := f.GetPoll(ctx, poll.ID)
	if e != nil {
		t.Fatal(e)
	}
	pb, e := s.GetPoll(ctx, poll.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(pa, pb) {
		t.Fatalf("poll mismatch: %+v / %+v", pa, pb)
	}
}

func TestUpgradeV1ForPolls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUser(context.Background(), &domain.User{ID: "u", Name: "User"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`DROP TABLE polls; DROP TABLE poll_response_events; DROP INDEX message_poll; ALTER TABLE messages DROP COLUMN poll_id; PRAGMA user_version=1;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u, err := s.GetUser(context.Background(), "u")
	if err != nil || u.Name != "User" {
		t.Fatalf("user lost: %+v %v", u, err)
	}
	if err := s.SavePoll(context.Background(), &domain.Poll{ID: "p", ChannelID: "c", Question: "Q"}); err != nil {
		t.Fatal(err)
	}
}
