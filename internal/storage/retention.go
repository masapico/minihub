package storage

import "time"

// RetentionReport describes the selected records before or after pruning.
type RetentionReport struct {
	Cutoff    time.Time `json:"cutoff"`
	Messages  int       `json:"messages"`
	Polls     int       `json:"polls"`
	Schedules int       `json:"schedules"`
}
