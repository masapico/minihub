package service_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/masapico/minihub/internal/config"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/service"
	"github.com/masapico/minihub/internal/storage"
)

func aiService(t *testing.T, handler http.HandlerFunc, timeout string) *service.Service {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Keep payload decoding available to handlers while allowing the HTTP
		// server to observe client cancellation during a blocked response.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	svc := setup(t)
	if err := svc.StartAI(context.Background(), []config.AIAccount{{ID: "helper", Name: "社内AI", URL: server.URL, Timeout: timeout}}, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.CloseAI)
	if _, err := svc.CreateChannel(context.Background(), "admin", domain.Channel{ID: "general", Name: "全社", Type: domain.ChannelPrivate, Members: []string{"alice", "bob"}}); err != nil {
		t.Fatal(err)
	}
	return svc
}

func awaitAIReply(t *testing.T, svc *service.Service, root int64) domain.Message {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		after := int64(0)
		page, err := svc.GetThreadMessages(context.Background(), "alice", "general", root, storage.MessageQuery{AfterSeq: &after, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page.Messages {
			if m.AI != nil {
				return m
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("AI reply did not arrive")
	return domain.Message{}
}

func TestAIMainPostAndThreadContext(t *testing.T) {
	requests := make(chan map[string]any, 4)
	var count atomic.Int32
	svc := aiService(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.Header.Get("X-MiniHub-Request-ID") != body["requestId"] || r.Method != "POST" {
			t.Error("missing request identity")
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"text":"回答 @ai:helper @alice"}`)
	}, "")
	ctx := context.Background()
	root, err := svc.PostMessage(ctx, "alice", "general", "@ai:helper @ai:helper 質問")
	if err != nil {
		t.Fatal(err)
	}
	answer := awaitAIReply(t, svc, root.Seq)
	body := <-requests
	if body["version"] != float64(1) || len(body["messages"].([]any)) != 1 || body["channel"].(map[string]any)["type"] != "private" {
		t.Fatalf("payload: %#v", body)
	}
	if answer.UserID != "ai_helper" || answer.AI.Name != "社内AI" || answer.AI.TriggerMessageID != root.ID || answer.AI.Kind != "answer" || len(answer.MentionUserIDs) != 0 {
		t.Fatalf("answer: %#v", answer)
	}
	if _, err := svc.WithdrawMessage(ctx, "alice", "general", answer.Seq); err == nil {
		t.Fatal("requester must not impersonate AI author")
	}
	deleted, err := svc.PostThreadMessage(ctx, "bob", "general", root.Seq, "送信しない本文")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.WithdrawMessage(ctx, "bob", "general", deleted.Seq); err != nil {
		t.Fatal(err)
	}
	trigger, err := svc.PostThreadMessage(ctx, "alice", "general", root.Seq, "@ai:helper 続き")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case body = <-requests:
	case <-time.After(8 * time.Second):
		t.Fatal("no thread request")
	}
	items := body["messages"].([]any)
	if len(items) != 3 || items[1].(map[string]any)["author"].(map[string]any)["kind"] != "ai" || items[2].(map[string]any)["id"] != trigger.ID {
		t.Fatalf("thread context: %#v", items)
	}
	if count.Load() != 2 {
		t.Fatalf("duplicate or recursive requests: %d", count.Load())
	}
	mentions, err := svc.ListMentions(ctx, "alice", "", 100)
	if err != nil || len(mentions.Mentions) != 0 {
		t.Fatalf("AI output notified user: %#v %v", mentions, err)
	}
}

func TestAIResponseFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType string
		status                  int
	}{
		{"http_error", "secret upstream error", "text/plain", 500},
		{"redirect", "", "application/json", 302},
		{"invalid_json", "{", "application/json", 200},
		{"missing_text", "{}", "application/json", 200},
		{"wrong_content_type", `{"text":"answer"}`, "text/plain", 200},
		{"long_answer", `{"text":"` + strings.Repeat("a", service.MaxMessageBytes+1) + `"}`, "application/json", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			svc := aiService(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("Location", "/again")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}, "")
			root, err := svc.PostMessage(context.Background(), "alice", "general", "@ai:helper test")
			if err != nil {
				t.Fatal(err)
			}
			answer := awaitAIReply(t, svc, root.Seq)
			if answer.AI.Kind != "error" || strings.Contains(answer.Text, "secret") || calls.Load() != 1 {
				t.Fatalf("failure handling: %#v calls=%d", answer, calls.Load())
			}
		})
	}
}

func TestAITimeoutAndEmptyResponse(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		svc := aiService(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, "250ms")
		root, err := svc.PostMessage(context.Background(), "alice", "general", "@ai:helper test")
		if err != nil {
			t.Fatal(err)
		}
		if m := awaitAIReply(t, svc, root.Seq); m.AI.Kind != "error" {
			t.Fatal(m)
		}
	})
	for _, status := range []int{200, 204} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			svc := aiService(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status == 200 {
					io.WriteString(w, `{"text":""}`)
				}
			}, "")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			root, err := svc.PostMessage(ctx, "alice", "general", "@ai:helper test")
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond)
			svc.CloseAI()
			after := int64(0)
			page, err := svc.GetThreadMessages(context.Background(), "alice", "general", root.Seq, storage.MessageQuery{AfterSeq: &after, Limit: 100})
			if err != nil || len(page.Messages) != 0 {
				t.Fatalf("empty response posted: %#v %v", page, err)
			}
		})
	}
}

func TestAIContextLimit(t *testing.T) {
	var calls atomic.Int32
	svc := aiService(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }, "")
	ctx := context.Background()
	root, err := svc.PostMessage(ctx, "alice", "general", "長い会話")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 65; i++ {
		if _, err := svc.PostThreadMessage(ctx, "alice", "general", root.Seq, strings.Repeat("x", service.MaxMessageBytes)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.PostThreadMessage(ctx, "alice", "general", root.Seq, "@ai:helper 全文を処理"); err != nil {
		t.Fatal(err)
	}
	answer := awaitAIReply(t, svc, root.Seq)
	if answer.AI.Kind != "error" || calls.Load() != 0 || !strings.Contains(answer.Text, "上限") {
		t.Fatalf("limit: %#v calls=%d", answer, calls.Load())
	}
}

func TestAIQueueBoundAndCancellation(t *testing.T) {
	started := make(chan struct{}, 4)
	svc := aiService(t, func(w http.ResponseWriter, r *http.Request) { started <- struct{}{}; <-r.Context().Done() }, "")
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if _, err := svc.PostMessage(ctx, "alice", "general", "@ai:helper busy"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(8 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	for i := 0; i < 32; i++ {
		if _, err := svc.PostMessage(ctx, "alice", "general", "@ai:helper queued"); err != nil {
			t.Fatal(err)
		}
	}
	root, err := svc.PostMessage(ctx, "alice", "general", "@ai:helper overflow")
	if err != nil {
		t.Fatal(err)
	}
	if m := awaitAIReply(t, svc, root.Seq); m.AI.Kind != "error" || !strings.Contains(m.Text, "混み合") {
		t.Fatal(m)
	}
	svc.CloseAI()
}

func TestAIWithdrawalWhileRunning(t *testing.T) {
	started, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	svc := aiService(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"text":"late answer"}`)
		close(returned)
	}, "")
	ctx := context.Background()
	root, err := svc.PostMessage(ctx, "alice", "general", "@ai:helper test")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(8 * time.Second):
		t.Fatal("not started")
	}
	if _, err := svc.WithdrawMessage(ctx, "alice", "general", root.Seq); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-returned:
	case <-time.After(8 * time.Second):
		t.Fatal("not returned")
	}
	svc.CloseAI()
	if _, err := svc.RestoreMessage(ctx, "alice", "general", root.Seq); err != nil {
		t.Fatal(err)
	}
	after := int64(0)
	page, err := svc.GetThreadMessages(ctx, "alice", "general", root.Seq, storage.MessageQuery{AfterSeq: &after, Limit: 100})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("withdrawn request received answer: %#v %v", page, err)
	}
}

func TestAIMultipleAccountsAndBearer(t *testing.T) {
	requests := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		id := body["aiAccount"].(map[string]any)["id"].(string)
		wantAuth := map[string]string{"helper": "Bearer direct-test-token", "reviewer": "Bearer fake-test-token", "observer": ""}
		if r.Header.Get("Authorization") != wantAuth[id] {
			t.Errorf("incorrect Bearer authentication for %s", id)
		}
		requests <- id
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"text":"done"}`)
	}))
	defer server.Close()
	t.Setenv("MINIHUB_AI_MULTI_TEST_TOKEN", "fake-test-token")
	svc := setup(t)
	disabled := false
	if err := svc.StartAI(context.Background(), []config.AIAccount{
		{ID: "helper", Name: "Helper", URL: server.URL, Token: "direct-test-token"},
		{ID: "reviewer", Name: "Reviewer", URL: server.URL, TokenEnv: "MINIHUB_AI_MULTI_TEST_TOKEN"},
		{ID: "observer", Name: "Observer", URL: server.URL},
		{ID: "disabled", Name: "Disabled", URL: server.URL, Enabled: &disabled},
	}, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	defer svc.CloseAI()
	ctx := context.Background()
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic, Members: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	root, err := svc.PostMessage(ctx, "alice", "general", "@ai:helper @ai:reviewer @ai:observer @ai:helper @ai:disabled @ai:unknown")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		select {
		case id := <-requests:
			if seen[id] {
				t.Fatal("duplicate account invocation")
			}
			seen[id] = true
		case <-time.After(8 * time.Second):
			t.Fatal("missing request")
		}
	}
	if !seen["helper"] || !seen["reviewer"] || !seen["observer"] {
		t.Fatal(seen)
	}
	awaitAIReply(t, svc, root.Seq)
}
