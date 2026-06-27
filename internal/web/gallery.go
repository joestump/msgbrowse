package web

import (
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joestump/msgbrowse/internal/source"
	"github.com/joestump/msgbrowse/internal/store"
)

// galleryFilterForm is the re-renderable filter state for the gallery.
type galleryFilterForm struct {
	Tab            string
	ConversationID int64
	Source         string
	Start          string
	End            string
}

// galleryFileView decorates a file attachment with its on-disk size and type,
// computed on demand from the read-only archive.
type galleryFileView struct {
	store.MediaItem
	SizeHuman   string
	ContentType string
}

// linkGroup is a set of deduplicated links sharing a domain.
type linkGroup struct {
	Domain string
	Links  []store.LinkItem
}

type galleryData struct {
	baseData
	Filter  galleryFilterForm
	Sources []string
	Counts  store.MediaCounts
	Images  []store.MediaItem
	Files   []galleryFileView
	Groups  []linkGroup
}

// validTabs are the gallery's three views.
var validTabs = map[string]bool{"images": true, "files": true, "links": true}

func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	convs, err := s.store.ListConversations(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}

	form, filter := parseGalleryFilter(r)
	counts, err := s.store.CountMedia(ctx, filter)
	if err != nil {
		s.serverError(w, err)
		return
	}

	data := galleryData{
		baseData: baseData{Title: "Media · msgbrowse", Conversations: convs},
		Filter:   form,
		Sources:  source.All,
		Counts:   counts,
	}

	switch form.Tab {
	case "files":
		items, err := s.store.ListAttachments(ctx, "file", filter)
		if err != nil {
			s.serverError(w, err)
			return
		}
		data.Files = s.decorateFiles(items)
	case "links":
		links, err := s.store.ListLinks(ctx, filter)
		if err != nil {
			s.serverError(w, err)
			return
		}
		data.Groups = groupLinksByDomain(links)
	default: // images
		data.Images, err = s.store.ListAttachments(ctx, "image", filter)
		if err != nil {
			s.serverError(w, err)
			return
		}
	}

	s.render(w, "gallery", data)
}

// decorateFiles stats each file in the read-only archive to add size and type.
// Files that can't be stat'd (missing/renamed) still render, just without
// size/type, so the listing never fails on a single bad attachment.
func (s *Server) decorateFiles(items []store.MediaItem) []galleryFileView {
	out := make([]galleryFileView, 0, len(items))
	for _, it := range items {
		v := galleryFileView{MediaItem: it}
		if full, ok := s.mediaFilePath(it.Source, it.ConversationName, it.RelPath); ok {
			if info, err := os.Stat(full); err == nil && !info.IsDir() {
				v.SizeHuman = humanSize(info.Size())
				v.ContentType = fileContentType(full)
			}
		}
		out = append(out, v)
	}
	return out
}

// fileContentType resolves a file's type by extension, falling back to sniffing
// the first 512 bytes (http.DetectContentType) when the extension is unknown.
func fileContentType(full string) string {
	if ct := mime.TypeByExtension(filepath.Ext(full)); ct != "" {
		// Trim any "; charset=..." for a compact display label.
		if i := strings.IndexByte(ct, ';'); i >= 0 {
			ct = ct[:i]
		}
		return ct
	}
	f, err := os.Open(full)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	ct := http.DetectContentType(buf[:n])
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return ct
}

// groupLinksByDomain groups already-domain-sorted links into per-domain blocks.
func groupLinksByDomain(links []store.LinkItem) []linkGroup {
	var groups []linkGroup
	for _, l := range links {
		if n := len(groups); n > 0 && groups[n-1].Domain == l.Domain {
			groups[n-1].Links = append(groups[n-1].Links, l)
			continue
		}
		groups = append(groups, linkGroup{Domain: l.Domain, Links: []store.LinkItem{l}})
	}
	return groups
}

// parseGalleryFilter reads the gallery's tab + filters from the query string.
func parseGalleryFilter(r *http.Request) (galleryFilterForm, store.GalleryFilter) {
	tab := r.URL.Query().Get("tab")
	if !validTabs[tab] {
		tab = "images"
	}
	convID, _ := strconv.ParseInt(r.URL.Query().Get("conversation"), 10, 64)
	src := r.URL.Query().Get("source")
	if !source.IsKnown(src) {
		src = ""
	}
	start := r.URL.Query().Get("start")
	end := r.URL.Query().Get("end")

	form := galleryFilterForm{Tab: tab, ConversationID: convID, Source: src, Start: start, End: end}
	filter := store.GalleryFilter{
		ConversationID: convID,
		Source:         src,
		StartUnix:      dayStartUnix(start),
		EndUnix:        dayEndUnix(end),
	}
	return form, filter
}

// GalleryQuery builds the querystring that preserves the current filters when
// switching tabs (used by the tab links in the template). Exported so the
// html/template can call it as a method.
func (f galleryFilterForm) GalleryQuery(tab string) string {
	v := url.Values{}
	v.Set("tab", tab)
	if f.ConversationID > 0 {
		v.Set("conversation", strconv.FormatInt(f.ConversationID, 10))
	}
	if f.Source != "" {
		v.Set("source", f.Source)
	}
	if f.Start != "" {
		v.Set("start", f.Start)
	}
	if f.End != "" {
		v.Set("end", f.End)
	}
	return "/gallery?" + v.Encode()
}
