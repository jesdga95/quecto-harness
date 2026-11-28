package server

import (
	"errors"
	"net/http"

	"github.com/jesdga95/quecto-harness/internal/config"
)

func (s *Server) getMemory(w http.ResponseWriter, r *http.Request) {
	m, err := s.cfg.EditableMemory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) putMemory(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text     *string `json:"text"`
		Revision string  `json:"revision"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Text == nil || body.Revision == "" {
		writeError(w, http.StatusBadRequest, "Provide the memory text and its revision.")
		return
	}
	m, err := s.cfg.ReplaceMemory(*body.Text, body.Revision)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, config.ErrMemoryChanged):
			status = http.StatusConflict
		case errors.Is(err, config.ErrMemorySize):
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}
