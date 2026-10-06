package backend

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/masapico/minihub/internal/auth"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
	"github.com/masapico/minihub/internal/storage/filestore"
	"github.com/masapico/minihub/internal/storage/sqlitestore"
	"golang.org/x/crypto/bcrypt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestUnrelatedDBIsUntouched(t *testing.T) {
	root, _ := fixture(t)
	path := filepath.Join(root, DBName)
	db, err := sql.Open("sqlite", path)
	must(t, err)
	_, err = db.Exec("CREATE TABLE unrelated(value TEXT); INSERT INTO unrelated VALUES('keep')")
	must(t, err)
	must(t, db.Close())
	before, err := os.ReadFile(path)
	must(t, err)
	if _, err = Migrate(context.Background(), root, false); err == nil {
		t.Fatal("unrelated DB accepted")
	}
	after, err := os.ReadFile(path)
	must(t, err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("unrelated DB modified")
	}
}

func TestCancelledMigrationCanRetry(t *testing.T) {
	root, _ := fixture(t)
	before := sourceHashes(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Migrate(ctx, root, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, sourceHashes(t, root)) {
		t.Fatal("source changed")
	}
	_, err := Migrate(context.Background(), root, false)
	must(t, err)
}

func TestWithdrawalSurvivesMigration(t *testing.T) {
	root, s := fixture(t)
	ctx := context.Background()
	value := domain.Withdrawal{Version: domain.Version, Kind: "message", ChannelID: "c", TargetID: "1", ActorID: "u", At: time.Now().UTC()}
	must(t, s.SaveWithdrawal(ctx, value))
	must(t, s.RestoreWithdrawal(ctx, "message", "c", "1", "v", time.Now().UTC()))
	value.ActorID = "v"
	value.At = time.Now().UTC()
	must(t, s.SaveWithdrawal(ctx, value))
	before, err := os.ReadFile(filepath.Join(root, "withdrawals", "c", "message", "1.json"))
	must(t, err)
	_, err = Migrate(ctx, root, false)
	must(t, err)
	h, err := Open(root, "sqlite")
	must(t, err)
	defer h.Close()
	got, err := h.GetWithdrawal(ctx, "message", "c", "1")
	must(t, err)
	if got.ActorID != "v" || !got.At.Equal(value.At) || len(got.Events) != 3 {
		t.Fatalf("migrated withdrawal=%+v", got)
	}
	after, err := os.ReadFile(filepath.Join(root, "withdrawals", "c", "message", "1.json"))
	must(t, err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("source withdrawal changed")
	}
}

func TestLoginSurvivesMigrationAndRestore(t *testing.T) {
	root, s := fixture(t)
	ctx := context.Background()
	u, err := s.GetUser(ctx, "u")
	must(t, err)
	hash, err := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	must(t, err)
	u.PasswordHash = string(hash)
	must(t, s.SaveUser(ctx, u))
	manager := auth.NewManager(s, false)
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	_, csrf, err := manager.Login(rec, request, "u", "test-password")
	must(t, err)
	cookie := rec.Result().Cookies()[0]
	_, err = Migrate(ctx, root, false)
	must(t, err)
	backup := t.TempDir()
	must(t, os.CopyFS(backup, os.DirFS(root)))
	h, err := Open(backup, "sqlite")
	must(t, err)
	defer h.Close()
	restored := auth.NewManager(h, false)
	r := httptest.NewRequest(http.MethodPost, "/api/channels", nil)
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	id, err := restored.Authenticate(r)
	must(t, err)
	if id != "u" || !restored.ValidateCSRF(r) {
		t.Fatal("authentication did not survive")
	}
}

func fixture(t *testing.T) (string, *filestore.FileStorage) {
	t.Helper()
	root := t.TempDir()
	s, err := filestore.New(root)
	must(t, err)
	ctx := context.Background()
	must(t, s.SaveGroup(ctx, &domain.Group{ID: "g", Name: "Group"}))
	for _, u := range []string{"u", "v"} {
		must(t, s.SaveUser(ctx, &domain.User{ID: u, Name: u, Groups: []string{"g"}, Enabled: true, PasswordHash: "hash", AuthGeneration: 7}))
	}
	must(t, s.SaveChannel(ctx, &domain.Channel{ID: "c", Name: "Private", Type: domain.ChannelPrivate, Members: []string{"u", "v"}, Managers: []string{"u"}, Groups: []string{"g"}}))
	for i := 0; i < 6; i++ {
		r := int64(0)
		if i%2 != 0 {
			r = 1
		}
		_, err = s.AddMessage(ctx, "c", domain.Message{UserID: "u", ThreadRootSeq: r, Text: "日本語 @v @group:g", Timestamp: time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC)})
		must(t, err)
	}
	must(t, s.SetReadState(ctx, "v", "c", 3))
	must(t, s.SetThreadReadState(ctx, "v", "c", 1, 4))
	m, err := s.GetMessage(ctx, "c", 1)
	must(t, err)
	must(t, s.SetMentionRead(ctx, "v", m.ID, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)))
	for _, active := range []bool{true, false, true} {
		_, err = s.SetReaction(ctx, "c", domain.ReactionEvent{MessageSeq: 1, UserID: "v", Key: "thanks", Active: active})
		must(t, err)
	}
	must(t, s.SaveSchedule(ctx, &domain.Schedule{ID: "sch", ChannelID: "c", Revision: 3, Title: "Meeting", CreatedBy: "u"}))
	for i := 0; i < 3; i++ {
		_, err = s.AddScheduleResponse(ctx, "sch", domain.ScheduleResponse{UserID: "u", Choices: map[string]domain.ScheduleChoice{"a": domain.ScheduleYes}})
		must(t, err)
	}
	must(t, s.SaveSession(ctx, &domain.Session{TokenHash: "tokenhash", UserID: "u", CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour), AuthGeneration: 7}))
	return root, s
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func sourceHashes(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	for _, dir := range []string{"users", "groups", "channels", "state", "sessions", "schedules"} {
		must(t, filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			out[rel] = sha256.Sum256(b)
			return nil
		}))
	}
	return out
}
func compareStores(t *testing.T, a, b storage.Storage) {
	t.Helper()
	ctx := context.Background()
	for _, root := range []int64{0, 1} {
		x, e := a.GetMessages(ctx, "c", storage.MessageQuery{Limit: 100, ThreadRootSeq: root})
		must(t, e)
		y, e := b.GetMessages(ctx, "c", storage.MessageQuery{Limit: 100, ThreadRootSeq: root})
		must(t, e)
		if !reflect.DeepEqual(x, y) {
			t.Fatalf("messages differ: %+v %+v", x, y)
		}
	}
	x, e := a.ThreadSummaries(ctx, "v", "c")
	must(t, e)
	y, e := b.ThreadSummaries(ctx, "v", "c")
	must(t, e)
	if !reflect.DeepEqual(x, y) {
		t.Fatal("thread state differs")
	}
	ma, e := a.ListMentions(ctx, "v")
	must(t, e)
	mb, e := b.ListMentions(ctx, "v")
	must(t, e)
	if !reflect.DeepEqual(ma, mb) {
		t.Fatal("mentions differ")
	}
	ra, e := a.GetReactions(ctx, "c", 0)
	must(t, e)
	rb, e := b.GetReactions(ctx, "c", 0)
	must(t, e)
	if !reflect.DeepEqual(ra, rb) {
		t.Fatal("reactions differ")
	}
	sa, e := a.ListScheduleResponses(ctx, "sch")
	must(t, e)
	sb, e := b.ListScheduleResponses(ctx, "sch")
	must(t, e)
	if !reflect.DeepEqual(sa, sb) {
		t.Fatal("schedule responses differ")
	}
	ua, e := a.GetUser(ctx, "u")
	must(t, e)
	ub, e := b.GetUser(ctx, "u")
	must(t, e)
	if !reflect.DeepEqual(ua, ub) {
		t.Fatal("user differs")
	}
	ca, e := a.GetSession(ctx, "tokenhash")
	must(t, e)
	cb, e := b.GetSession(ctx, "tokenhash")
	must(t, e)
	if !reflect.DeepEqual(ca, cb) {
		t.Fatal("session differs")
	}
	n, e := a.GetReadState(ctx, "v", "c")
	must(t, e)
	n2, e := b.GetReadState(ctx, "v", "c")
	must(t, e)
	if n != n2 {
		t.Fatal("read state differs")
	}
}
func TestMigrateAndRestore(t *testing.T) {
	root, source := fixture(t)
	ctx := context.Background()
	before := sourceHashes(t, root)
	report, err := Migrate(ctx, root, true)
	must(t, err)
	if report.Records["messages"].Count != 6 || report.Records["reaction_events"].Count != 3 || report.Records["response_events"].Count != 3 {
		t.Fatal(report)
	}
	if _, err = os.Stat(filepath.Join(root, DBName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry-run published DB")
	}
	if mark, err := readMarker(root); err != nil || mark != "" {
		t.Fatal(mark, err)
	}
	_, err = Migrate(ctx, root, false)
	must(t, err)
	if !reflect.DeepEqual(before, sourceHashes(t, root)) {
		t.Fatal("source changed")
	}
	if h, err := Open(root, "file"); err == nil {
		h.Close()
		t.Fatal("file opened after migration")
	}
	h, err := Open(root, "sqlite")
	must(t, err)
	compareStores(t, source, h)
	must(t, h.Close())
	backup := t.TempDir()
	must(t, os.CopyFS(backup, os.DirFS(root)))
	restored, err := Open(backup, "sqlite")
	must(t, err)
	defer restored.Close()
	compareStores(t, source, restored)
	m, err := restored.AddMessage(ctx, "c", domain.Message{UserID: "u", Text: "after restore"})
	must(t, err)
	if m.Seq != 7 {
		t.Fatal(m.Seq)
	}
	r, err := restored.AddScheduleResponse(ctx, "sch", domain.ScheduleResponse{UserID: "u"})
	must(t, err)
	if r.Seq != 4 {
		t.Fatal(r.Seq)
	}
}
func TestSelectionAndLocking(t *testing.T) {
	root := t.TempDir()
	h, err := Open(root, "file")
	must(t, err)
	if other, err := Open(root, "file"); err == nil {
		other.Close()
		t.Fatal("double open")
	}
	if _, err = Migrate(context.Background(), root, false); err == nil {
		t.Fatal("migrated live directory")
	}
	must(t, h.Close())
	if other, err := Open(root, "sqlite"); err == nil {
		other.Close()
		t.Fatal("configuration-only switch accepted")
	}
	root, _ = fixture(t)
	if other, err := Open(root, "sqlite"); err == nil {
		other.Close()
		t.Fatal("file data shadowed")
	}
	empty := t.TempDir()
	h, err = Open(empty, "sqlite")
	must(t, err)
	must(t, h.Close())
	must(t, os.Remove(filepath.Join(empty, DBName)))
	if h, err = Open(empty, "sqlite"); err == nil {
		h.Close()
		t.Fatal("missing DB recreated")
	}
}
func TestMigrationCorruptionAndIncompleteTail(t *testing.T) {
	for _, tc := range []struct {
		name, tail string
		ok         bool
	}{{"incomplete", "{", true}, {"corrupt", "{bad}\n", false}, {"duplicate", "duplicate", false}} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := fixture(t)
			path := filepath.Join(root, "channels", "c", "messages", "2026-01-01.jsonl")
			tail := []byte(tc.tail)
			if tc.name == "duplicate" {
				var err error
				tail, err = os.ReadFile(path)
				must(t, err)
			}
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			must(t, err)
			_, err = f.Write(tail)
			must(t, err)
			must(t, f.Close())
			before := sourceHashes(t, root)
			report, err := Migrate(context.Background(), root, false)
			if tc.ok {
				must(t, err)
				if len(report.IncompleteTails) != 1 {
					t.Fatal(report)
				}
			} else {
				if err == nil {
					t.Fatal("bad data accepted")
				}
				if _, err = os.Stat(filepath.Join(root, DBName)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed DB published")
				}
			}
			if !reflect.DeepEqual(before, sourceHashes(t, root)) {
				t.Fatal("source changed")
			}
		})
	}
}
func TestInterruptedMarkerPublicationRecovery(t *testing.T) {
	root, _ := fixture(t)
	_, err := Migrate(context.Background(), root, false)
	must(t, err)
	must(t, writeMarker(root, "file"))
	if h, err := Open(root, "file"); err == nil {
		h.Close()
		t.Fatal("opened retained source")
	}
	_, err = Migrate(context.Background(), root, false)
	must(t, err)
	h, err := Open(root, "sqlite")
	must(t, err)
	must(t, h.Close())
}
func TestMigrationRejectsUnrelatedDBAndReferences(t *testing.T) {
	root, _ := fixture(t)
	s, err := sqlitestore.Open(filepath.Join(root, DBName))
	must(t, err)
	must(t, s.Close())
	if _, err = Migrate(context.Background(), root, false); err == nil {
		t.Fatal("unrelated DB adopted")
	}
	root, _ = fixture(t)
	path := filepath.Join(root, "channels", "c", "meta.json")
	b, err := os.ReadFile(path)
	must(t, err)
	var c domain.Channel
	must(t, json.Unmarshal(b, &c))
	c.Members = append(c.Members, "missing")
	b, err = json.Marshal(c)
	must(t, err)
	must(t, os.WriteFile(path, b, 0600))
	if _, err = Migrate(context.Background(), root, false); err == nil {
		t.Fatal("broken member reference accepted")
	}
}
func TestFileBackupRestore(t *testing.T) {
	root, source := fixture(t)
	copyRoot := t.TempDir()
	must(t, os.CopyFS(copyRoot, os.DirFS(root)))
	h, err := Open(copyRoot, "file")
	must(t, err)
	defer h.Close()
	compareStores(t, source, h)
}
