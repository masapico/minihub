package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/httpapi"
	"github.com/masapico/minihub/internal/service"
	"github.com/masapico/minihub/internal/storage/storetest"
)

func TestAttachmentUploadAndMessagePostHTTP(t *testing.T) {
	ctx := context.Background()
	store, err := storetest.New(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []domain.User{
		{ID: "admin", Name: "Admin", Role: domain.RoleAdmin, Enabled: true},
		{ID: "alice", Name: "Alice", Role: domain.RoleUser, Enabled: true},
	} {
		if err := store.SaveUser(ctx, &u); err != nil {
			t.Fatal(err)
		}
	}
	svc := service.New(store)
	svc.SetAttachmentsDir(filepath.Join(t.TempDir(), "attachments"))
	if _, err := svc.CreateChannel(ctx, "admin", domain.Channel{
		ID:      "general",
		Name:    "General",
		Type:    domain.ChannelPublic,
		Members: []string{"alice"},
	}); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := httpapi.New(svc, httpapi.HeaderAuthenticator{}, logger)

	// 1. Upload attachment via POST /api/channels/general/attachments
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	part, err := w.CreateFormFile("file", "sales_report.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("excel binary data")); err != nil {
		t.Fatal(err)
	}
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/channels/general/attachments", &b)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-User-ID", "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("upload failed: status %d, body: %s", rec.Code, rec.Body.String())
	}

	var summary service.AttachmentSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("unmarshal upload summary: %v", err)
	}
	if summary.ID == "" || summary.Filename != "sales_report.xlsx" {
		t.Fatalf("unexpected summary: %+v", summary)
	}

	// 2. Post message with attachment but without @ai: mention -> MUST fail 400
	noAIMsg := map[string]any{
		"text":          "Check this out",
		"attachmentIds": []string{summary.ID},
	}
	noAIBody, _ := json.Marshal(noAIMsg)
	reqNoAI := httptest.NewRequest(http.MethodPost, "/api/channels/general/messages", bytes.NewReader(noAIBody))
	reqNoAI.Header.Set("Content-Type", "application/json")
	reqNoAI.Header.Set("X-User-ID", "alice")
	recNoAI := httptest.NewRecorder()
	handler.ServeHTTP(recNoAI, reqNoAI)

	if recNoAI.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when posting attachment without AI mention, got: %d (%s)", recNoAI.Code, recNoAI.Body.String())
	}

	// 3. Post message with attachment AND @ai: mention -> MUST succeed 201
	aiMsg := map[string]any{
		"text":          "@ai:helper Analyze this sales report",
		"attachmentIds": []string{summary.ID},
	}
	aiBody, _ := json.Marshal(aiMsg)
	reqAI := httptest.NewRequest(http.MethodPost, "/api/channels/general/messages", bytes.NewReader(aiBody))
	reqAI.Header.Set("Content-Type", "application/json")
	reqAI.Header.Set("X-User-ID", "alice")
	recAI := httptest.NewRecorder()
	handler.ServeHTTP(recAI, reqAI)

	if recAI.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created with AI mention, got: %d (%s)", recAI.Code, recAI.Body.String())
	}

	var created domain.Message
	if err := json.Unmarshal(recAI.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created message: %v", err)
	}
	if len(created.Attachments) != 1 || created.Attachments[0].Filename != "sales_report.xlsx" {
		t.Fatalf("unexpected message attachments: %+v", created.Attachments)
	}
}
