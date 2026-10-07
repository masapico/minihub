package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/masapico/minihub/internal/config"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
	"github.com/masapico/minihub/internal/storage/storetest"
)

func TestAIPayloadRolesAndSystemPrompt(t *testing.T) {
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
	root, err := s.PostMessage(ctx, "alice", "c", "root **bold**")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []domain.Message{
		{UserID: "ai_helper", Text: "own answer", AI: &domain.AIMessage{ID: "helper", Name: "Helper", Kind: "answer"}},
		{UserID: "ai_other", Text: "other answer", AI: &domain.AIMessage{ID: "other", Name: "Other AI", Kind: "answer"}},
		{UserID: "ai_helper", Text: "error to omit", AI: &domain.AIMessage{ID: "helper", Name: "Helper", Kind: "error"}},
		{UserID: "ai_other", Text: "other error to omit", AI: &domain.AIMessage{ID: "other", Name: "Other AI", Kind: "error"}},
	} {
		m.ThreadRootSeq = root.Seq
		if _, err := store.AddMessage(ctx, "c", m); err != nil {
			t.Fatal(err)
		}
	}
	withdrawn, err := s.PostThreadMessage(ctx, "alice", "c", root.Seq, "withdrawn to omit")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WithdrawMessage(ctx, "alice", "c", withdrawn.Seq); err != nil {
		t.Fatal(err)
	}
	trigger, err := s.PostThreadMessage(ctx, "alice", "c", root.Seq, "@ai:helper next")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostThreadMessage(ctx, "alice", "c", root.Seq, "later to omit"); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"", " \t\n", "簡潔に回答してください。"} {
		t.Run("prompt="+prompt, func(t *testing.T) {
			job := aiJob{author: "alice", channel: "c", trigger: trigger, account: aiEndpoint{AIAccount: config.AIAccount{ID: "helper", Model: "local-model", SystemPrompt: prompt}}}
			b, err := s.aiPayload(ctx, job)
			if err != nil {
				t.Fatal(err)
			}
			var got aiRequest
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			want := []aiContextMessage{
				{Role: "user", Content: "Alice (alice):\n" + root.Text},
				{Role: "assistant", Content: "own answer"},
				{Role: "user", Content: "Other AI (ai_other):\nother answer"},
				{Role: "user", Content: "Alice (alice):\n" + trigger.Text},
			}
			if strings.TrimSpace(prompt) != "" {
				want = append([]aiContextMessage{{Role: "system", Content: prompt}}, want...)
			}
			if got.Model != "local-model" || got.Stream || !reflect.DeepEqual(got.Messages, want) {
				t.Fatalf("payload: %s", b)
			}
		})
	}
	job := aiJob{author: "alice", channel: "c", trigger: trigger, account: aiEndpoint{AIAccount: config.AIAccount{ID: "helper", Model: "local-model", SystemPrompt: strings.Repeat("x", aiMaxContextBytes)}}}
	if _, err := s.aiPayload(ctx, job); !errors.Is(err, errAIContextLimit) {
		t.Fatalf("system prompt excluded from request limit: %v", err)
	}
}

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
				io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"late reply"}}]}`)
			}))
			defer server.Close()
			account := config.AIAccount{ID: "helper", Name: "AI", URL: server.URL, Model: "test-model"}
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
