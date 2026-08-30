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
	ID                 string    `json:"id"`
	Type               MediaType `json:"type"`
	FileName           string    `json:"file_name"`
	ContentType        string    `json:"content_type"`
	Size               int64     `json:"size"`
	StoragePath        string    `json:"storage_path"`
	Description        string    `json:"description"`
	Tags               []string  `json:"tags"`
	Source             string    `json:"source"`
	OriginalCaption    string    `json:"original_caption"`
	Transcript         *string   `json:"transcript,omitempty"`
	AIDescription      *string   `json:"ai_description,omitempty"`
	AIDescriptionError *string   `json:"ai_description_error,omitempty"`
	TranscriptJobID    *string   `json:"transcript_job_id,omitempty"`
	TelegramFileID     *string   `json:"telegram_file_id,omitempty"`
	FileUniqueID       *string   `json:"file_unique_id,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type SearchQuery struct {
	Text   string
	Tags   []string
	Limit  int
	Offset int
}

// SimilarRequest is the input to a hybrid similarity search. At least one of
// Text, ItemID or ImageBytes should be set. The service turns each into one or
// more embedding vectors and ranks items by the weighted sum of cosine
// similarities.
type SimilarRequest struct {
	Text        string  // natural-language query
	ItemID      string  // find items visually similar to this existing item
	ImageBytes  []byte  // find items visually similar to an uploaded image
	TextWeight  float64 // weight for the text-space similarity term
	ImageWeight float64 // weight for the image-space similarity term
	Limit       int
	Offset      int
}

// SimilarQuery carries precomputed embedding vectors to the repository layer.
type SimilarQuery struct {
	TextVector  []float32
	ImageVector []float32
	TextWeight  float64
	ImageWeight float64
	Limit       int
	Offset      int
}

// EmbeddingKind selects which embedding column to read.
type EmbeddingKind string

const (
	EmbeddingKindText  EmbeddingKind = "text"
	EmbeddingKindImage EmbeddingKind = "image"
)

type UploadMeta struct {
	Type            MediaType
	FileName        string
	ContentType     string
	Size            int64
	Description     string
	Tags            []string
	Source          string
	OriginalCaption string
	FileUniqueID    string
}

type UpdateMeta struct {
	Description    *string
	Tags           []string
	Transcript     *string
	TelegramFileID *string
	FileUniqueID   *string
}
