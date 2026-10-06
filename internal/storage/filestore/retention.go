package filestore

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/masapico/minihub/internal/domain"
	"github.com/masapico/minihub/internal/storage"
)

const RetentionJournal = ".retention-pending.json"

type retentionChannel struct {
	Messages map[int64]string `json:"messages"`
	Roots    []int64          `json:"roots"`
}
type retentionPlan struct {
	Report    storage.RetentionReport     `json:"report"`
	Channels  map[string]retentionChannel `json:"channels"`
	Polls     map[string]string           `json:"polls"`
	Schedules map[string]string           `json:"schedules"`
}
type retentionEntry struct {
	id       string
	ts       time.Time
	root     int64
	poll     string
	schedule string
}

func (s *FileStorage) PruneRetention(ctx context.Context, cutoff time.Time, apply bool) (storage.RetentionReport, error) {
	path := filepath.Join(s.root, RetentionJournal)
	var plan retentionPlan
	if err := readJSON(ctx, path, &plan); err == nil {
		if !plan.Report.Cutoff.Equal(cutoff) {
			return storage.RetentionReport{}, fmt.Errorf("unfinished retention run uses cutoff %s; rerun with -before %s", plan.Report.Cutoff.Format("2006-01-02"), plan.Report.Cutoff.Format("2006-01-02"))
		}
		if !apply {
			return plan.Report, nil
		}
		if err := s.applyRetention(ctx, plan); err != nil {
			return plan.Report, err
		}
		return plan.Report, os.Remove(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return storage.RetentionReport{}, err
	}
	var err error
	plan, err = s.planRetention(ctx, cutoff)
	if err != nil || !apply {
		return plan.Report, err
	}
	// Preserve the highest allocated sequence before removing the last record.
	for ch := range plan.Channels {
		idx, e := s.positionsLocked(ctx, ch)
		if e != nil {
			return plan.Report, e
		}
		if e = atomicJSON(ctx, filepath.Join(s.root, "channels", ch, "sequence.json"), struct {
			LastSeq int64 `json:"lastSeq"`
		}{idx.latest}); e != nil {
			return plan.Report, e
		}
	}
	if err = atomicJSON(ctx, path, plan); err != nil {
		return plan.Report, err
	}
	if err = s.applyRetention(ctx, plan); err != nil {
		return plan.Report, err
	}
	return plan.Report, os.Remove(path)
}

func (s *FileStorage) planRetention(ctx context.Context, cutoff time.Time) (retentionPlan, error) {
	plan := retentionPlan{Report: storage.RetentionReport{Cutoff: cutoff}, Channels: map[string]retentionChannel{}, Polls: map[string]string{}, Schedules: map[string]string{}}
	channels, err := s.ListChannels(ctx)
	if err != nil {
		return plan, err
	}
	for _, ch := range channels {
		if err := checkID("channel", ch.ID); err != nil {
			return plan, err
		}
		idx, e := s.positionsLocked(ctx, ch.ID)
		if e != nil {
			return plan, e
		}
		all := make(map[int64]retentionEntry, len(idx.bySeq))
		for seq, pos := range idx.bySeq {
			m, e := readPosition(pos)
			if e != nil {
				return plan, e
			}
			if m.PollRef != nil {
				if e = checkID("poll", m.PollRef.ID); e != nil {
					return plan, e
				}
			}
			if m.ScheduleRef != nil {
				if e = checkID("schedule", m.ScheduleRef.ID); e != nil {
					return plan, e
				}
			}
			entry := retentionEntry{id: m.ID, ts: m.Timestamp, root: m.ThreadRootSeq}
			if m.PollRef != nil {
				entry.poll = m.PollRef.ID
			}
			if m.ScheduleRef != nil {
				entry.schedule = m.ScheduleRef.ID
			}
			all[seq] = entry
		}
		selected := map[int64]bool{}
		polls, schedules := map[string]bool{}, map[string]bool{}
		for seq, m := range all {
			if m.ts.Before(cutoff) {
				selected[seq] = true
			}
		}
		for {
			changed := false
			for seq, m := range all {
				if selected[seq] {
					if m.poll != "" && !polls[m.poll] {
						polls[m.poll], changed = true, true
					}
					if m.schedule != "" && !schedules[m.schedule] {
						schedules[m.schedule], changed = true, true
					}
					continue
				}
				if m.root > 0 && selected[m.root] || m.poll != "" && polls[m.poll] || m.schedule != "" && schedules[m.schedule] {
					selected[seq], changed = true, true
				}
			}
			if !changed {
				break
			}
		}
		if len(selected) == 0 {
			continue
		}
		record := retentionChannel{Messages: make(map[int64]string, len(selected))}
		for seq := range selected {
			m := all[seq]
			record.Messages[seq] = m.id
			if m.root == 0 {
				record.Roots = append(record.Roots, seq)
			}
			if m.poll != "" {
				plan.Polls[m.poll] = ch.ID
			}
			if m.schedule != "" {
				plan.Schedules[m.schedule] = ch.ID
			}
		}
		plan.Channels[ch.ID] = record
		plan.Report.Messages += len(selected)
	}
	plan.Report.Polls, plan.Report.Schedules = len(plan.Polls), len(plan.Schedules)
	return plan, nil
}

func (s *FileStorage) applyRetention(ctx context.Context, plan retentionPlan) error {
	for ch, selected := range plan.Channels {
		if err := checkID("channel", ch); err != nil {
			return err
		}
		for _, kind := range []string{"messages", "reactions"} {
			paths, err := filepath.Glob(filepath.Join(s.root, "channels", ch, kind, "*.jsonl"))
			if err != nil {
				return err
			}
			for _, path := range paths {
				err = rewriteRetentionLog(ctx, path, func(line []byte) (bool, error) {
					if kind == "messages" {
						var m struct {
							Seq int64  `json:"seq"`
							ID  string `json:"id"`
						}
						if e := json.Unmarshal(line, &m); e != nil {
							return false, e
						}
						id, gone := selected.Messages[m.Seq]
						if gone && id != m.ID {
							return false, errors.New("retention journal message ID mismatch")
						}
						return !gone, nil
					}
					var event struct {
						MessageSeq int64 `json:"messageSeq"`
					}
					if e := json.Unmarshal(line, &event); e != nil {
						return false, e
					}
					_, gone := selected.Messages[event.MessageSeq]
					return !gone, nil
				})
				if err != nil {
					return fmt.Errorf("rewrite %s: %w", path, err)
				}
			}
		}
		for seq := range selected.Messages {
			path := filepath.Join(s.root, "withdrawals", ch, "message", strconv.FormatInt(seq, 10)+".json")
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	for id, ch := range plan.Polls {
		if err := checkID("poll", id); err != nil {
			return err
		}
		if err := checkID("channel", ch); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(s.root, "polls", id)); err != nil {
			return err
		}
		path := filepath.Join(s.root, "withdrawals", ch, "poll", id+".json")
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for id, ch := range plan.Schedules {
		if err := checkID("schedule", id); err != nil {
			return err
		}
		if err := checkID("channel", ch); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(s.root, "schedules", id)); err != nil {
			return err
		}
		path := filepath.Join(s.root, "withdrawals", ch, "schedule", id+".json")
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		return err
	}
	for _, user := range users {
		path := filepath.Join(s.root, "state", user.ID+".json")
		var state userState
		if err := readJSON(ctx, path, &state); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		changed := false
		for ch, record := range plan.Channels {
			for _, id := range record.Messages {
				if _, ok := state.Mentions[id]; ok {
					delete(state.Mentions, id)
					changed = true
				}
			}
			for _, root := range record.Roots {
				if _, ok := state.Threads[ch][root]; ok {
					delete(state.Threads[ch], root)
					changed = true
				}
			}
		}
		if changed {
			if err := atomicJSON(ctx, path, state); err != nil {
				return err
			}
		}
	}
	s.mu.Lock()
	s.positionIndexes = make(map[string]*positionIndex)
	s.mu.Unlock()
	s.reactMu.Lock()
	s.reactions = make(map[string]map[int64]map[string]map[string]bool)
	s.reactionsLoaded = make(map[string]bool)
	s.reactMu.Unlock()
	s.mentionMu.Lock()
	s.mentionRecords = make(map[string]domain.Mention)
	s.mentionUserRefs = make(map[string][]string)
	s.legacyUserRefs = make(map[string][]string)
	s.legacyGroupRefs = make(map[string][]string)
	s.mentionMu.Unlock()
	return nil
}

func rewriteRetentionLog(ctx context.Context, path string, keep func([]byte) (bool, error)) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	reader := bufio.NewReader(in)
	changed := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, e := reader.ReadBytes('\n')
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return e
		}
		retain, e := keep(line)
		if e != nil {
			return e
		}
		if !retain {
			changed = true
			break
		}
	}
	if !changed {
		return nil
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".retention-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	reader = bufio.NewReader(in)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			tmp.Close()
			return err
		}
		line, e := reader.ReadBytes('\n')
		if errors.Is(e, io.EOF) {
			break
		} // Ignore an incomplete final line, as recovery does.
		if e != nil {
			tmp.Close()
			return e
		}
		retain, e := keep(line)
		if e != nil {
			tmp.Close()
			return e
		}
		if retain {
			n, e := tmp.Write(line)
			if e != nil {
				tmp.Close()
				return e
			}
			written += int64(n)
		}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := in.Close(); err != nil {
		return err
	}
	if written == 0 {
		return os.Remove(path)
	}
	return os.Rename(tmp.Name(), path)
}
