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
	Source          string    `json:"source"`
	OriginalCaption string    `json:"original_caption"`
	Transcript      *string   `json:"transcript,omitempty"`
	AIDescription   *string   `json:"ai_description,omitempty"`
	TranscriptJobID *string   `json:"transcript_job_id,omitempty"`
	TelegramFileID  *string   `json:"telegram_file_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type SearchQuery struct {
	Text   string
	Tags   []string
	Limit  int
	Offset int
}

type UploadMeta struct {
	Type            MediaType
	FileName        string
	ContentType     string
	Size            int64
	Description     string
	Tags            []string
	Source          string
	OriginalCaption string
}

type UpdateMeta struct {
	Description    *string
	Tags           []string
	Transcript     *string
	TelegramFileID *string
}
