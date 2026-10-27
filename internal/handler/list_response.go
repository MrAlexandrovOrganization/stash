package handler

import (
	"compress/gzip"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"stash/internal/model"
)

// Lists include descriptions and transcripts for the entire search result.
// Compress them for clients such as stash-bot's standard Go HTTP transport,
// which advertises gzip and transparently decodes the response.
func writeListJSON(w http.ResponseWriter, r *http.Request, items []*model.Item) {
	w.Header().Add("Vary", "Accept-Encoding")
	if !acceptsGzip(r.Header.Values("Accept-Encoding")) {
		writeJSON(w, http.StatusOK, items)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Encoding", "gzip")
	w.WriteHeader(http.StatusOK)
	zw := gzip.NewWriter(w)
	defer zw.Close()
	_ = json.NewEncoder(zw).Encode(items)
}

func acceptsGzip(values []string) bool {
	for _, value := range values {
		for _, coding := range strings.Split(value, ",") {
			parts := strings.Split(coding, ";")
			if !strings.EqualFold(strings.TrimSpace(parts[0]), "gzip") {
				continue
			}
			quality := 1.0
			for _, param := range parts[1:] {
				name, value, ok := strings.Cut(strings.TrimSpace(param), "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(name), "q") {
					continue
				}
				q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || !(q >= 0 && q <= 1) {
					return false
				}
				quality = q
			}
			return quality > 0
		}
	}
	return false
}
