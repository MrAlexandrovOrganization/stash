package model

import "time"

type MediaType string

const (
	MediaTypeImage    MediaType = "image"
	MediaTypeVideo    MediaType = "video"
	MediaTypeGIF      MediaType = "gif"
	MediaTypeDocument MediaType = "document"
)

type Item struct {
	ID              string    `json:"id"`
	Type            MediaType `json:"type"`
	FileName        string    `json:"file_name"`
	ContentType     string    `json:"content_type"`
	Size            int64     `json:"size"`
	StoragePath     string    `json:"storage_path"`
	Description     string    `json:"description"`
	Tags            []string  `json:"tags"`
	Transcript      *string   `json:"transcript,omitempty"`
	TranscriptJobID *string   `json:"transcript_job_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type SearchQuery struct {
	Text string
	Tags []string
}

type UploadMeta struct {
	Type        MediaType
	FileName    string
	ContentType string
	Size        int64
	Description string
	Tags        []string
}

type UpdateMeta struct {
	Description *string
	Tags        []string
}
