package realtime_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/realtime"
	"github.com/masapico/minihub/internal/service"
	"github.com/masapico/minihub/internal/storage"
	"github.com/masapico/minihub/internal/storage/storetest"
)

// Opt in: THREAD_CONNECTION_PROFILE=1 go test ./internal/realtime -run TestThreadConnectionProfile -count=1 -v
func TestThreadConnectionProfile(t *testing.T) {
	if os.Getenv("THREAD_CONNECTION_PROFILE") == "" {
		t.Skip("set THREAD_CONNECTION_PROFILE for 500 connections / 60 seconds")
	}
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 500; i++ {
		if err := store.SaveUser(ctx, &domain.User{ID: fmt.Sprintf("u%d", i), Name: "Load user", Role: domain.RoleUser, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveChannel(ctx, &domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic, Members: []string{"u0"}}); err != nil {
		t.Fatal(err)
	}
	svc := service.New(store)
	hub := realtime.New(func(r *http.Request) (string, error) { return r.URL.Query().Get("user"), nil }, svc.CanReadChannel, svc.CanPostChannel, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetMessagePublisher(hub)
	root, err := svc.PostMessage(ctx, "u0", "general", "root")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(hub)
	defer server.Close()
	conns := make([]*websocket.Conn, 0, 500)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for i := 0; i < 500; i++ {
		c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+fmt.Sprintf("/?user=u%d", i), nil)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	const posts = 600
	mixed := os.Getenv("THREAD_MIXED_PROFILE") != ""
	var wg sync.WaitGroup
	errs := make(chan error, len(conns))
	for _, c := range conns {
		wg.Add(1)
		go func(c *websocket.Conn) {
			defer wg.Done()
			deadline := 100 * time.Second
			if mixed {
				deadline = 180 * time.Second
			}
			_ = c.SetReadDeadline(time.Now().Add(deadline))
			for i := 0; i < posts; {
				var e realtime.Event
				if err := c.ReadJSON(&e); err != nil {
					errs <- err
					return
				}
				if mixed && e.Type == "reaction_changed" {
					continue
				}
				if e.Type != "thread_updated" || e.ThreadRootSeq != root.Seq || e.Seq != int64(i+2) {
					errs <- fmt.Errorf("wrong event %+v", e)
					return
				}
				i++
			}
		}(c)
	}
	times := make([]time.Duration, 0, posts)
	started := time.Now()
	for i := 0; i < posts; i++ {
		target := started.Add(time.Duration(i) * 100 * time.Millisecond)
		if delay := time.Until(target); delay > 0 {
			time.Sleep(delay)
		}
		start := time.Now()
		m, err := svc.PostThreadMessage(ctx, "u0", "general", root.Seq, "load reply")
		if err != nil {
			t.Fatal(err)
		}
		if mixed && i%10 == 9 {
			var updates sync.WaitGroup
			for j := 0; j < 10; j++ {
				updates.Add(1)
				go func(j int) {
					defer updates.Done()
					user := fmt.Sprintf("u%d", 1+((i/10)*10+j)%499)
					if err := svc.MarkThreadRead(ctx, user, "general", root.Seq, m.Seq); err != nil {
						t.Error(err)
					}
				}(j)
			}
			if _, err := svc.SetReaction(ctx, "u0", "general", m.Seq, "thanks", true); err != nil {
				t.Error(err)
			}
			updates.Wait()
		}
		times = append(times, time.Since(start))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if mixed {
		for i := 1; i < 500; i++ {
			summaries, err := store.ThreadSummaries(ctx, fmt.Sprintf("u%d", i), "general", storage.ThreadFilter{Roots: []int64{root.Seq}})
			if err != nil || len(summaries) != 1 || summaries[0].LastReadSeq <= root.Seq {
				t.Fatalf("missing read update user=%d summaries=%+v err=%v", i, summaries, err)
			}
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	t.Logf("connections=500 posts=%d elapsed=%s commit_and_publish_p50=%s p95=%s", posts, time.Since(started), times[posts/2], times[posts*95/100])
}
