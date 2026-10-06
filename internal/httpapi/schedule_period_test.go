package httpapi_test

import (
	"encoding/json"
	"github.com/masapico/minihub/internal/domain"
	"net/http"
	"testing"
)

func TestSchedulePeriodPagination(t *testing.T) {
	h := setupAPI(t)
	createdUser := request(t, h, http.MethodPost, "/api/users", "admin", map[string]any{"users": []map[string]any{{"id": "bob", "name": "Bob", "password": "test-password"}}})
	if createdUser.Code != http.StatusCreated {
		t.Fatal(createdUser.Body.String())
	}
	request(t, h, http.MethodPost, "/api/channels", "admin", map[string]any{"id": "periods", "name": "Periods", "type": "public", "members": []string{"alice"}})
	for _, date := range []string{"2000-01-01", "2000-01-02", "2999-01-01"} {
		r := request(t, h, http.MethodPost, "/api/schedules", "alice", map[string]any{"channelId": "periods", "title": date, "candidates": []map[string]string{{"date": date, "startTime": "10:00"}, {"date": date, "startTime": "11:00"}}})
		if r.Code != http.StatusCreated {
			t.Fatal(r.Body.String())
		}
	}
	var firstID string
	for _, tc := range []struct {
		query, user string
		count       int
		next        bool
	}{
		{"period=past&limit=1", "alice", 1, true},
		{"period=past&limit=1&cursor=1", "alice", 1, false},
		{"period=upcoming", "alice", 1, false},
		{"", "alice", 3, false},
		{"period=all", "alice", 3, false},
		{"period=past", "bob", 0, false},
	} {
		r := request(t, h, http.MethodGet, "/api/schedules?"+tc.query, tc.user, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("%s: %s", tc.query, r.Body.String())
		}
		var page struct {
			Schedules  []domain.Schedule
			NextCursor string
		}
		if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Schedules) != tc.count || (page.NextCursor != "") != tc.next {
			t.Fatalf("%s: %s", tc.query, r.Body.String())
		}
		if tc.next {
			firstID = page.Schedules[0].ID
		}
		if tc.query == "period=past&limit=1&cursor=1" && page.Schedules[0].ID == firstID {
			t.Fatal("duplicate page")
		}
	}
	if r := request(t, h, http.MethodGet, "/api/schedules?period=invalid", "alice", nil); r.Code != http.StatusBadRequest {
		t.Fatalf("invalid period: %d", r.Code)
	}
	channel := request(t, h, http.MethodPost, "/api/channels", "admin", map[string]any{"id": "private-period", "name": "Private", "type": "private"})
	if channel.Code != http.StatusCreated {
		t.Fatal(channel.Body.String())
	}
	r := request(t, h, http.MethodPost, "/api/schedules", "admin", map[string]any{"channelId": "private-period", "title": "Private past", "candidates": []map[string]string{{"date": "2000-01-01"}, {"date": "2000-01-02"}}})
	if r.Code != http.StatusCreated {
		t.Fatal(r.Body.String())
	}
	var private domain.Schedule
	if err := json.Unmarshal(r.Body.Bytes(), &private); err != nil {
		t.Fatal(err)
	}
	published := request(t, h, http.MethodPost, "/api/schedules/"+private.ID+"/publish", "admin", map[string]any{"revision": private.Revision})
	if published.Code != http.StatusOK {
		t.Fatal(published.Body.String())
	}
	for _, user := range []string{"alice", "bob"} {
		r := request(t, h, http.MethodGet, "/api/schedules?period=past", user, nil)
		var page struct{ Schedules []domain.Schedule }
		if r.Code != http.StatusOK {
			t.Fatal(r.Body.String())
		}
		if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Schedules {
			if item.ID == private.ID {
				t.Fatal("private schedule leaked")
			}
		}
	}
}
