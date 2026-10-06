package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/masapico/minihub/internal/auth"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/httpapi"
	"github.com/masapico/minihub/internal/service"
	"github.com/masapico/minihub/internal/storage"
	"github.com/masapico/minihub/internal/storage/storetest"
)

func setupAPI(t *testing.T) http.Handler {
	t.Helper()
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.User{{ID: "admin", Name: "Admin", Role: domain.RoleAdmin, Enabled: true}, {ID: "alice", Name: "Alice", Groups: []string{"sales"}, Role: domain.RoleUser, Enabled: true}} {
		user := user
		if err := store.SaveUser(context.Background(), &user); err != nil {
			t.Fatal(err)
		}
	}
	return httpapi.New(service.New(store), httpapi.HeaderAuthenticator{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func request(t *testing.T, handler http.Handler, method, path, user string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		raw = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, raw)
	if user != "" {
		req.Header.Set("X-User-ID", user)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestChannelAndMessageHTTPFlow(t *testing.T) {
	handler := setupAPI(t)
	created := request(t, handler, http.MethodPost, "/api/channels", "admin", map[string]any{"id": "general", "name": "General", "type": "public"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	if got := request(t, handler, http.MethodPost, "/api/channels/general/messages", "alice", map[string]string{"text": "hello"}).Code; got != http.StatusForbidden {
		t.Fatalf("post before join status=%d", got)
	}
	joined := request(t, handler, http.MethodPost, "/api/channels/general/join", "alice", nil)
	if joined.Code != http.StatusOK {
		t.Fatalf("join status=%d body=%s", joined.Code, joined.Body.String())
	}
	posted := request(t, handler, http.MethodPost, "/api/channels/general/messages", "alice", map[string]string{"text": "hello"})
	if posted.Code != http.StatusCreated {
		t.Fatalf("post status=%d body=%s", posted.Code, posted.Body.String())
	}
	if got := request(t, handler, http.MethodGet, "/api/channels/general/messages?after=0&limit=100", "alice", nil).Code; got != http.StatusOK {
		t.Fatalf("history status=%d", got)
	}
	status := request(t, handler, http.MethodGet, "/api/channels/general/read", "alice", nil)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"unreadCount":0`) {
		t.Fatalf("read status=%d body=%s", status.Code, status.Body.String())
	}
	statuses := request(t, handler, http.MethodGet, "/api/channels/read-statuses", "alice", nil)
	if statuses.Code != http.StatusOK || !strings.Contains(statuses.Body.String(), `"statuses":{"general":{"lastReadSeq":1,"latestSeq":1,"unreadCount":0}}`) {
		t.Fatalf("read statuses=%d body=%s", statuses.Code, statuses.Body.String())
	}
}

func TestChannelManagementHTTPFlow(t *testing.T) {
	handler := setupAPI(t)
	created := request(t, handler, http.MethodPost, "/api/channels", "admin", map[string]any{"id": "secret", "name": "Secret", "type": "private"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	delegated := request(t, handler, http.MethodPatch, "/api/channels/secret/members", "admin", map[string]any{"addManagers": []string{"alice"}, "removeManagers": []string{"admin"}})
	if delegated.Code != http.StatusOK || !strings.Contains(delegated.Body.String(), `"managers":["alice"]`) {
		t.Fatalf("delegate=%d %s", delegated.Code, delegated.Body.String())
	}
	if got := request(t, handler, http.MethodGet, "/api/channels/secret/management", "alice", nil).Code; got != http.StatusOK {
		t.Fatalf("manager detail=%d", got)
	}
	if got := request(t, handler, http.MethodPatch, "/api/channels/secret", "alice", map[string]any{"name": "Nope"}).Code; got != http.StatusForbidden {
		t.Fatalf("manager settings=%d", got)
	}
	if got := request(t, handler, http.MethodPost, "/api/channels/secret/archive", "alice", nil).Code; got != http.StatusForbidden {
		t.Fatalf("manager archive=%d", got)
	}
	if got := request(t, handler, http.MethodPost, "/api/channels/secret/archive", "admin", nil).Code; got != http.StatusNoContent {
		t.Fatalf("admin archive=%d", got)
	}
	if got := request(t, handler, http.MethodGet, "/api/channels/secret", "alice", nil).Code; got != http.StatusNotFound {
		t.Fatalf("archived detail=%d", got)
	}
	managed := request(t, handler, http.MethodGet, "/api/channels?management=true&includeArchived=true", "admin", nil)
	if managed.Code != http.StatusOK || !strings.Contains(managed.Body.String(), `"archivedAt"`) {
		t.Fatalf("archived list=%d %s", managed.Code, managed.Body.String())
	}
	if got := request(t, handler, http.MethodPost, "/api/channels/secret/restore", "admin", nil).Code; got != http.StatusNoContent {
		t.Fatalf("restore=%d", got)
	}
}

func TestMessagePagingHTTP(t *testing.T) {
	handler := setupAPI(t)
	request(t, handler, http.MethodPost, "/api/channels", "admin", map[string]any{"id": "general", "name": "General", "type": "public", "members": []string{"alice"}})
	for i := 1; i <= 5; i++ {
		response := request(t, handler, http.MethodPost, "/api/channels/general/messages", "alice", map[string]string{"text": fmt.Sprintf("message %d", i)})
		if response.Code != http.StatusCreated {
			t.Fatalf("post %d status=%d body=%s", i, response.Code, response.Body.String())
		}
	}

	latest := request(t, handler, http.MethodGet, "/api/channels/general/messages?limit=2", "alice", nil)
	var page storage.MessagePage
	if latest.Code != http.StatusOK || json.Unmarshal(latest.Body.Bytes(), &page) != nil {
		t.Fatalf("latest status=%d body=%s", latest.Code, latest.Body.String())
	}
	if len(page.Messages) != 2 || page.Messages[0].Seq != 4 || page.Messages[1].Seq != 5 || page.NextBefore == nil || *page.NextBefore != 4 || page.NextAfter != nil {
		t.Fatalf("unexpected latest page: %#v", page)
	}

	before := request(t, handler, http.MethodGet, "/api/channels/general/messages?before=4&limit=2", "alice", nil)
	if before.Code != http.StatusOK || json.Unmarshal(before.Body.Bytes(), &page) != nil || len(page.Messages) != 2 || page.Messages[0].Seq != 2 || page.Messages[1].Seq != 3 {
		t.Fatalf("unexpected before page: status=%d body=%s", before.Code, before.Body.String())
	}
	after := request(t, handler, http.MethodGet, "/api/channels/general/messages?after=0&limit=2", "alice", nil)
	if after.Code != http.StatusOK || json.Unmarshal(after.Body.Bytes(), &page) != nil || page.NextAfter == nil || *page.NextAfter != 2 {
		t.Fatalf("unexpected after page: status=%d body=%s", after.Code, after.Body.String())
	}
	around := request(t, handler, http.MethodGet, "/api/channels/general/messages?around=3&limit=3", "alice", nil)
	if around.Code != http.StatusOK || json.Unmarshal(around.Body.Bytes(), &page) != nil || len(page.Messages) != 3 || page.Messages[0].Seq != 2 || page.Messages[1].Seq != 3 || page.Messages[2].Seq != 4 {
		t.Fatalf("unexpected around page: status=%d body=%s", around.Code, around.Body.String())
	}
	for _, path := range []string{
		"/api/channels/general/messages?before=4&after=1",
		"/api/channels/general/messages?before=4&around=3",
		"/api/channels/general/messages?before=0",
		"/api/channels/general/messages?after=-1",
		"/api/channels/general/messages?limit=501",
	} {
		if response := request(t, handler, http.MethodGet, path, "alice", nil); response.Code != http.StatusBadRequest {
			t.Errorf("GET %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestReactionHTTPFlow(t *testing.T) {
	handler := setupAPI(t)
	request(t, handler, http.MethodPost, "/api/channels", "admin", map[string]any{"id": "general", "name": "General", "type": "public", "members": []string{"alice"}})
	posted := request(t, handler, http.MethodPost, "/api/channels/general/messages", "alice", map[string]string{"text": "hello"})
	if posted.Code != http.StatusCreated {
		t.Fatalf("post status=%d body=%s", posted.Code, posted.Body.String())
	}
	added := request(t, handler, http.MethodPut, "/api/channels/general/messages/1/reactions/ack", "alice", nil)
	if added.Code != http.StatusOK || !strings.Contains(added.Body.String(), `"count":1`) || !strings.Contains(added.Body.String(), `"reactedByMe":true`) {
		t.Fatalf("add status=%d body=%s", added.Code, added.Body.String())
	}
	listed := request(t, handler, http.MethodGet, "/api/channels/general/reactions?afterMessageSeq=0", "alice", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"messageSeq":1`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	users := request(t, handler, http.MethodGet, "/api/channels/general/messages/1/reactions/ack/users", "alice", nil)
	if users.Code != http.StatusOK || users.Body.String() != "{\"userIds\":[\"alice\"]}\n" {
		t.Fatalf("users status=%d body=%s", users.Code, users.Body.String())
	}
	invalid := request(t, handler, http.MethodGet, "/api/channels/general/messages/1/reactions/party/users", "alice", nil)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid key status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	removed := request(t, handler, http.MethodDelete, "/api/channels/general/messages/1/reactions/ack", "alice", nil)
	if removed.Code != http.StatusOK || !strings.Contains(removed.Body.String(), `"reactions":[]`) {
		t.Fatalf("remove status=%d body=%s", removed.Code, removed.Body.String())
	}
	users = request(t, handler, http.MethodGet, "/api/channels/general/messages/1/reactions/ack/users", "alice", nil)
	if users.Code != http.StatusOK || users.Body.String() != "{\"userIds\":[]}\n" {
		t.Fatalf("removed users status=%d body=%s", users.Code, users.Body.String())
	}
}

func TestHTTPAuthenticationAndAdminAuthorization(t *testing.T) {
	handler := setupAPI(t)
	if got := request(t, handler, http.MethodGet, "/api/me", "", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("missing identity status=%d", got)
	}
	got := request(t, handler, http.MethodPost, "/api/channels", "alice", map[string]any{"id": "x", "name": "X", "type": "public"}).Code
	if got != http.StatusForbidden {
		t.Fatalf("non-admin create status=%d", got)
	}
}

func TestDuplicateIDsUseFriendlyConsistentMessage(t *testing.T) {
	handler := setupAPI(t)
	want := `"error":"IDは重複できません"`

	request(t, handler, http.MethodPost, "/api/groups", "admin", map[string]string{"id": "sales", "name": "営業部"})
	request(t, handler, http.MethodPost, "/api/channels", "admin", map[string]any{"id": "general", "name": "全般", "type": "public"})

	cases := []struct {
		name string
		path string
		body any
	}{
		{name: "user", path: "/api/users", body: map[string]any{"users": []map[string]any{{"id": "alice", "name": "Alice 2", "password": "password-123"}}}},
		{name: "users in same request", path: "/api/users", body: map[string]any{"users": []map[string]any{{"id": "new-user", "name": "New 1", "password": "password-123"}, {"id": "new-user", "name": "New 2", "password": "password-123"}}}},
		{name: "group", path: "/api/groups", body: map[string]string{"id": "sales", "name": "営業部2"}},
		{name: "channel", path: "/api/channels", body: map[string]any{"id": "general", "name": "全般2", "type": "public"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := request(t, handler, http.MethodPost, tc.path, "admin", tc.body)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), want) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestUserManagementHTTP(t *testing.T) {
	handler := setupAPI(t)
	request(t, handler, http.MethodPost, "/api/groups", "admin", map[string]string{"id": "sales", "name": "Sales"})
	body := map[string]any{"users": []map[string]any{{"id": "bob", "name": "Bob", "password": "temporary-pass", "role": "user", "groups": []string{"sales"}}}}
	created := request(t, handler, http.MethodPost, "/api/users", "admin", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "passwordHash") || strings.Contains(created.Body.String(), "temporary-pass") {
		t.Fatal("credential leaked")
	}
	listed := request(t, handler, http.MethodGet, "/api/users", "admin", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"id":"bob"`) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	if got := request(t, handler, http.MethodGet, "/api/users", "alice", nil).Code; got != http.StatusForbidden {
		t.Fatalf("non-admin list status=%d", got)
	}
	updated := request(t, handler, http.MethodPut, "/api/users/bob", "admin", map[string]any{
		"name": "Robert", "password": "replacement-pass", "role": "admin", "enabled": true,
	})
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"name":"Robert"`) || !strings.Contains(updated.Body.String(), `"role":"admin"`) {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	if strings.Contains(updated.Body.String(), "password") {
		t.Fatal("updated credential leaked")
	}
	if got := request(t, handler, http.MethodPut, "/api/users/bob", "alice", map[string]any{"name": "Denied", "role": "user", "enabled": true}).Code; got != http.StatusForbidden {
		t.Fatalf("non-admin update status=%d", got)
	}
}

func TestGroupManagementAndChannelAssignmentHTTP(t *testing.T) {
	handler := setupAPI(t)
	created := request(t, handler, http.MethodPost, "/api/groups", "admin", map[string]string{"id": "sales", "name": "営業部"})
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"name":"営業部"`) {
		t.Fatalf("create group status=%d body=%s", created.Code, created.Body.String())
	}
	if got := request(t, handler, http.MethodPost, "/api/groups", "alice", map[string]string{"id": "it", "name": "情報システム部"}).Code; got != http.StatusForbidden {
		t.Fatalf("non-admin create group status=%d", got)
	}
	listed := request(t, handler, http.MethodGet, "/api/groups", "alice", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"id":"sales"`) {
		t.Fatalf("list groups status=%d body=%s", listed.Code, listed.Body.String())
	}
	channel := request(t, handler, http.MethodPost, "/api/channels", "admin", map[string]any{"id": "sales-room", "name": "営業部", "type": "private", "groups": []string{"sales"}})
	if channel.Code != http.StatusCreated || !strings.Contains(channel.Body.String(), `"groups":["sales"]`) {
		t.Fatalf("create grouped channel status=%d body=%s", channel.Code, channel.Body.String())
	}
	detail := request(t, handler, http.MethodGet, "/api/channels/sales-room", "alice", nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"effectiveMembers":[`) || !strings.Contains(detail.Body.String(), `"name":"Alice"`) || !strings.Contains(detail.Body.String(), `"viaGroups":["sales"]`) {
		t.Fatalf("group-derived channel members status=%d body=%s", detail.Code, detail.Body.String())
	}
	groupDetail := request(t, handler, http.MethodGet, "/api/groups/sales", "admin", nil)
	if groupDetail.Code != http.StatusOK || !strings.Contains(groupDetail.Body.String(), `"members":[`) || !strings.Contains(groupDetail.Body.String(), `"channels":[`) {
		t.Fatalf("group detail status=%d body=%s", groupDetail.Code, groupDetail.Body.String())
	}
	if got := request(t, handler, http.MethodGet, "/api/groups/sales", "alice", nil).Code; got != http.StatusForbidden {
		t.Fatalf("non-admin group detail status=%d", got)
	}
	changed := request(t, handler, http.MethodPatch, "/api/groups/sales/members", "admin", map[string]any{"add": []string{"admin"}, "remove": []string{"alice"}})
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `"id":"admin"`) || strings.Contains(changed.Body.String(), `"id":"alice"`) {
		t.Fatalf("change members status=%d body=%s", changed.Code, changed.Body.String())
	}
	legacyChange := request(t, handler, http.MethodPut, "/api/users/admin", "admin", map[string]any{
		"name": "Admin", "role": "admin", "enabled": true, "groups": []string{"other"},
	})
	if legacyChange.Code != http.StatusBadRequest {
		t.Fatalf("legacy group change status=%d body=%s", legacyChange.Code, legacyChange.Body.String())
	}
	deleted := request(t, handler, http.MethodDelete, "/api/groups/sales", "admin", nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete group status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if got := request(t, handler, http.MethodGet, "/api/groups/sales", "admin", nil).Code; got != http.StatusNotFound {
		t.Fatalf("deleted group status=%d", got)
	}
}

func TestUserDirectoryIsAvailableWithoutLeakingPrivateFields(t *testing.T) {
	handler := setupAPI(t)
	listed := request(t, handler, http.MethodGet, "/api/users/directory", "alice", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"name":"Admin"`) {
		t.Fatalf("directory status=%d body=%s", listed.Code, listed.Body.String())
	}
	for _, private := range []string{"passwordHash", "groups", "role", "enabled"} {
		if strings.Contains(listed.Body.String(), private) {
			t.Fatalf("directory leaked %s", private)
		}
	}
}

func TestSessionLoginAndCSRFProtection(t *testing.T) {
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveUser(context.Background(), &domain.User{ID: "admin", Name: "Admin", Role: domain.RoleAdmin, Enabled: true, PasswordHash: string(hash)}); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewManager(store, false)
	handler := httpapi.New(service.New(store), sessions, slog.New(slog.NewTextHandler(io.Discard, nil)))

	login := request(t, handler, http.MethodPost, "/api/auth/login", "", map[string]string{"userId": "admin", "password": "correct-password"})
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	if strings.Contains(login.Body.String(), "passwordHash") {
		t.Fatal("password hash leaked in response")
	}
	var loginBody struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &loginBody); err != nil {
		t.Fatal(err)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatal("secure session cookie missing")
	}

	createBody, _ := json.Marshal(map[string]string{"id": "general", "name": "General", "type": "public"})
	withoutCSRF := httptest.NewRequest(http.MethodPost, "/api/channels", bytes.NewReader(createBody))
	withoutCSRF.AddCookie(cookies[0])
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, withoutCSRF)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", denied.Code)
	}

	withCSRF := httptest.NewRequest(http.MethodPost, "/api/channels", bytes.NewReader(createBody))
	withCSRF.AddCookie(cookies[0])
	withCSRF.Header.Set("X-CSRF-Token", loginBody.CSRFToken)
	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, withCSRF)
	if allowed.Code != http.StatusCreated {
		t.Fatalf("valid CSRF status=%d body=%s", allowed.Code, allowed.Body.String())
	}
}

func TestSelfPasswordChangeKeepsCurrentSessionAndRevokesOthers(t *testing.T) {
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.MinCost)
	if err := store.SaveUser(context.Background(), &domain.User{ID: "alice", Name: "Alice", Role: domain.RoleUser, Enabled: true, PasswordHash: string(hash)}); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewManager(store, false)
	handler := httpapi.New(service.NewWithOptions(store, true, 365*24*time.Hour), sessions, slog.New(slog.NewTextHandler(io.Discard, nil)))
	login := func() (*http.Cookie, string) {
		response := request(t, handler, http.MethodPost, "/api/auth/login", "", map[string]string{"userId": "alice", "password": "old-password"})
		var body struct {
			CSRF string `json:"csrfToken"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		return response.Result().Cookies()[0], body.CSRF
	}
	current, csrf := login()
	other, _ := login()
	body, _ := json.Marshal(map[string]string{"currentPassword": "old-password", "newPassword": "new-password"})
	req := httptest.NewRequest(http.MethodPut, "/api/me/password", bytes.NewReader(body))
	req.AddCookie(current)
	req.Header.Set("X-CSRF-Token", csrf)
	changed := httptest.NewRecorder()
	handler.ServeHTTP(changed, req)
	if changed.Code != http.StatusNoContent {
		t.Fatalf("change status=%d body=%s", changed.Code, changed.Body.String())
	}
	for name, cookie := range map[string]*http.Cookie{"current": current, "other": other} {
		check := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		check.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, check)
		want := http.StatusOK
		if name == "other" {
			want = http.StatusUnauthorized
		}
		if response.Code != want {
			t.Fatalf("%s session status=%d want=%d", name, response.Code, want)
		}
	}
}
