package web

import (
	"bytes"
	"net/http"
	"strconv"

	"github.com/joestump/sigbrowse/internal/store"
)

// baseData is embedded in every full-page view; it drives the chrome (sidebar).
type baseData struct {
	Title         string
	Conversations []store.ConversationSummary
	ActiveID      int64
}

// messageListData drives the transcript message list and its infinite-scroll
// sentinel (used both in the full page and the HTMX partial).
type messageListData struct {
	ActiveID   int64
	Messages   []store.MessageView
	HasMore    bool
	NextTSUnix int64
	NextID     int64
}

type indexData struct {
	baseData
	NewestTS      string
	TotalMessages int
	HasArchive    bool
}

type conversationData struct {
	baseData
	Active *store.ConversationSummary
	List   messageListData
}

type statusData struct {
	baseData
	Run               *store.IngestRun
	Snapshots         []store.Snapshot
	NewestTS          string
	TotalMessages     int
	SnapshotFootprint int64
}

// pageSize is the number of messages per transcript page.
const pageSize = 50

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	convs, err := s.store.ListConversations(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	newest, err := s.store.NewestMessageTS(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	total, err := s.store.CountMessages(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "index", indexData{
		baseData:      baseData{Title: "sigbrowse", Conversations: convs},
		NewestTS:      newest,
		TotalMessages: total,
		HasArchive:    len(convs) > 0,
	})
}

func (s *Server) handleConversation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	convs, err := s.store.ListConversations(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	active, err := s.store.GetConversationByID(ctx, id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if active == nil {
		http.NotFound(w, r)
		return
	}
	page, err := s.store.GetMessages(ctx, id, 0, 0, pageSize)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "conversation", conversationData{
		baseData: baseData{Title: active.Name + " · sigbrowse", Conversations: convs, ActiveID: id},
		Active:   active,
		List: messageListData{
			ActiveID:   id,
			Messages:   page.Messages,
			HasMore:    page.HasMore,
			NextTSUnix: page.NextTSUnix,
			NextID:     page.NextID,
		},
	})
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	afterTS, _ := strconv.ParseInt(r.URL.Query().Get("after_ts"), 10, 64)
	afterID, _ := strconv.ParseInt(r.URL.Query().Get("after_id"), 10, 64)
	page, err := s.store.GetMessages(ctx, id, afterTS, afterID, pageSize)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "message_list", messageListData{
		ActiveID:   id,
		Messages:   page.Messages,
		HasMore:    page.HasMore,
		NextTSUnix: page.NextTSUnix,
		NextID:     page.NextID,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	convs, err := s.store.ListConversations(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	run, err := s.store.LatestIngestRun(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	snaps, err := s.store.ListSnapshots(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	newest, err := s.store.NewestMessageTS(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	total, err := s.store.CountMessages(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	var footprint int64
	for _, sn := range snaps {
		footprint += sn.SizeBytes
	}
	s.render(w, "status", statusData{
		baseData:          baseData{Title: "Status · sigbrowse", Conversations: convs},
		Run:               run,
		Snapshots:         snaps,
		NewestTS:          newest,
		TotalMessages:     total,
		SnapshotFootprint: footprint,
	})
}

// render executes a named template into a buffer first, so a template error
// never produces a half-written response.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.log.Error("request failed", "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// parseID parses a positive int64 path id.
func parseID(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
