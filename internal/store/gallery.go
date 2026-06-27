package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// GalleryFilter narrows the media/links gallery by conversation, source, and
// date range. The zero value means "everything".
type GalleryFilter struct {
	ConversationID int64
	Source         string
	StartUnix      int64
	EndUnix        int64
	Limit          int
}

// MediaItem is one attachment (image or file) with the provenance needed to
// render it and link back to its message in context.
type MediaItem struct {
	ConversationID   int64
	ConversationName string
	Source           string
	MessageID        int64
	Kind             string // "image" | "file"
	RelPath          string
	OriginalName     string
	TS               string
	TSUnix           int64
}

// LinkItem is one deduplicated URL with its domain, occurrence count, and the
// earliest message it appeared in (for "jump to source").
type LinkItem struct {
	URL              string
	Domain           string
	Count            int
	ConversationID   int64
	ConversationName string
	Source           string
	MessageID        int64
	TS               string
	TSUnix           int64
}

// galleryWhere builds the shared WHERE fragment and args for the joined
// attachments/links → messages → conversations queries. The leading clause is
// always present so callers can append with " AND ".
func galleryWhere(f GalleryFilter) (string, []any) {
	where := []string{"1 = 1"}
	var args []any
	if f.ConversationID > 0 {
		where = append(where, "m.conversation_id = ?")
		args = append(args, f.ConversationID)
	}
	if f.Source != "" {
		where = append(where, "m.source = ?")
		args = append(args, f.Source)
	}
	if f.StartUnix > 0 {
		where = append(where, "m.ts_unix >= ?")
		args = append(args, f.StartUnix)
	}
	if f.EndUnix > 0 {
		where = append(where, "m.ts_unix <= ?")
		args = append(args, f.EndUnix)
	}
	return strings.Join(where, " AND "), args
}

func galleryLimit(f GalleryFilter, def, max int) int {
	if f.Limit <= 0 || f.Limit > max {
		return def
	}
	return f.Limit
}

// ListAttachments returns attachments of the given kind ("image" or "file"),
// newest first, matching the filter.
func (s *Store) ListAttachments(ctx context.Context, kind string, f GalleryFilter) ([]MediaItem, error) {
	whereSQL, args := galleryWhere(f)
	limit := galleryLimit(f, 200, 1000)
	q := `
SELECT m.conversation_id, c.name, m.source, m.id, a.kind, a.rel_path, a.original_name, m.ts, m.ts_unix
  FROM attachments a
  JOIN messages m      ON m.id = a.message_id
  JOIN conversations c ON c.id = m.conversation_id
 WHERE ` + whereSQL + ` AND a.kind = ?
 ORDER BY m.ts_unix DESC, m.id DESC, a.id DESC
 LIMIT ?`
	args = append(args, kind, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	defer rows.Close()
	var out []MediaItem
	for rows.Next() {
		var m MediaItem
		if err := rows.Scan(&m.ConversationID, &m.ConversationName, &m.Source, &m.MessageID,
			&m.Kind, &m.RelPath, &m.OriginalName, &m.TS, &m.TSUnix); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListLinks returns links matching the filter, deduplicated by URL. Each item
// carries its occurrence count and the earliest message it appeared in. Results
// are ordered by domain, then by descending occurrence count.
func (s *Store) ListLinks(ctx context.Context, f GalleryFilter) ([]LinkItem, error) {
	whereSQL, args := galleryWhere(f)
	// Pull matching links oldest-first so the first time we see a URL is its
	// earliest occurrence; dedup and count in Go. Capped to bound memory.
	const scanCap = 5000
	q := `
SELECT l.url, l.domain, m.conversation_id, c.name, m.source, m.id, m.ts, m.ts_unix
  FROM links l
  JOIN messages m      ON m.id = l.message_id
  JOIN conversations c ON c.id = m.conversation_id
 WHERE ` + whereSQL + `
 ORDER BY m.ts_unix ASC, m.id ASC, l.id ASC
 LIMIT ?`
	args = append(args, scanCap)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	defer rows.Close()

	byURL := make(map[string]*LinkItem)
	var order []string
	for rows.Next() {
		var (
			url, domain, name, src, ts string
			convID, msgID, tsUnix      int64
		)
		if err := rows.Scan(&url, &domain, &convID, &name, &src, &msgID, &ts, &tsUnix); err != nil {
			return nil, err
		}
		if li, ok := byURL[url]; ok {
			li.Count++
			continue
		}
		byURL[url] = &LinkItem{
			URL: url, Domain: domain, Count: 1,
			ConversationID: convID, ConversationName: name, Source: src,
			MessageID: msgID, TS: ts, TSUnix: tsUnix,
		}
		order = append(order, url)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]LinkItem, 0, len(order))
	for _, u := range order {
		out = append(out, *byURL[u])
	}
	// Stable order: domain asc, then most-frequent first, then earliest.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Domain != b.Domain {
			return a.Domain < b.Domain
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.TSUnix < b.TSUnix
	})
	return out, nil
}

// MediaCounts is the per-tab totals shown on the gallery (so empty tabs are
// obvious and the active filter's effect is visible).
type MediaCounts struct {
	Images int
	Files  int
	Links  int // distinct URLs
}

// CountMedia returns the number of images, files, and distinct links matching
// the filter.
func (s *Store) CountMedia(ctx context.Context, f GalleryFilter) (MediaCounts, error) {
	whereSQL, args := galleryWhere(f)
	var c MediaCounts

	attQ := `
SELECT a.kind, COUNT(*)
  FROM attachments a
  JOIN messages m ON m.id = a.message_id
 WHERE ` + whereSQL + `
 GROUP BY a.kind`
	rows, err := s.db.QueryContext(ctx, attQ, args...)
	if err != nil {
		return c, fmt.Errorf("count attachments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return c, err
		}
		switch kind {
		case "image":
			c.Images = n
		case "file":
			c.Files = n
		}
	}
	if err := rows.Err(); err != nil {
		return c, err
	}

	linkQ := `
SELECT COUNT(DISTINCT l.url)
  FROM links l
  JOIN messages m ON m.id = l.message_id
 WHERE ` + whereSQL
	if err := s.db.QueryRowContext(ctx, linkQ, args...).Scan(&c.Links); err != nil {
		return c, fmt.Errorf("count links: %w", err)
	}
	return c, nil
}
