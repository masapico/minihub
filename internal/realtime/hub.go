package realtime

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait        = 10 * time.Second
	pongWait         = 60 * time.Second
	pingEvery        = 45 * time.Second
	maxInput         = 1024
	presenceInterval = 250 * time.Millisecond
)

type Event struct {
	ThreadRootSeq int64     `json:"threadRootSeq,omitempty"`
	Type          string    `json:"type"`
	ChannelID     string    `json:"channelId,omitempty"`
	Seq           int64     `json:"seq,omitempty"`
	MessageSeq    int64     `json:"messageSeq,omitempty"`
	ScheduleID    string    `json:"scheduleId,omitempty"`
	PollID        string    `json:"pollId,omitempty"`
	Revision      int64     `json:"revision,omitempty"`
	UserID        string    `json:"userId,omitempty"`
	TypingID      string    `json:"typingSession,omitempty"`
	OnlineUserIDs *[]string `json:"onlineUserIds,omitempty"`
}

type AuthorizeFunc func(context.Context, string, string) bool
type AuthenticateFunc func(*http.Request) (string, error)
type PresenceCandidatesFunc func(context.Context, string, string) ([]string, error)

type Hub struct {
	authenticate       AuthenticateFunc
	authorizeRead      AuthorizeFunc
	authorizePost      AuthorizeFunc
	logger             *slog.Logger
	mu                 sync.RWMutex
	clients            map[*client]struct{}
	upgrader           websocket.Upgrader
	nextClientID       atomic.Uint64
	closed             bool
	handlers           sync.WaitGroup
	presenceCandidates PresenceCandidatesFunc
	online             map[string]int
	presenceTimer      *time.Timer
	presenceVersion    uint64
	presenceChanged    map[string]struct{}
}

type client struct {
	hub         *Hub
	conn        *websocket.Conn
	userID      string
	send        chan Event
	done        chan struct{}
	closeOnce   sync.Once
	typingID    string
	typingMu    sync.Mutex
	typingScope typingScope
	lastTyping  time.Time
	watched     map[string]map[string]struct{}
}

type typingScope struct {
	channelID     string
	threadRootSeq int64
}

func New(authenticate AuthenticateFunc, authorizeRead, authorizePost AuthorizeFunc, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		authenticate:    authenticate,
		authorizeRead:   authorizeRead,
		authorizePost:   authorizePost,
		logger:          logger,
		clients:         make(map[*client]struct{}),
		online:          make(map[string]int),
		presenceChanged: make(map[string]struct{}),
		upgrader:        websocket.Upgrader{HandshakeTimeout: 10 * time.Second},
	}
}

func (h *Hub) SetPresenceCandidates(fn PresenceCandidatesFunc) { h.presenceCandidates = fn }

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userID, err := h.authenticate(r)
	if err != nil || userID == "" {
		http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Debug("websocket upgrade failed", "userId", userID, "error", err)
		return
	}
	typingID := strconv.FormatUint(h.nextClientID.Add(1), 36)
	// The opaque connection identifier lets recipients aggregate multiple tabs for one user.
	c := &client{hub: h, conn: conn, userID: userID, typingID: typingID, send: make(chan Event, 32), done: make(chan struct{})}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = conn.Close()
		return
	}
	h.clients[c] = struct{}{}
	h.online[userID]++
	if h.online[userID] == 1 {
		h.schedulePresenceLocked(userID)
	}
	h.handlers.Add(1)
	h.mu.Unlock()
	defer h.handlers.Done()
	written := make(chan struct{})
	go func() { defer close(written); c.writePump() }()
	c.readPump()
	<-written
}

// Close prevents new registrations and drains WebSocket work before storage closes.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	if h.presenceTimer != nil {
		h.presenceTimer.Stop()
		h.presenceTimer = nil
	}
	clients := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		c.close()
	}
	h.handlers.Wait()
}

func (h *Hub) NewMessage(ctx context.Context, channelID string, seq int64) {
	h.publish(ctx, Event{Type: "new_message", ChannelID: channelID, Seq: seq})
}

func (h *Hub) MessageChanged(ctx context.Context, channelID string, seq int64) {
	h.publish(ctx, Event{Type: "message_changed", ChannelID: channelID, Seq: seq})
}

func (h *Hub) ThreadUpdated(ctx context.Context, channelID string, root, seq int64, userID string) {
	h.publish(ctx, Event{Type: "thread_updated", ChannelID: channelID, ThreadRootSeq: root, Seq: seq, UserID: userID})
}

func (h *Hub) ReactionChanged(ctx context.Context, channelID string, messageSeq int64) {
	h.publish(ctx, Event{Type: "reaction_changed", ChannelID: channelID, MessageSeq: messageSeq})
}

func (h *Hub) ScheduleChanged(ctx context.Context, channelID, scheduleID string, revision int64) {
	h.publish(ctx, Event{Type: "schedule_changed", ChannelID: channelID, ScheduleID: scheduleID, Revision: revision})
}

func (h *Hub) PollChanged(ctx context.Context, channelID, pollID string, revision int64) {
	h.publish(ctx, Event{Type: "poll_changed", ChannelID: channelID, PollID: pollID, Revision: revision})
}

func (h *Hub) ChannelsChanged(_ context.Context) {
	h.mu.Lock()
	h.presenceVersion++
	for c := range h.clients {
		c.watched = nil
	}
	h.mu.Unlock()
	h.publishAll(Event{Type: "channels_changed"})
}

func (h *Hub) schedulePresenceLocked(userID string) {
	h.presenceChanged[userID] = struct{}{}
	if !h.closed && h.presenceTimer == nil {
		h.presenceTimer = time.AfterFunc(presenceInterval, h.flushPresence)
	}
}

func (h *Hub) presenceSnapshotLocked(channelID string, allowed map[string]struct{}) Event {
	ids := make([]string, 0, len(allowed))
	for id := range allowed {
		if h.online[id] > 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return Event{Type: "presence_snapshot", ChannelID: channelID, OnlineUserIDs: &ids}
}

func (h *Hub) flushPresence() {
	h.mu.Lock()
	h.presenceTimer = nil
	if h.closed {
		h.mu.Unlock()
		return
	}
	changed := h.presenceChanged
	h.presenceChanged = make(map[string]struct{})
	slow := make([]*client, 0)
	for c := range h.clients {
		for channelID, allowed := range c.watched {
			relevant := false
			for id := range changed {
				if _, ok := allowed[id]; ok {
					relevant = true
					break
				}
			}
			if !relevant {
				continue
			}
			select {
			case c.send <- h.presenceSnapshotLocked(channelID, allowed):
			default:
				slow = append(slow, c)
			}
		}
	}
	h.mu.Unlock()
	for _, c := range slow {
		c.close()
	}
}

func (h *Hub) DisconnectUser(userID string) {
	h.mu.RLock()
	clients := make([]*client, 0)
	for c := range h.clients {
		if c.userID == userID {
			clients = append(clients, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range clients {
		c.close()
	}
}

func (h *Hub) publish(ctx context.Context, event Event) {
	h.mu.RLock()
	clients := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	for _, c := range clients {
		if (event.Type == "typing_start" || event.Type == "typing_stop") && c.userID == event.UserID {
			continue
		}
		if !h.authorizeRead(ctx, c.userID, event.ChannelID) {
			continue
		}
		select {
		case c.send <- event:
		default:
			c.close()
		}
	}
}

func (h *Hub) publishAll(event Event) {
	h.mu.RLock()
	clients := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	for _, c := range clients {
		select {
		case c.send <- event:
		default:
			c.close()
		}
	}
}

func (c *client) close() {
	c.closeOnce.Do(func() {
		scope, typing := c.clearTyping(typingScope{})
		c.hub.mu.Lock()
		delete(c.hub.clients, c)
		c.watched = nil
		c.hub.online[c.userID]--
		if c.hub.online[c.userID] == 0 {
			delete(c.hub.online, c.userID)
			c.hub.schedulePresenceLocked(c.userID)
		}
		c.hub.mu.Unlock()
		if typing {
			c.publishTyping("typing_stop", scope)
		}
		close(c.done)
		_ = c.conn.Close()
	})
}

func (c *client) readPump() {
	defer c.close()
	c.conn.SetReadLimit(maxInput)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		var event Event
		if err := c.conn.ReadJSON(&event); err != nil {
			return
		}
		c.handleIncoming(event)
	}
}

func (c *client) handleIncoming(event Event) {
	switch event.Type {
	case "typing_start":
		if event.ChannelID == "" || event.ThreadRootSeq < 0 || !c.hub.authorizePost(context.Background(), c.userID, event.ChannelID) {
			return
		}
		scope := typingScope{channelID: event.ChannelID, threadRootSeq: event.ThreadRootSeq}
		oldScope, changed, publish := c.startTyping(scope)
		if changed {
			c.publishTyping("typing_stop", oldScope)
		}
		if publish {
			c.publishTyping("typing_start", scope)
		}
	case "typing_stop":
		if event.ThreadRootSeq < 0 {
			return
		}
		scope, typing := c.clearTyping(typingScope{channelID: event.ChannelID, threadRootSeq: event.ThreadRootSeq})
		if typing {
			c.publishTyping("typing_stop", scope)
		}
	case "presence_watch":
		if event.ChannelID == "" || c.hub.presenceCandidates == nil {
			return
		}
		c.hub.mu.RLock()
		version := c.hub.presenceVersion
		c.hub.mu.RUnlock()
		ids, err := c.hub.presenceCandidates(context.Background(), c.userID, event.ChannelID)
		if err != nil {
			select {
			case c.send <- Event{Type: "presence_unavailable", ChannelID: event.ChannelID}:
			default:
				c.close()
			}
			return
		}
		allowed := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			allowed[id] = struct{}{}
		}
		c.hub.mu.Lock()
		if version != c.hub.presenceVersion {
			c.hub.mu.Unlock()
			select {
			case c.send <- Event{Type: "presence_unavailable", ChannelID: event.ChannelID}:
			default:
				c.close()
			}
			return
		}
		if c.watched == nil {
			c.watched = make(map[string]map[string]struct{})
		}
		if _, exists := c.watched[event.ChannelID]; !exists && len(c.watched) >= 2 {
			c.hub.mu.Unlock()
			return
		}
		c.watched[event.ChannelID] = allowed
		snapshot := c.hub.presenceSnapshotLocked(event.ChannelID, allowed)
		select {
		case c.send <- snapshot:
		default:
			go c.close()
		}
		c.hub.mu.Unlock()
	case "presence_unwatch":
		c.hub.mu.Lock()
		delete(c.watched, event.ChannelID)
		c.hub.mu.Unlock()
	}
}

func (c *client) publishTyping(eventType string, scope typingScope) {
	c.hub.publish(context.Background(), Event{
		Type: eventType, ChannelID: scope.channelID, ThreadRootSeq: scope.threadRootSeq,
		UserID: c.userID, TypingID: c.typingID,
	})
}

func (c *client) startTyping(scope typingScope) (typingScope, bool, bool) {
	c.typingMu.Lock()
	defer c.typingMu.Unlock()
	now := time.Now()
	oldScope := c.typingScope
	changed := oldScope.channelID != "" && oldScope != scope
	if oldScope == scope && now.Sub(c.lastTyping) < 500*time.Millisecond {
		return typingScope{}, false, false
	}
	c.typingScope = scope
	c.lastTyping = now
	return oldScope, changed, true
}

func (c *client) clearTyping(expected typingScope) (typingScope, bool) {
	c.typingMu.Lock()
	defer c.typingMu.Unlock()
	if c.typingScope.channelID == "" || (expected.channelID != "" && expected != c.typingScope) {
		return typingScope{}, false
	}
	scope := c.typingScope
	c.typingScope = typingScope{}
	c.lastTyping = time.Time{}
	return scope, true
}

func (c *client) writePump() {
	ticker := time.NewTicker(pingEvery)
	defer ticker.Stop()
	defer c.close()
	for {
		select {
		case event := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteJSON(event); err != nil {
				return
			}
		case <-c.done:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			_ = c.conn.WriteMessage(websocket.CloseMessage, nil)
			return
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
