package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/masapico/minihub/internal/domain"
)

func createPublicSchedule(t *testing.T, handler http.Handler) domain.Schedule {
	t.Helper()
	channel := request(t, handler, http.MethodPost, "/api/channels", "admin", map[string]any{"id": "planning", "name": "Planning", "type": "public", "members": []string{"alice"}})
	if channel.Code != http.StatusCreated {
		t.Fatalf("create channel status=%d body=%s", channel.Code, channel.Body.String())
	}
	created := request(t, handler, http.MethodPost, "/api/schedules", "alice", map[string]any{
		"channelId": "planning", "title": "懇親会", "description": "参加可能日を回答してください",
		"candidates": []map[string]string{{"date": "2027-01-10", "startTime": "18:00", "endTime": "20:00"}, {"date": "2027-01-11"}},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create schedule status=%d body=%s", created.Code, created.Body.String())
	}
	var schedule domain.Schedule
	if err := json.Unmarshal(created.Body.Bytes(), &schedule); err != nil {
		t.Fatal(err)
	}
	return schedule
}

func TestScheduleHTTPFlowPublishesAnswersLocksAndFinalizes(t *testing.T) {
	handler := setupAPI(t)
	schedule := createPublicSchedule(t, handler)
	if schedule.Status != domain.ScheduleDraft || schedule.ID == "" {
		t.Fatalf("unexpected schedule: %#v", schedule)
	}
	published := request(t, handler, http.MethodPost, "/api/schedules/"+schedule.ID+"/publish", "alice", map[string]any{"revision": schedule.Revision})
	if published.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", published.Code, published.Body.String())
	}
	if err := json.Unmarshal(published.Body.Bytes(), &schedule); err != nil {
		t.Fatal(err)
	}
	history := request(t, handler, http.MethodGet, "/api/channels/planning/messages?limit=10", "alice", nil)
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), `"scheduleRef":{"id":"`+schedule.ID+`","event":"published"}`) {
		t.Fatalf("announcement status=%d body=%s", history.Code, history.Body.String())
	}
	choices := map[string]domain.ScheduleChoice{schedule.Candidates[0].ID: domain.ScheduleYes, schedule.Candidates[1].ID: domain.ScheduleMaybe}
	answered := request(t, handler, http.MethodPut, "/api/schedules/"+schedule.ID+"/responses/me", "alice", map[string]any{"revision": schedule.Revision, "choices": choices, "comment": "どちらでも可"})
	if answered.Code != http.StatusOK {
		t.Fatalf("answer status=%d body=%s", answered.Code, answered.Body.String())
	}
	detail := request(t, handler, http.MethodGet, "/api/schedules/"+schedule.ID, "alice", nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"yes":1`) || !strings.Contains(detail.Body.String(), `"maybe":1`) {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	changed := append([]domain.ScheduleCandidate(nil), schedule.Candidates...)
	changed[0].Date = "2027-02-01"
	edit := request(t, handler, http.MethodPut, "/api/schedules/"+schedule.ID, "alice", map[string]any{"revision": schedule.Revision, "title": schedule.Title, "description": "changed", "candidates": changed})
	if edit.Code != http.StatusBadRequest {
		t.Fatalf("candidate edit status=%d body=%s", edit.Code, edit.Body.String())
	}
	finalized := request(t, handler, http.MethodPost, "/api/schedules/"+schedule.ID+"/finalize", "alice", map[string]any{"revision": schedule.Revision, "candidateId": schedule.Candidates[0].ID})
	if finalized.Code != http.StatusOK {
		t.Fatalf("finalize status=%d body=%s", finalized.Code, finalized.Body.String())
	}
	if got := request(t, handler, http.MethodPut, "/api/schedules/"+schedule.ID+"/responses/me", "alice", map[string]any{"revision": schedule.Revision + 2, "choices": choices}).Code; got != http.StatusBadRequest {
		t.Fatalf("answer after finalize status=%d", got)
	}
}

func TestPrivateScheduleDoesNotLeakAndStaleEditConflicts(t *testing.T) {
	handler := setupAPI(t)
	request(t, handler, http.MethodPost, "/api/channels", "admin", map[string]any{"id": "secret", "name": "Secret", "type": "private"})
	created := request(t, handler, http.MethodPost, "/api/schedules", "admin", map[string]any{"channelId": "secret", "title": "Secret plan", "candidates": []map[string]string{{"date": "2027-01-10"}, {"date": "2027-01-11"}}})
	var schedule domain.Schedule
	_ = json.Unmarshal(created.Body.Bytes(), &schedule)
	if got := request(t, handler, http.MethodGet, "/api/schedules/"+schedule.ID, "alice", nil).Code; got != http.StatusNotFound {
		t.Fatalf("private schedule status=%d", got)
	}
	stale := request(t, handler, http.MethodPut, "/api/schedules/"+schedule.ID, "admin", map[string]any{"revision": schedule.Revision + 1, "title": schedule.Title, "candidates": schedule.Candidates})
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "再読み込み") {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}
}

func TestGroupDerivedPrivateMemberCanUseSchedule(t *testing.T) {
	handler := setupAPI(t)
	group := request(t, handler, http.MethodPost, "/api/groups", "admin", map[string]string{"id": "sales", "name": "Sales"})
	if group.Code != http.StatusCreated {
		t.Fatalf("create group status=%d body=%s", group.Code, group.Body.String())
	}
	channel := request(t, handler, http.MethodPost, "/api/channels", "admin", map[string]any{
		"id": "sales-room", "name": "Sales room", "type": "private", "groups": []string{"sales"},
	})
	if channel.Code != http.StatusCreated {
		t.Fatalf("create channel status=%d body=%s", channel.Code, channel.Body.String())
	}
	created := request(t, handler, http.MethodPost, "/api/schedules", "alice", map[string]any{
		"channelId": "sales-room", "title": "Sales planning",
		"candidates": []map[string]string{{"date": "2027-01-10"}, {"date": "2027-01-11"}},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("group-derived member create schedule status=%d body=%s", created.Code, created.Body.String())
	}
	var schedule domain.Schedule
	if err := json.Unmarshal(created.Body.Bytes(), &schedule); err != nil {
		t.Fatal(err)
	}
	published := request(t, handler, http.MethodPost, "/api/schedules/"+schedule.ID+"/publish", "alice", map[string]any{"revision": schedule.Revision})
	if published.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", published.Code, published.Body.String())
	}
	if err := json.Unmarshal(published.Body.Bytes(), &schedule); err != nil {
		t.Fatal(err)
	}
	choices := map[string]domain.ScheduleChoice{
		schedule.Candidates[0].ID: domain.ScheduleYes,
		schedule.Candidates[1].ID: domain.ScheduleNo,
	}
	answered := request(t, handler, http.MethodPut, "/api/schedules/"+schedule.ID+"/responses/me", "alice", map[string]any{
		"revision": schedule.Revision, "choices": choices,
	})
	if answered.Code != http.StatusOK {
		t.Fatalf("group-derived member answer status=%d body=%s", answered.Code, answered.Body.String())
	}
	detail := request(t, handler, http.MethodGet, "/api/schedules/"+schedule.ID, "alice", nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"userId":"alice"`) {
		t.Fatalf("group-derived member schedule detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	directory := request(t, handler, http.MethodGet, "/api/users/directory", "alice", nil)
	if directory.Code != http.StatusOK || !strings.Contains(directory.Body.String(), `"id":"alice","name":"Alice"`) {
		t.Fatalf("schedule responder name is not resolvable status=%d body=%s", directory.Code, directory.Body.String())
	}
}

func TestScheduleCloseReopenAndCompleteAnswers(t *testing.T) {
	handler := setupAPI(t)
	schedule := createPublicSchedule(t, handler)
	published := request(t, handler, http.MethodPost, "/api/schedules/"+schedule.ID+"/publish", "alice", map[string]any{"revision": schedule.Revision})
	if err := json.Unmarshal(published.Body.Bytes(), &schedule); err != nil {
		t.Fatal(err)
	}
	closed := request(t, handler, http.MethodPost, "/api/schedules/"+schedule.ID+"/close", "alice", map[string]any{"revision": schedule.Revision})
	if closed.Code != http.StatusOK {
		t.Fatalf("close status=%d body=%s", closed.Code, closed.Body.String())
	}
	if err := json.Unmarshal(closed.Body.Bytes(), &schedule); err != nil {
		t.Fatal(err)
	}
	incomplete := map[string]domain.ScheduleChoice{schedule.Candidates[0].ID: domain.ScheduleYes}
	if got := request(t, handler, http.MethodPut, "/api/schedules/"+schedule.ID+"/responses/me", "alice", map[string]any{"revision": schedule.Revision, "choices": incomplete}).Code; got != http.StatusBadRequest {
		t.Fatalf("closed answer status=%d", got)
	}
	reopened := request(t, handler, http.MethodPost, "/api/schedules/"+schedule.ID+"/reopen", "alice", map[string]any{"revision": schedule.Revision})
	if reopened.Code != http.StatusOK {
		t.Fatalf("reopen status=%d body=%s", reopened.Code, reopened.Body.String())
	}
	if err := json.Unmarshal(reopened.Body.Bytes(), &schedule); err != nil {
		t.Fatal(err)
	}
	if got := request(t, handler, http.MethodPut, "/api/schedules/"+schedule.ID+"/responses/me", "alice", map[string]any{"revision": schedule.Revision, "choices": incomplete}).Code; got != http.StatusBadRequest {
		t.Fatalf("incomplete answer status=%d", got)
	}
}
