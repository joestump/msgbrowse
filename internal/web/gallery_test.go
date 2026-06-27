package web

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/joestump/msgbrowse/internal/signal"
	"github.com/joestump/msgbrowse/internal/source"
)

func TestGalleryImagesTab(t *testing.T) {
	srv, st, _ := newTestServer(t)
	conv, _ := st.GetConversation(context.Background(), "Harper")

	rec := get(t, srv, "/gallery")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !contains(body, "image-grid") || !contains(body, "lightbox") {
		t.Errorf("images tab missing grid/lightbox")
	}
	// The fixture has Harper/media/cabin.jpg — its media URL should appear.
	if !contains(body, "/media/"+itoa(conv.ID)+"/media/cabin.jpg") {
		t.Errorf("images tab missing fixture image URL")
	}
}

func TestGalleryFilesTab(t *testing.T) {
	srv, st, _ := newTestServer(t)
	conv, _ := st.GetConversation(context.Background(), "Harper")

	rec := get(t, srv, "/gallery?tab=files")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !contains(body, "lease.pdf") {
		t.Errorf("files tab missing lease.pdf")
	}
	// Size/type are computed from the on-disk file; the fixture lease.pdf exists.
	if !contains(body, "/media/"+itoa(conv.ID)+"/media/lease.pdf") {
		t.Errorf("files tab missing file URL")
	}
}

func TestGalleryLinksTab(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := get(t, srv, "/gallery?tab=links")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	// Fixture has a yelp link and a maps link; domains group them.
	if !contains(body, "link-group") {
		t.Errorf("links tab missing groups")
	}
	if !contains(body, "yelp.com") && !contains(body, "example.com") {
		t.Errorf("links tab missing expected domains: %s", body)
	}
}

func TestGalleryTabPreservesFilter(t *testing.T) {
	srv, st, _ := newTestServer(t)
	conv, _ := st.GetConversation(context.Background(), "Harper")
	rec := get(t, srv, "/gallery?tab=images&conversation="+itoa(conv.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	// Tab links should carry the conversation filter forward.
	if !contains(body, "conversation="+itoa(conv.ID)) {
		t.Errorf("tab links dropped the conversation filter")
	}
}

// TestGalleryLinkEscaping confirms a crafted link URL is attribute-escaped in
// the rendered href/text (defense in depth — the parser excludes <>"' from
// bare URLs, but the store accepts any string).
func TestGalleryLinkEscaping(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := context.Background()
	id, err := st.UpsertConversation(ctx, source.Signal, "Evil")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := time.Parse(signal.TimestampLayout, "2022-06-01 10:00:00")
	_, err = st.ReplaceConversationMessages(ctx, id, source.Signal, []signal.Message{
		{Conversation: "Evil", Timestamp: parsed, TimestampRaw: "2022-06-01 10:00:00",
			Sender: "Mallory", Body: "x",
			Links: []signal.Link{{URL: `https://evil.test/"><script>alert(1)</script>`}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := get(t, srv, "/gallery?tab=links")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if contains(body, "<script>alert(1)</script>") {
		t.Errorf("crafted link URL leaked unescaped (XSS): %s", body)
	}
}

func TestGalleryBadTabDefaultsToImages(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := get(t, srv, "/gallery?tab=bogus")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !contains(rec.Body.String(), "image-grid") {
		t.Errorf("bad tab should fall back to images")
	}
}
