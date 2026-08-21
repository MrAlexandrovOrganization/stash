package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"stash/internal/model"
	"stash/internal/repository"
	"stash/internal/service"
)

type Handler struct {
	svc service.Service
}

func New(svc service.Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /items", h.upload)
	mux.HandleFunc("GET /items", h.search)
	mux.HandleFunc("GET /items/{id}", h.getItem)
	mux.HandleFunc("GET /items/{id}/file", h.getFile)
	mux.HandleFunc("DELETE /items/{id}", h.deleteItem)
	mux.HandleFunc("PATCH /items/{id}", h.updateItem)
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "parse form: "+err.Error())
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "file field required")
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	meta := model.UploadMeta{
		Type:            detectMediaType(header, contentType),
		FileName:        header.Filename,
		ContentType:     contentType,
		Size:            header.Size,
		Description:     r.FormValue("description"),
		Tags:            parseTags(r.FormValue("tags")),
		Source:          r.FormValue("source"),
		OriginalCaption: r.FormValue("original_caption"),
	}

	item, err := h.svc.Upload(r.Context(), file, meta)
	if err != nil {
		slog.Error("upload", "error", err)
		writeErr(w, http.StatusInternalServerError, "upload failed")
		return
	}

	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	q := model.SearchQuery{
		Text: r.URL.Query().Get("q"),
		Tags: parseTags(r.URL.Query().Get("tags")),
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			q.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			q.Offset = n
		}
	}
	items, err := h.svc.Search(r.Context(), q)
	if err != nil {
		slog.Error("search", "error", err)
		writeErr(w, http.StatusInternalServerError, "search failed")
		return
	}
	if items == nil {
		items = []*model.Item{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) getItem(w http.ResponseWriter, r *http.Request) {
	item, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "get failed")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) getFile(w http.ResponseWriter, r *http.Request) {
	rc, item, err := h.svc.GetFile(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "get file failed")
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", item.ContentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+item.FileName+`"`)
	if _, err := io.Copy(w, rc); err != nil {
		slog.Error("stream file", "error", err)
	}
}

func (h *Handler) deleteItem(w http.ResponseWriter, r *http.Request) {
	err := h.svc.Delete(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		slog.Error("delete", "error", err)
		writeErr(w, http.StatusInternalServerError, "delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) updateItem(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Description    *string  `json:"description"`
		Tags           []string `json:"tags"`
		Transcript     *string  `json:"transcript"`
		TelegramFileID *string  `json:"telegram_file_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	meta := model.UpdateMeta{
		Description:    body.Description,
		Tags:           body.Tags,
		Transcript:     body.Transcript,
		TelegramFileID: body.TelegramFileID,
	}
	item, err := h.svc.Update(r.Context(), r.PathValue("id"), meta)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		slog.Error("update", "error", err)
		writeErr(w, http.StatusInternalServerError, "update failed")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func detectMediaType(header *multipart.FileHeader, contentType string) model.MediaType {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(header.Filename), "."))
	switch ext {
	case "jpg", "jpeg", "png", "webp", "heic", "bmp":
		return model.MediaTypeImage
	case "gif":
		return model.MediaTypeGIF
	case "mp4", "mov", "avi", "mkv", "webm":
		return model.MediaTypeVideo
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	switch {
	case strings.HasPrefix(mediaType, "image/gif"):
		return model.MediaTypeGIF
	case strings.HasPrefix(mediaType, "image/"):
		return model.MediaTypeImage
	case strings.HasPrefix(mediaType, "video/"):
		return model.MediaTypeVideo
	}
	return model.MediaTypeDocument
}

func parseTags(s string) []string {
	if s == "" {
		return nil
	}
	var tags []string
	for part := range strings.SplitSeq(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json", "error", err)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
