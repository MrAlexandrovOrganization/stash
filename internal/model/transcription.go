package model

type TranscriptionTask struct {
	RequestID   string
	ItemID      string
	Generation  int64
	StoragePath string
	Format      string
	JobID       *string
	State       string
}
