package httpapi

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/masapico/minihub/internal/service"
)

func TestWriteErrorReturnsJapaneseMessages(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		status  int
		message string
	}{
		{name: "unauthenticated", err: service.ErrUnauthenticated, status: http.StatusUnauthorized, message: "ログインが必要です"},
		{name: "forbidden", err: service.ErrForbidden, status: http.StatusForbidden, message: "この操作を行う権限がありません"},
		{name: "not found", err: service.ErrNotFound, status: http.StatusNotFound, message: "指定された情報が見つかりません"},
		{name: "conflict", err: service.ErrConflict, status: http.StatusConflict, message: "IDは重複できません"},
		{name: "stale", err: service.ErrStale, status: http.StatusConflict, message: "予定調整が更新されています。再読み込みしてください"},
		{name: "invalid detail", err: fmt.Errorf("%w: 名前は必須です", service.ErrInvalid), status: http.StatusBadRequest, message: "名前は必須です"},
		{name: "invalid fallback", err: service.ErrInvalid, status: http.StatusBadRequest, message: "リクエストの内容が正しくありません"},
		{name: "internal", err: errors.New("disk unavailable"), status: http.StatusInternalServerError, message: "サーバー内部でエラーが発生しました。しばらく待ってから、もう一度お試しください"},
	}

	api := &API{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			api.writeError(recorder, test.err)
			if recorder.Code != test.status {
				t.Fatalf("status=%d want=%d", recorder.Code, test.status)
			}
			want := `{"error":"` + test.message + `"}`
			if body := strings.TrimSpace(recorder.Body.String()); body != want {
				t.Fatalf("body=%q want=%q", body, want)
			}
		})
	}
}
