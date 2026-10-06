package httpapi_test

import (
	"encoding/json"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/service"
	"net/http"
	"strings"
	"testing"
)

func TestThreadHTTPFlowAndValidation(t *testing.T) {
	h := setupAPI(t)
	request(t, h, "POST", "/api/channels", "admin", map[string]any{"id": "general", "name": "General", "type": "public"})
	r := request(t, h, "POST", "/api/channels/general/messages", "admin", map[string]string{"text": "root"})
	if r.Code != 201 {
		t.Fatal(r.Body.String())
	}
	r = request(t, h, "POST", "/api/channels/general/threads/1/messages", "admin", map[string]string{"text": "reply"})
	if r.Code != 201 {
		t.Fatal(r.Body.String())
	}
	var reply domain.Message
	if err := json.Unmarshal(r.Body.Bytes(), &reply); err != nil || reply.ThreadRootSeq != 1 {
		t.Fatalf("reply=%+v %v", reply, err)
	}
	if r := request(t, h, "PUT", "/api/channels/general/messages/2/reactions/ack", "admin", nil); r.Code != 200 {
		t.Fatalf("reply reaction=%d %s", r.Code, r.Body.String())
	}
	r = request(t, h, "GET", "/api/channels/general/reactions?messageSeqs=1,2", "admin", nil)
	var reactions []service.MessageReactions
	if err := json.Unmarshal(r.Body.Bytes(), &reactions); r.Code != 200 || err != nil || len(reactions) != 1 || reactions[0].MessageSeq != 2 || len(reactions[0].Reactions) != 1 || !reactions[0].Reactions[0].ReactedByMe {
		t.Fatalf("selected reactions=%d %s %v", r.Code, r.Body.String(), err)
	}
	for _, path := range []string{"/api/channels/general/reactions?messageSeqs=", "/api/channels/general/reactions?messageSeqs=2,2", "/api/channels/general/reactions?messageSeqs=0", "/api/channels/general/reactions?messageSeqs=2&afterMessageSeq=0"} {
		if r := request(t, h, "GET", path, "admin", nil); r.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", path, r.Code, r.Body.String())
		}
	}
	r = request(t, h, "GET", "/api/channels/general/messages", "alice", nil)
	if r.Code != 200 || strings.Contains(r.Body.String(), `"text":"reply"`) {
		t.Fatalf("main=%s", r.Body.String())
	}
	r = request(t, h, "GET", "/api/channels/general/threads/1/messages", "alice", nil)
	var page service.ThreadPage
	if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil || len(page.Messages) != 1 || page.Root.Seq != 1 {
		t.Fatalf("thread=%s %v", r.Body.String(), err)
	}
	r = request(t, h, "GET", "/api/channels/general/thread-summaries?roots=1", "alice", nil)
	if r.Code != 200 || strings.Contains(r.Body.String(), `"text"`) {
		t.Fatalf("summary contains body: %s", r.Body.String())
	}
	for _, path := range []string{"/api/channels/general/threads/1/messages?before=2&after=0", "/api/channels/general/threads/1/messages?limit=101", "/api/channels/general/threads/0/messages", "/api/channels/general/threads/2/messages", "/api/channels/general/thread-summaries?roots=-1", "/api/threads?cursor=bad", "/api/threads?unread=bad"} {
		if r := request(t, h, "GET", path, "alice", nil); r.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", path, r.Code, r.Body.String())
		}
	}
	if r := request(t, h, "PUT", "/api/channels/general/threads/1/read", "alice", map[string]int{"seq": 2}); r.Code != 204 {
		t.Fatalf("read=%d %s", r.Code, r.Body.String())
	}
	if r := request(t, h, "POST", "/api/channels/general/threads/1/messages", "alice", map[string]string{"text": "unjoined"}); r.Code != 403 {
		t.Fatalf("unjoined=%d", r.Code)
	}
	if r := request(t, h, "POST", "/api/channels", "admin", map[string]any{"id": "secret", "name": "Secret", "type": "private"}); r.Code != 201 {
		t.Fatalf("private channel=%d %s", r.Code, r.Body.String())
	}
	if r := request(t, h, "GET", "/api/channels/secret/reactions?messageSeqs=1", "alice", nil); r.Code != http.StatusNotFound {
		t.Fatalf("private reactions leaked: %d %s", r.Code, r.Body.String())
	}
}
