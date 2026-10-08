package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/masapico/minihub/internal/domain"
)

const (
	MaxAttachmentFileBytes  = 20 * 1024 * 1024 // 20 MiB
	MaxAttachmentTotalBytes = 50 * 1024 * 1024 // 50 MiB
	MaxAttachmentCount      = 5
)

var allowedAttachmentExtensions = map[string]bool{
	".xlsx": true,
	".xls":  true,
	".docx": true,
	".doc":  true,
	".pptx": true,
	".ppt":  true,
	".pdf":  true,
	".txt":  true,
	".csv":  true,
	".tsv":  true,
	".json": true,
	".md":   true,
}

type StagedAttachment struct {
	ID        string
	ChannelID string
	UserID    string
	Filename  string
	Size      int64
	Path      string
	CreatedAt time.Time
}

type AttachmentSummary struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

type AttachmentManager struct {
	dir    string
	mu     sync.Mutex
	staged map[string]StagedAttachment
}

func newAttachmentManager(dir string) *AttachmentManager {
	if dir == "" {
		dir = filepath.Join("data", "attachments")
	}
	return &AttachmentManager{
		dir:    dir,
		staged: make(map[string]StagedAttachment),
	}
}

func (s *Service) SetAttachmentsDir(dir string) {
	s.attachmentMgr.mu.Lock()
	defer s.attachmentMgr.mu.Unlock()
	s.attachmentMgr.dir = dir
}

func (s *Service) AttachmentsDir() string {
	s.attachmentMgr.mu.Lock()
	defer s.attachmentMgr.mu.Unlock()
	return s.attachmentMgr.dir
}

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" || name == "\\" {
		return "attachment.bin"
	}
	// Replace control characters
	var sb strings.Builder
	for _, r := range name {
		if r < 32 || r == 127 || r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			sb.WriteRune('_')
		} else {
			sb.WriteRune(r)
		}
	}
	result := sb.String()
	if len(result) > 200 {
		ext := filepath.Ext(result)
		if len(ext) < 20 {
			result = result[:200-len(ext)] + ext
		} else {
			result = result[:200]
		}
	}
	return result
}

// StageAttachment validates and stores an uploaded attachment file in temporary stage.
func (s *Service) StageAttachment(ctx context.Context, userID, channelID, filename string, r io.Reader) (AttachmentSummary, error) {
	if err := s.canPost(ctx, userID, channelID); err != nil {
		return AttachmentSummary{}, err
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if !allowedAttachmentExtensions[ext] {
		return AttachmentSummary{}, fmt.Errorf("%w: 対応していないファイル形式です（Excel, Word, PowerPoint, PDF, テキスト/データ形式のみ添付可能）", ErrInvalid)
	}

	sanitized := sanitizeFilename(filename)
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return AttachmentSummary{}, fmt.Errorf("generate attachment id: %w", err)
	}
	id := hex.EncodeToString(randomBytes[:])

	baseDir := s.AttachmentsDir()
	now := time.Now()
	dayDir := filepath.Join(baseDir, now.Format("2006-01-02"))
	if err := os.MkdirAll(dayDir, 0o700); err != nil {
		return AttachmentSummary{}, fmt.Errorf("create attachment directory: %w", err)
	}

	destFilename := fmt.Sprintf("%s_%s", id, sanitized)
	destPath := filepath.Join(dayDir, destFilename)
	absPath, err := filepath.Abs(destPath)
	if err != nil {
		return AttachmentSummary{}, fmt.Errorf("resolve attachment absolute path: %w", err)
	}

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return AttachmentSummary{}, fmt.Errorf("create attachment file: %w", err)
	}
	defer f.Close()

	// Enforce MaxAttachmentFileBytes + 1 limit while copying
	limited := io.LimitReader(r, MaxAttachmentFileBytes+1)
	written, err := io.Copy(f, limited)
	if err != nil {
		_ = os.Remove(destPath)
		return AttachmentSummary{}, fmt.Errorf("write attachment file: %w", err)
	}
	if written > MaxAttachmentFileBytes {
		_ = os.Remove(destPath)
		return AttachmentSummary{}, fmt.Errorf("%w: ファイルサイズが上限（20 MiB）を超えています", ErrInvalid)
	}
	if written == 0 {
		_ = os.Remove(destPath)
		return AttachmentSummary{}, fmt.Errorf("%w: ファイルが空です", ErrInvalid)
	}

	// Guarantee disk flush before returning
	if err := f.Sync(); err != nil {
		_ = os.Remove(destPath)
		return AttachmentSummary{}, fmt.Errorf("sync attachment file: %w", err)
	}

	s.attachmentMgr.mu.Lock()
	defer s.attachmentMgr.mu.Unlock()

	// Prune staged attachments older than 2 hours
	cutoff := now.Add(-2 * time.Hour)
	for k, item := range s.attachmentMgr.staged {
		if item.CreatedAt.Before(cutoff) {
			delete(s.attachmentMgr.staged, k)
		}
	}

	s.attachmentMgr.staged[id] = StagedAttachment{
		ID:        id,
		ChannelID: channelID,
		UserID:    userID,
		Filename:  sanitized,
		Size:      written,
		Path:      absPath,
		CreatedAt: now,
	}

	return AttachmentSummary{
		ID:       id,
		Filename: sanitized,
		Size:     written,
	}, nil
}

// consumeStagedAttachments claims staged attachments and checks totals.
func (s *Service) consumeStagedAttachments(userID, channelID string, ids []string) ([]domain.Attachment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > MaxAttachmentCount {
		return nil, fmt.Errorf("%w: 添付ファイルは1回の投稿につき%d件までです", ErrInvalid, MaxAttachmentCount)
	}

	s.attachmentMgr.mu.Lock()
	defer s.attachmentMgr.mu.Unlock()

	seen := make(map[string]bool, len(ids))
	result := make([]domain.Attachment, 0, len(ids))
	var totalSize int64

	for _, id := range ids {
		if seen[id] {
			return nil, fmt.Errorf("%w: 重複した添付ファイルIDが指定されています", ErrInvalid)
		}
		seen[id] = true

		staged, ok := s.attachmentMgr.staged[id]
		if !ok || staged.ChannelID != channelID || staged.UserID != userID {
			return nil, fmt.Errorf("%w: 添付ファイルが見つからないか、有効期限が切れています", ErrNotFound)
		}

		totalSize += staged.Size
		if totalSize > MaxAttachmentTotalBytes {
			return nil, fmt.Errorf("%w: 添付ファイルの合計サイズが上限（50 MiB）を超えています", ErrInvalid)
		}

		result = append(result, domain.Attachment{
			ID:       staged.ID,
			Filename: staged.Filename,
			Size:     staged.Size,
			Path:     staged.Path,
		})
	}

	// Remove consumed items from staging
	for _, id := range ids {
		delete(s.attachmentMgr.staged, id)
	}

	return result, nil
}

// CleanupAttachments removes attachment directories and files created before the given cutoff date (day-level).
func (s *Service) CleanupAttachments(cutoff time.Time) (int, error) {
	baseDir := s.AttachmentsDir()
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}

	cutoffDate := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, cutoff.Location())
	deleted := 0

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		d, parseErr := time.Parse("2006-01-02", name)
		if parseErr == nil {
			if d.Before(cutoffDate) {
				subPath := filepath.Join(baseDir, name)
				if err := os.RemoveAll(subPath); err == nil {
					deleted++
				}
			}
			continue
		}
		// Also support legacy YYYYMM format if any
		if ym, err := time.Parse("200601", name); err == nil {
			cutoffMonth := time.Date(cutoff.Year(), cutoff.Month(), 1, 0, 0, 0, 0, cutoff.Location())
			if ym.Before(cutoffMonth) {
				subPath := filepath.Join(baseDir, name)
				if err := os.RemoveAll(subPath); err == nil {
					deleted++
				}
			}
		}
	}
	return deleted, nil
}
