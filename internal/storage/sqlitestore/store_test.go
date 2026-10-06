package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
	"github.com/masapico/minihub/internal/storage/filestore"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "chat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}
func TestConcurrentCommitAndRead(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 80; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.AddMessage(ctx, "c", domain.Message{UserID: "u", Text: fmt.Sprint(i)}); err != nil {
				t.Error(err)
			}
			if err := s.SetReadState(ctx, "u", "c", int64(i)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	page, err := s.GetMessages(ctx, "c", storage.MessageQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 80 {
		t.Fatal(len(page.Messages))
	}
	for i, m := range page.Messages {
		if m.Seq != int64(i+1) {
			t.Fatal(m.Seq)
		}
	}
	seq, err := s.GetReadState(ctx, "u", "c")
	if err != nil || seq != 79 {
		t.Fatal(seq, err)
	}
	// Deletion must never reset the allocation watermark.
	if _, err = s.write.Exec("DELETE FROM messages WHERE channel_id='c' AND seq=80"); err != nil {
		t.Fatal(err)
	}
	m, err := s.AddMessage(ctx, "c", domain.Message{UserID: "u", Text: "after deletion"})
	if err != nil || m.Seq != 81 {
		t.Fatal(m, err)
	}
	// Failed duplicate insertion rolls back its sequence allocation.
	if _, err = s.AddMessage(ctx, "c", domain.Message{ID: m.ID, UserID: "u", Text: "duplicate"}); err == nil {
		t.Fatal("duplicate accepted")
	}
	m, err = s.AddMessage(ctx, "c", domain.Message{UserID: "u", Text: "next"})
	if err != nil || m.Seq != 82 {
		t.Fatal(m, err)
	}
}
func TestPagesAndStateMatchFile(t *testing.T) {
	ctx := context.Background()
	f, err := filestore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := testStore(t)
	stores := []storage.Storage{f, s}
	for _, st := range stores {
		for _, u := range []string{"u", "v"} {
			if err = st.SaveUser(ctx, &domain.User{ID: u, Groups: []string{"g"}}); err != nil {
				t.Fatal(err)
			}
		}
		if err = st.SaveChannel(ctx, &domain.Channel{ID: "c"}); err != nil {
			t.Fatal(err)
		}
		for i := int64(1); i <= 80; i++ {
			root := int64(0)
			if i%4 == 0 {
				root = 1
			}
			user := "u"
			if i%3 == 0 {
				user = "v"
			}
			m := domain.Message{ID: fmt.Sprintf("m%d", i), UserID: user, Text: "hello @v @group:g", ThreadRootSeq: root, Timestamp: time.Date(2026, 1, 1, 0, 0, int(90-i), 0, time.UTC)}
			if i%5 == 0 {
				m.MentionUserIDs = []string{"v"}
			}
			if _, err = st.AddMessage(ctx, "c", m); err != nil {
				t.Fatal(err)
			}
		}
		if err = st.SetThreadReadState(ctx, "u", "c", 1, 16); err != nil {
			t.Fatal(err)
		}
		if err = st.SetMentionRead(ctx, "v", "m1", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	for _, root := range []int64{0, 1} {
		for _, limit := range []int{1, 2, 7, 20} {
			for _, seq := range []int64{1, 8, 17, 80, 100} {
				for _, mode := range []string{"latest", "before", "after", "around"} {
					q := storage.MessageQuery{ThreadRootSeq: root, Limit: limit}
					switch mode {
					case "before":
						q.BeforeSeq = &seq
					case "after":
						q.AfterSeq = &seq
					case "around":
						q.AroundSeq = &seq
					}
					a, e := f.GetMessages(ctx, "c", q)
					b, e2 := s.GetMessages(ctx, "c", q)
					if e != nil || e2 != nil || !reflect.DeepEqual(a, b) {
						t.Fatalf("root=%d limit=%d seq=%d mode=%s: %v / %v\n%+v\n%+v", root, limit, seq, mode, e, e2, a, b)
					}
				}
			}
		}
	}
	for _, user := range []string{"u", "v"} {
		for _, filter := range []storage.ThreadFilter{{}, {Roots: []int64{1}}, {Participating: true}, {Roots: []int64{70}}} {
			a, e := f.ThreadSummaries(ctx, user, "c", filter)
			b, e2 := s.ThreadSummaries(ctx, user, "c", filter)
			if e != nil || e2 != nil || !reflect.DeepEqual(a, b) {
				t.Fatalf("summaries: %+v %+v %v %v", a, b, e, e2)
			}
		}
	}
	a, e := f.ListMentions(ctx, "v")
	b, e2 := s.ListMentions(ctx, "v")
	slices.SortFunc(a, func(a, b domain.Mention) int { return int(a.Message.Seq - b.Message.Seq) })
	slices.SortFunc(b, func(a, b domain.Mention) int { return int(a.Message.Seq - b.Message.Seq) })
	if e != nil || e2 != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("mentions differ %d/%d %v/%v", len(a), len(b), e, e2)
	}
}
func TestCrashRecovery(t *testing.T) {
	if path := os.Getenv("MINIHUB_CRASH_DB"); path != "" {
		s, err := Open(path)
		if err != nil {
			os.Exit(2)
		}
		if _, err = s.AddMessage(context.Background(), "c", domain.Message{UserID: "u", Text: "committed"}); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "crash.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashRecovery$")
	cmd.Env = append(os.Environ(), "MINIHUB_CRASH_DB="+path)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := s.GetMessage(context.Background(), "c", 1)
	if err != nil || m.Text != "committed" {
		t.Fatal(m, err)
	}
	m, err = s.AddMessage(context.Background(), "c", domain.Message{UserID: "u", Text: "next"})
	if err != nil || m.Seq != 2 {
		t.Fatal(m, err)
	}
}
func TestCancellationAndSchemaGuard(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.AddMessage(ctx, "c", domain.Message{UserID: "u", Text: "cancelled"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	seq, err := s.LatestMessageSeq(context.Background(), "c")
	if err != nil || seq != 0 {
		t.Fatal(seq, err)
	}
	if _, err = s.write.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	var path string
	if err = s.read.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(path); err == nil {
		t.Fatal("future schema opened")
	}
}
func TestTransactionFailureLeavesState(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.SetReadState(ctx, "u", "c", 5); err != nil {
		t.Fatal(err)
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE read_state SET last_seq=8"); err != nil {
			return err
		}
		return errors.New("injected failure")
	})
	if err == nil {
		t.Fatal("missing failure")
	}
	n, e := s.GetReadState(ctx, "u", "c")
	if n != 5 || e != nil {
		t.Fatal(n, e)
	}
}

func TestExpiredSessionCleanupKeepsInvalidRecords(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	for id, expires := range map[string]time.Time{"expired": now, "valid": now.Add(time.Hour), "missing": {}} {
		if err := s.SaveSession(ctx, &domain.Session{TokenHash: id, ExpiresAt: expires}); err != nil {
			t.Fatal(err)
		}
	}
	count, err := s.DeleteExpiredSessions(ctx, now)
	if count != 1 || err == nil {
		t.Fatal(count, err)
	}
	if _, err = s.GetSession(ctx, "expired"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err = s.GetSession(ctx, "missing"); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseSettingsAndSnapshotReads(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var mode string
	var sync, foreign int
	if err := s.write.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := s.write.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatal(err)
	}
	if err := s.write.QueryRow("PRAGMA foreign_keys").Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" || sync != 2 || foreign != 1 {
		t.Fatal(mode, sync, foreign)
	}
	if _, err := s.AddMessage(ctx, "c", domain.Message{UserID: "u", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM messages").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if _, err = s.AddMessage(ctx, "c", domain.Message{UserID: "u", Text: "second"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow("SELECT COUNT(*) FROM messages").Scan(&count); err != nil || count != 1 {
		t.Fatal("read snapshot changed", count, err)
	}
	if _, err = tx.Exec("DELETE FROM messages"); err == nil {
		t.Fatal("read pool allowed mutation")
	}
}
