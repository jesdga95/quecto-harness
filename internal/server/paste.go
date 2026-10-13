package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jesdga95/quecto-harness/internal/tools"
)

// pasteBudget bounds the request. The bytes arrive base64 encoded, which is a
// third larger than the image limit they are held to once decoded.
const pasteBudget = 2 * tools.MaxImage

// postPaste stores bytes the browser has no path for, which is an image or a
// block of text pasted into the composer. What comes back is a path, so the
// message that carries it attaches a file like any other.
func (s *Server) postPaste(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Data string `json:"data"`
	}
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, pasteBudget))
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "The pasted file did not arrive whole: paste a smaller one")
		return
	}
	data, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "The pasted file is not encoded as base64")
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "The pasted file is empty")
		return
	}
	// Text is inlined into the prompt and held to a smaller limit than an
	// image, so what a paste is decides the size it may be.
	limit := tools.MaxAttachment
	if tools.LooksLikeImage(data) {
		limit = tools.MaxImage
	}
	if len(data) > limit {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("The pasted file is %d kb, the limit is %d kb", len(data)/1024, limit/1024))
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		body.Name = "Pasted file"
	}

	// Pastes only arrive here, so this is where the ones nobody sent are
	// cleared out.
	s.uploads.Prune()

	path, err := s.uploads.Save(body.Name, data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path})
}
