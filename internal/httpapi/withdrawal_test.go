package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/masapico/minihub/internal/domain"
)

func TestWithdrawalHTTPRoutes(t *testing.T) {
	h := setupAPI(t)
	created := request(t, h, http.MethodPost, "/api/channels", "admin", map[string]any{"id": "c", "name": "Chat", "type": "public", "members": []string{"alice"}})
	if created.Code != http.StatusCreated {
		t.Fatal(created.Body.String())
	}
	m := request(t, h, http.MethodPost, "/api/channels/c/messages", "alice", map[string]any{"text": "wrong text"})
	if m.Code != http.StatusCreated {
		t.Fatal(m.Body.String())
	}
	w := request(t, h, http.MethodPost, "/api/channels/c/messages/1/withdraw", "alice", nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "wrong text") || !strings.Contains(w.Body.String(), "withdrawnAt") {
		t.Fatalf("message withdrawal=%d %s", w.Code, w.Body.String())
	}
	history := request(t, h, http.MethodGet, "/api/channels/c/messages?limit=10", "alice", nil)
	if strings.Contains(history.Body.String(), "wrong text") {
		t.Fatalf("message leaked: %s", history.Body.String())
	}
	p := request(t, h, http.MethodPost, "/api/polls", "alice", map[string]any{"channelId": "c", "question": "wrong question", "options": []string{"A", "B"}})
	if p.Code != http.StatusCreated {
		t.Fatal(p.Body.String())
	}
	var poll struct {
		Poll domain.Poll `json:"poll"`
	}
	if err := json.Unmarshal(p.Body.Bytes(), &poll); err != nil {
		t.Fatal(err)
	}
	w = request(t, h, http.MethodPost, "/api/polls/"+poll.Poll.ID+"/withdraw", "alice", nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "wrong question") {
		t.Fatalf("poll withdrawal=%d %s", w.Code, w.Body.String())
	}
	detail := request(t, h, http.MethodGet, "/api/polls/"+poll.Poll.ID, "alice", nil)
	if strings.Contains(detail.Body.String(), "wrong question") {
		t.Fatalf("poll leaked: %s", detail.Body.String())
	}
	s := request(t, h, http.MethodPost, "/api/schedules", "alice", map[string]any{"channelId": "c", "title": "wrong plan", "candidates": []map[string]string{{"date": "2027-01-10"}, {"date": "2027-01-11"}}})
	if s.Code != http.StatusCreated {
		t.Fatal(s.Body.String())
	}
	var schedule domain.Schedule
	if err := json.Unmarshal(s.Body.Bytes(), &schedule); err != nil {
		t.Fatal(err)
	}
	published := request(t, h, http.MethodPost, "/api/schedules/"+schedule.ID+"/publish", "alice", map[string]any{"revision": schedule.Revision})
	if published.Code != http.StatusOK {
		t.Fatal(published.Body.String())
	}
	w = request(t, h, http.MethodPost, "/api/schedules/"+schedule.ID+"/withdraw", "alice", nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "wrong plan") {
		t.Fatalf("schedule withdrawal=%d %s", w.Code, w.Body.String())
	}
	history = request(t, h, http.MethodGet, "/api/channels/c/messages?limit=10", "alice", nil)
	if strings.Contains(history.Body.String(), "wrong question") || strings.Contains(history.Body.String(), "wrong plan") {
		t.Fatalf("announcement leaked: %s", history.Body.String())
	}
	for _, tc := range []struct{ path, content string }{
		{"/api/channels/c/messages/1/restore", "wrong text"},
		{"/api/polls/" + poll.Poll.ID + "/restore", "wrong question"},
		{"/api/schedules/" + schedule.ID + "/restore", "wrong plan"},
	} {
		r := request(t, h, http.MethodPost, tc.path, "alice", nil)
		if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), tc.content) {
			t.Fatalf("restore %s=%d %s", tc.path, r.Code, r.Body.String())
		}
	}
}
