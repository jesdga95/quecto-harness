package server

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// pingInterval keeps intermediaries and idle timeouts from closing a stream
// that is waiting on a slow model.
const pingInterval = 20 * time.Second

// getEvents streams one session's events from the later of the from parameter
// and Last-Event-ID. Both name a frame the client already has: a stream opened
// after a snapshot carries the parameter alone, and a browser reconnecting on
// its own resends the URL it was opened with, whose parameter is older than the
// header it now sends.
func (s *Server) getEvents(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	if session == "" {
		writeError(w, http.StatusBadRequest, "No session: pass ?session=<id>")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "This connection cannot stream events")
		return
	}

	from := int64(0)
	if v := r.URL.Query().Get("from"); v != "" {
		from, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if seen, err := strconv.ParseInt(v, 10, 64); err == nil && seen > from {
			from = seen
		}
	}

	ch, cancel, replay := s.hub.Subscribe(session, from)
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	for _, f := range replay {
		writeFrame(w, f)
	}
	flusher.Flush()

	ping := time.NewTicker(pingInterval)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case f, open := <-ch:
			if !open {
				return
			}
			writeFrame(w, f)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ":ping\n\n")
			flusher.Flush()
		}
	}
}

// writeFrame writes one frame. The field names are matched literally by the
// browser, so they are all lowercase: an id it does not read is an id it cannot
// reconnect from.
func writeFrame(w http.ResponseWriter, f frame) {
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", f.Seq, f.Type, f.Data)
}
