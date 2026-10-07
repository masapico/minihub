package service

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/masapico/minihub/internal/config"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
	"github.com/masapico/minihub/internal/storage/storetest"
)

func TestAIRechecksAuthorizationAndWithdrawal(t *testing.T) {
	for _, kind := range []string{"trigger", "parent", "archive", "membership"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			store, err := storetest.New(t, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range []domain.User{{ID: "admin", Name: "Admin", Role: domain.RoleAdmin, Enabled: true}, {ID: "alice", Name: "Alice", Role: domain.RoleUser, Enabled: true}} {
				if err := store.SaveUser(ctx, &u); err != nil {
					t.Fatal(err)
				}
			}
			s := New(store)
			if _, err := s.CreateChannel(ctx, "admin", domain.Channel{ID: "c", Name: "Private", Type: domain.ChannelPrivate, Members: []string{"alice"}}); err != nil {
				t.Fatal(err)
			}
			root, err := s.PostMessage(ctx, "alice", "c", "root")
			if err != nil {
				t.Fatal(err)
			}
			trigger, err := s.PostThreadMessage(ctx, "alice", "c", root.Seq, "trigger")
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				var err error
				switch kind {
				case "trigger":
					_, err = s.WithdrawMessage(ctx, "alice", "c", trigger.Seq)
				case "parent":
					_, err = s.WithdrawMessage(ctx, "alice", "c", root.Seq)
				case "archive":
					err = s.ArchiveChannel(ctx, "admin", "c")
				case "membership":
					_, err = s.ChangeChannelMembers(ctx, "admin", "c", ChannelMemberChanges{RemoveUsers: []string{"alice"}})
				}
				if err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"text":"late reply"}`)
			}))
			defer server.Close()
			account := config.AIAccount{ID: "helper", Name: "AI", URL: server.URL}
			if err := s.StartAI(ctx, []config.AIAccount{account}, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
				t.Fatal(err)
			}
			defer s.CloseAI()
			// Run synchronously so a missing reply cannot be explained by an
			// unfinished worker or shutdown canceling the response early.
			s.runAI(s.ai, aiJob{account: s.ai.accounts[0], author: "alice", channel: "c", id: "request", trigger: trigger})
			after := int64(0)
			page, err := store.GetMessages(ctx, "c", storage.MessageQuery{ThreadRootSeq: root.Seq, AfterSeq: &after, Limit: 100})
			if err != nil || len(page.Messages) != 1 || page.Messages[0].AI != nil {
				t.Fatalf("reply persisted after %s: %#v %v", kind, page, err)
			}
		})
	}
}
