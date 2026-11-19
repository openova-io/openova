package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/openova-io/openova/products/cortex/internal/auth"
	"github.com/openova-io/openova/products/cortex/internal/ingest"
	"github.com/openova-io/openova/products/cortex/internal/store"
)

// Uploader stages an upload: it stores the original and queues the index job.
type Uploader interface {
	Upload(ctx context.Context, sourceID, filename string, content io.ReadSeeker, size int64, readers []string) (documentID, jobID string, err error)
}

const (
	// formSlack is what a multipart body may hold beyond the file: part headers and readers.
	formSlack = 1 << 20
	// formMemory is the part of a multipart body held in memory; the rest goes to a temporary file.
	formMemory = 8 << 20
	// maxReaders is the most reader fields one upload may carry.
	maxReaders = 100
)

// readerForm is the form of a principal in access.readers of the definitions.
var readerForm = regexp.MustCompile(`^(user|group|agent):[^\s]{1,200}$|^org:members$`)

// upload is POST /v1/sources/{id}/documents: a multipart body with one part file and optional
// parts reader, one principal each. With at least one reader the document reads only for those
// principals and the managers of its source. A source the caller does not manage, also one
// that does not exist, is refused before the body is read.
func (s *Server) upload(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	id := r.PathValue("id")
	src, err := s.cfg.Store.Source(r.Context(), c.Principals, id)
	switch {
	case errors.Is(err, store.ErrNotFound) || (err == nil && !src.Manager):
		writeError(w, http.StatusForbidden, "not-manager")
		return
	case err != nil:
		s.cfg.Log.Error("upload: the source cannot be read", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal")
		return
	case src.Connector != "upload":
		writeError(w, http.StatusConflict, "not-an-upload-source")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUpload+formSlack)
	if err := r.ParseMultipartForm(formMemory); err != nil {
		if mbe := (*http.MaxBytesError)(nil); errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "too-large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid-request")
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	form := r.MultipartForm
	files := form.File["file"]
	_, onlyReaders := form.Value["reader"]
	if len(files) != 1 || len(form.File) != 1 || len(form.Value) > 1 || (len(form.Value) == 1 && !onlyReaders) {
		writeError(w, http.StatusBadRequest, "invalid-request")
		return
	}
	readers := form.Value["reader"]
	if len(readers) > maxReaders {
		writeError(w, http.StatusBadRequest, "invalid-reader")
		return
	}
	for _, p := range readers {
		if !readerForm.MatchString(p) || store.CheckPrincipal(p) != nil {
			writeError(w, http.StatusBadRequest, "invalid-reader")
			return
		}
	}
	if files[0].Size > s.cfg.MaxUpload {
		writeError(w, http.StatusRequestEntityTooLarge, "too-large")
		return
	}
	f, err := files[0].Open()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid-request")
		return
	}
	defer func() { _ = f.Close() }()
	documentID, jobID, err := s.cfg.Uploads.Upload(r.Context(), id, files[0].Filename, f, files[0].Size, readers)
	switch {
	case errors.Is(err, ingest.ErrType):
		writeError(w, http.StatusUnsupportedMediaType, "type-not-allowed")
	case err != nil:
		s.cfg.Log.Error("upload failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal")
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"documentId": documentID, "jobId": jobID})
	}
}

// readable returns the document {id} when the caller may read it, and answers 404 otherwise.
func (s *Server) readable(w http.ResponseWriter, r *http.Request, c auth.Caller) (store.DocumentAccess, bool) {
	d, err := s.cfg.Store.Document(r.Context(), c.Principals, r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "document-not-found")
	case err != nil:
		s.cfg.Log.Error("the document cannot be read", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal")
	default:
		return d, true
	}
	return d, false
}

// document is GET /v1/documents/{id}.
func (s *Server) document(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if d, ok := s.readable(w, r, c); ok {
		writeJSON(w, http.StatusOK, map[string]any{"id": d.ID, "sourceId": d.SourceID, "title": d.Title, "uri": d.URI,
			"state": d.State, "updatedAt": d.UpdatedAt})
	}
}

// content is GET /v1/documents/{id}/content: the original bytes, as an attachment.
func (s *Server) content(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	d, ok := s.readable(w, r, c)
	if !ok {
		return
	}
	body, size, err := s.cfg.Originals.Get(r.Context(), d.ObjectKey)
	switch {
	case errors.Is(err, ingest.ErrNoObject):
		writeError(w, http.StatusNotFound, "document-not-found")
		return
	case err != nil:
		s.cfg.Log.Error("the original cannot be read", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	defer func() { _ = body.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

// deleteDocument is DELETE /v1/documents/{id}: a manager of the document's source queues its
// delete job.
func (s *Server) deleteDocument(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	d, ok := s.readable(w, r, c)
	if !ok {
		return
	}
	if !d.Manager {
		writeError(w, http.StatusForbidden, "not-manager")
		return
	}
	item, _ := json.Marshal(map[string]string{"object": d.ObjectKey})
	jobID, err := s.cfg.Store.Enqueue(r.Context(), store.Job{Kind: "delete", SourceID: d.SourceID, DocumentID: d.ID, Item: item})
	if err != nil {
		s.cfg.Log.Error("the delete job cannot be queued", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"jobId": jobID})
}

// events is GET /v1/events: a server-sent event stream with one frame "event: job" per change
// of a job whose document the caller reads or whose source the caller manages, and a comment
// line after each Heartbeat without a frame, from a timer that every write resets: it keeps
// the connection open and polls nothing. A stream ends after StreamFor; the client opens
// another with a current token.
func (s *Server) events(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	heartbeat, streamFor := s.cfg.Heartbeat, s.cfg.StreamFor
	if heartbeat <= 0 {
		heartbeat = 20 * time.Second
	}
	if streamFor <= 0 {
		streamFor = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), streamFor)
	defer cancel()
	ch, stop := s.cfg.Hub.Subscribe()
	defer stop()
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	idle := time.NewTimer(heartbeat)
	defer idle.Stop()
	send := func(text string) bool {
		idle.Reset(heartbeat)
		if _, err := io.WriteString(w, text); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send(": connected\n\n") {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
			if !send(": keep-alive\n\n") {
				return
			}
		case e := <-ch:
			if ok, err := s.cfg.Store.MayWatch(ctx, c.Principals, e.SourceID, e.DocumentID); err != nil || !ok {
				continue
			}
			data, _ := json.Marshal(e)
			if !send(fmt.Sprintf("event: job\ndata: %s\n\n", data)) {
				return
			}
		}
	}
}
