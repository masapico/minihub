package realtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"

	"github.com/masapico/minihub/internal/auth"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/httpapi"
	"github.com/masapico/minihub/internal/realtime"
	"github.com/masapico/minihub/internal/service"
	"github.com/masapico/minihub/internal/storage/storetest"
)

func TestHubPublishesOnlyAuthorizedEvents(t *testing.T) {
	hub := realtime.New(
		func(*http.Request) (string, error) { return "alice", nil },
		func(_ context.Context, userID, channelID string) bool {
			return userID == "alice" && channelID == "general"
		},
		func(_ context.Context, userID, channelID string) bool {
			return userID == "alice" && channelID == "general"
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	server := httptest.NewServer(hub)
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	hub.NewMessage(context.Background(), "secret", 1)
	hub.NewMessage(context.Background(), "general", 42)
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var event realtime.Event
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "new_message" || event.ChannelID != "general" || event.Seq != 42 {
		t.Fatalf("unexpected event: %#v", event)
	}
	hub.ThreadUpdated(context.Background(), "secret", 1, 2, "bob")
	hub.ThreadUpdated(context.Background(), "general", 40, 43, "bob")
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "thread_updated" || event.ThreadRootSeq != 40 || event.Seq != 43 || event.UserID != "bob" {
		t.Fatalf("unexpected thread event: %#v", event)
	}
	hub.ReactionChanged(context.Background(), "general", 41)
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "reaction_changed" || event.ChannelID != "general" || event.MessageSeq != 41 {
		t.Fatalf("unexpected reaction event: %#v", event)
	}
	hub.ScheduleChanged(context.Background(), "general", "plan1", 7)
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "schedule_changed" || event.ChannelID != "general" || event.ScheduleID != "plan1" || event.Revision != 7 {
		t.Fatalf("unexpected schedule event: %#v", event)
	}
	hub.ChannelsChanged(context.Background())
	event = realtime.Event{}
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "channels_changed" || event.ChannelID != "" {
		t.Fatalf("unexpected channels event: %#v", event)
	}
}

func TestHubRejectsUnknownUser(t *testing.T) {
	hub := realtime.New(
		func(*http.Request) (string, error) { return "", errors.New("unauthenticated") },
		func(context.Context, string, string) bool { return false },
		func(context.Context, string, string) bool { return false },
		nil,
	)
	server := httptest.NewServer(hub)
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	conn, response, err := websocket.DefaultDialer.Dial(url, nil)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("expected handshake failure")
	}
	if response == nil || response.StatusCode != 401 {
		t.Fatalf("response=%v error=%v", response, err)
	}
}

func TestPresenceWatchTracksLastConnectionAndFiltersCandidates(t *testing.T) {
	hub := realtime.New(
		func(r *http.Request) (string, error) { return r.Header.Get("X-User-ID"), nil },
		func(context.Context, string, string) bool { return true },
		func(_ context.Context, userID, channelID string) bool {
			return userID == "alice" && channelID == "general"
		},
		nil,
	)
	hub.SetPresenceCandidates(func(_ context.Context, userID, channelID string) ([]string, error) {
		if userID != "alice" || channelID != "general" {
			return nil, errors.New("forbidden")
		}
		return []string{"bob"}, nil
	})
	server := httptest.NewServer(hub)
	defer server.Close()
	defer hub.Close()
	dial := func(id string) *websocket.Conn {
		t.Helper()
		header := http.Header{"X-User-ID": []string{id}}
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), header)
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	alice := dial("alice")
	defer alice.Close()
	bob1 := dial("bob")
	defer bob1.Close()
	bob2 := dial("bob")
	defer bob2.Close()
	mallory := dial("mallory")
	defer mallory.Close()
	if err := alice.WriteJSON(realtime.Event{Type: "presence_watch", ChannelID: "secret"}); err != nil {
		t.Fatal(err)
	}
	var event realtime.Event
	_ = alice.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := alice.ReadJSON(&event); err != nil || event.Type != "presence_unavailable" {
		t.Fatalf("denied watch: %+v %v", event, err)
	}
	if err := alice.WriteJSON(realtime.Event{Type: "presence_watch", ChannelID: "general"}); err != nil {
		t.Fatal(err)
	}
	if err := alice.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "presence_snapshot" || event.OnlineUserIDs == nil || len(*event.OnlineUserIDs) != 1 || (*event.OnlineUserIDs)[0] != "bob" {
		t.Fatalf("filtered snapshot: %+v", event)
	}
	_ = bob1.Close()
	// The remaining tab keeps Bob online; a new watch gets the current count.
	if err := alice.WriteJSON(realtime.Event{Type: "presence_watch", ChannelID: "general"}); err != nil {
		t.Fatal(err)
	}
	for {
		if err := alice.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "presence_snapshot" {
			break
		}
	}
	if event.OnlineUserIDs == nil || len(*event.OnlineUserIDs) != 1 {
		t.Fatalf("remaining tab: %+v", event)
	}
	_ = bob2.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_ = alice.SetReadDeadline(deadline)
		if err := alice.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "presence_snapshot" && event.OnlineUserIDs != nil && len(*event.OnlineUserIDs) == 0 {
			break
		}
	}
}

func TestHubRelaysTypingForAuthorizedPoster(t *testing.T) {
	hub := realtime.New(
		func(r *http.Request) (string, error) {
			id := r.Header.Get("X-User-ID")
			if id == "" {
				return "", errors.New("unauthenticated")
			}
			return id, nil
		},
		func(_ context.Context, userID, channelID string) bool {
			return channelID == "general" && (userID == "alice" || userID == "bob" || userID == "charlie")
		},
		func(_ context.Context, userID, channelID string) bool {
			return channelID == "general" && userID == "alice"
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	server := httptest.NewServer(hub)
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	dial := func(userID string) *websocket.Conn {
		t.Helper()
		header := http.Header{}
		header.Set("X-User-ID", userID)
		conn, _, err := websocket.DefaultDialer.Dial(url, header)
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	alice := dial("alice")
	defer alice.Close()
	bob := dial("bob")
	defer bob.Close()
	charlie := dial("charlie")
	defer charlie.Close()

	if err := alice.WriteJSON(realtime.Event{Type: "typing_start", ChannelID: "general", ThreadRootSeq: 42, UserID: "mallory"}); err != nil {
		t.Fatal(err)
	}
	_ = bob.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event realtime.Event
	if err := bob.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "typing_start" || event.ChannelID != "general" || event.ThreadRootSeq != 42 || event.UserID != "alice" || event.TypingID == "" {
		t.Fatalf("unexpected typing event: %#v", event)
	}
	if err := alice.WriteJSON(realtime.Event{Type: "typing_start", ChannelID: "general", ThreadRootSeq: 43}); err != nil {
		t.Fatal(err)
	}
	if err := bob.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "typing_stop" || event.ThreadRootSeq != 42 || event.UserID != "alice" {
		t.Fatalf("unexpected typing scope stop: %#v", event)
	}
	if err := bob.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "typing_start" || event.ThreadRootSeq != 43 || event.UserID != "alice" {
		t.Fatalf("unexpected typing scope start: %#v", event)
	}
	if err := alice.WriteJSON(realtime.Event{Type: "typing_stop", ChannelID: "general", ThreadRootSeq: 42}); err != nil {
		t.Fatal(err)
	}
	if err := alice.WriteJSON(realtime.Event{Type: "typing_stop", ChannelID: "general", ThreadRootSeq: 43}); err != nil {
		t.Fatal(err)
	}
	if err := bob.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "typing_stop" || event.ThreadRootSeq != 43 || event.UserID != "alice" {
		t.Fatalf("unexpected final typing stop: %#v", event)
	}

	if err := charlie.WriteJSON(realtime.Event{Type: "typing_start", ChannelID: "general"}); err != nil {
		t.Fatal(err)
	}
	_ = bob.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if err := bob.ReadJSON(&event); err == nil {
		t.Fatalf("unauthorized typing event was relayed: %#v", event)
	}
	_ = alice.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if err := alice.ReadJSON(&event); err == nil {
		t.Fatalf("sender received its own typing event: %#v", event)
	}
}

func TestCommittedHTTPMessageProducesRealtimeEvent(t *testing.T) {
	ctx := context.Background()
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("a-secure-test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.User{{ID: "admin", Name: "Admin", Role: domain.RoleAdmin, Enabled: true}, {ID: "alice", Name: "Alice", Role: domain.RoleUser, Enabled: true, PasswordHash: string(hash)}} {
		user := user
		if err := store.SaveUser(ctx, &user); err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(store)
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{ID: "general", Name: "General", Type: domain.ChannelPublic, Members: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewManager(store, false)
	hub := realtime.New(sessions.Authenticate, svc.CanReadChannel, svc.CanPostChannel, logger)
	svc.SetMessagePublisher(hub)
	mux := http.NewServeMux()
	mux.Handle("/api/realtime", hub)
	mux.Handle("/api/", httpapi.New(svc, sessions, logger))
	server := httptest.NewServer(mux)
	defer server.Close()

	loginBody, _ := json.Marshal(map[string]string{"userId": "alice", "password": "a-secure-test-password"})
	loginResponse, err := http.Post(server.URL+"/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	defer loginResponse.Body.Close()
	var login struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.NewDecoder(loginResponse.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	if len(loginResponse.Cookies()) != 1 {
		t.Fatal("session cookie was not returned")
	}
	requestHeader := http.Header{}
	requestHeader.Set("Cookie", loginResponse.Cookies()[0].String())
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/realtime"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, requestHeader)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	body, _ := json.Marshal(map[string]string{"text": "hello"})
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/channels/general/messages", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(loginResponse.Cookies()[0])
	req.Header.Set("X-CSRF-Token", login.CSRFToken)
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("post status=%d", response.StatusCode)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event realtime.Event
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.ChannelID != "general" || event.Seq != 1 {
		t.Fatalf("unexpected event: %#v", event)
	}
}
