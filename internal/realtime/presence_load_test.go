package realtime_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/realtime"
	"github.com/masapico/minihub/internal/service"
	"github.com/masapico/minihub/internal/storage/storetest"
)

// Opt in: PRESENCE_CONNECTION_PROFILE=1 go test ./internal/realtime -run TestPresenceConnectionProfile -count=1 -v
func TestPresenceConnectionProfile(t *testing.T) {
	if os.Getenv("PRESENCE_CONNECTION_PROFILE") == "" {
		t.Skip("set PRESENCE_CONNECTION_PROFILE for 500 simultaneous presence watches")
	}
	const users = 500
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, users)
	for i := range ids {
		ids[i] = fmt.Sprintf("u%d", i)
		if err := store.SaveUser(context.Background(), &domain.User{ID: ids[i], Name: ids[i], Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveChannel(context.Background(), &domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic, Members: ids}); err != nil {
		t.Fatal(err)
	}
	svc := service.New(store)
	hub := realtime.New(
		func(r *http.Request) (string, error) { return r.URL.Query().Get("user"), nil },
		svc.CanReadChannel, svc.CanPostChannel, nil,
	)
	hub.SetPresenceCandidates(svc.MentionCandidateIDs)
	server := httptest.NewServer(hub)
	defer server.Close()
	defer hub.Close()
	conns := make([]*websocket.Conn, 0, users)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	var wg sync.WaitGroup
	errors := make(chan error, users)
	started := time.Now()
	for i := 0; i < users; i++ {
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+fmt.Sprintf("/?user=u%d", i), nil)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		wg.Add(1)
		go func(c *websocket.Conn) {
			defer wg.Done()
			_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
			for {
				var event realtime.Event
				if err := c.ReadJSON(&event); err != nil {
					errors <- err
					return
				}
				if event.Type == "presence_snapshot" && event.OnlineUserIDs != nil && len(*event.OnlineUserIDs) == users-1 {
					return
				}
			}
		}(conn)
		if err := conn.WriteJSON(realtime.Event{Type: "presence_watch", ChannelID: "general"}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Errorf("presence connection failed: %v", err)
	}
	t.Logf("500 watches reached the final snapshot in %s", time.Since(started))
}
