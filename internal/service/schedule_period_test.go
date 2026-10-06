package service

import (
	"github.com/masapico/minihub/internal/domain"
	"testing"
	"time"
)

func TestScheduleIsPast(t *testing.T) {
	midnight := time.Date(2026, 9, 15, 15, 0, 0, 0, time.UTC)
	for _, status := range []domain.ScheduleStatus{domain.ScheduleDraft, domain.ScheduleOpen, domain.ScheduleClosed, domain.ScheduleFinalized} {
		t.Run(string(status), func(t *testing.T) {
			s := domain.Schedule{Status: status, FinalCandidateID: "a", Candidates: []domain.ScheduleCandidate{{ID: "a", Date: "2026-09-15"}}}
			if scheduleIsPast(s, midnight.Add(-time.Nanosecond)) {
				t.Fatal("same Japanese day must remain upcoming")
			}
			if !scheduleIsPast(s, midnight) {
				t.Fatal("must become past at Japanese midnight")
			}
			s.Candidates = append(s.Candidates, domain.ScheduleCandidate{ID: "b", Date: "2026-09-20"})
			if got := scheduleIsPast(s, midnight); got != (status == domain.ScheduleFinalized) {
				t.Fatalf("mixed candidates: past=%v", got)
			}
		})
	}
	for _, s := range []domain.Schedule{
		{}, {Candidates: []domain.ScheduleCandidate{{Date: "invalid"}}},
		{Status: domain.ScheduleFinalized, FinalCandidateID: "missing", Candidates: []domain.ScheduleCandidate{{ID: "a", Date: "2020-01-01"}}},
	} {
		if scheduleIsPast(s, midnight) {
			t.Fatal("undetermined schedule must remain upcoming")
		}
	}
}
