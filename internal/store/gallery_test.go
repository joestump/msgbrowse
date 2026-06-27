package store

import (
	"context"
	"testing"

	"github.com/joestump/msgbrowse/internal/signal"
	"github.com/joestump/msgbrowse/internal/source"
)

func seedGalleryCorpus(t *testing.T) (*Store, int64, int64) {
	t.Helper()
	st := newTestStore(t)
	ctx := context.Background()

	harper, err := st.UpsertConversation(ctx, source.Signal, "Harper")
	if err != nil {
		t.Fatal(err)
	}
	group, err := st.UpsertConversation(ctx, source.Signal, "Group Trip")
	if err != nil {
		t.Fatal(err)
	}

	img := func(name string) []signal.Attachment {
		return []signal.Attachment{{Kind: signal.KindImage, RelPath: "media/" + name, OriginalName: name}}
	}
	file := func(name string) []signal.Attachment {
		return []signal.Attachment{{Kind: signal.KindFile, RelPath: "media/" + name, OriginalName: name}}
	}
	link := func(u string) []signal.Link { return []signal.Link{{URL: u}} }

	harperMsgs := []signal.Message{
		msg("Harper", "2022-03-01 09:00:00", "Harper", "pic", img("cabin.jpg"), nil),
		msg("Harper", "2022-03-01 09:01:00", "Harper", "doc", file("lease.pdf"), nil),
		msg("Harper", "2022-03-01 09:02:00", "Me", "map", nil, link("https://maps.example.com/a")),
		msg("Harper", "2022-03-02 09:03:00", "Me", "map again", nil, link("https://maps.example.com/a")),
		msg("Harper", "2022-03-02 09:04:00", "Me", "food", nil, link("https://www.yelp.com/biz/foo")),
	}
	if _, err := st.ReplaceConversationMessages(ctx, harper, source.Signal, harperMsgs); err != nil {
		t.Fatal(err)
	}
	groupMsgs := []signal.Message{
		msg("Group Trip", "2022-04-01 18:00:00", "MJ", "sunset", img("sunset.png"), nil),
	}
	if _, err := st.ReplaceConversationMessages(ctx, group, source.Signal, groupMsgs); err != nil {
		t.Fatal(err)
	}
	return st, harper, group
}

func TestListAttachments(t *testing.T) {
	st, harper, _ := seedGalleryCorpus(t)
	ctx := context.Background()

	images, err := st.ListAttachments(ctx, "image", GalleryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 { // cabin.jpg + sunset.png
		t.Errorf("images = %d, want 2", len(images))
	}
	// Newest first: sunset (2022-04) before cabin (2022-03).
	if len(images) == 2 && images[0].OriginalName != "sunset.png" {
		t.Errorf("images not newest-first: %+v", images)
	}

	files, err := st.ListAttachments(ctx, "file", GalleryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].OriginalName != "lease.pdf" {
		t.Errorf("files = %+v, want [lease.pdf]", files)
	}

	// Conversation filter: only Harper's image.
	hImages, err := st.ListAttachments(ctx, "image", GalleryFilter{ConversationID: harper})
	if err != nil {
		t.Fatal(err)
	}
	if len(hImages) != 1 || hImages[0].OriginalName != "cabin.jpg" {
		t.Errorf("harper images = %+v, want [cabin.jpg]", hImages)
	}

	// Date filter: only March images (excludes April sunset).
	marchImages, err := st.ListAttachments(ctx, "image", GalleryFilter{EndUnix: dayUnix(t, "2022-03-31")})
	if err != nil {
		t.Fatal(err)
	}
	if len(marchImages) != 1 || marchImages[0].OriginalName != "cabin.jpg" {
		t.Errorf("march images = %+v, want [cabin.jpg]", marchImages)
	}

	// Source filter: imessage has none.
	none, err := st.ListAttachments(ctx, "image", GalleryFilter{Source: source.IMessage})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("imessage images = %d, want 0", len(none))
	}
}

func TestListLinksDedupAndGroup(t *testing.T) {
	st, _, _ := seedGalleryCorpus(t)
	links, err := st.ListLinks(context.Background(), GalleryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	// Two distinct URLs (maps.example.com/a appears twice, yelp once).
	if len(links) != 2 {
		t.Fatalf("distinct links = %d, want 2: %+v", len(links), links)
	}
	// Ordered by domain asc: maps.example.com before yelp.com.
	if links[0].Domain != "maps.example.com" || links[1].Domain != "yelp.com" {
		t.Errorf("domain order wrong: %q, %q", links[0].Domain, links[1].Domain)
	}
	// The maps link was seen twice → Count 2; earliest occurrence retained.
	if links[0].Count != 2 {
		t.Errorf("maps link count = %d, want 2", links[0].Count)
	}
	if links[0].TS != "2022-03-01 09:02:00" {
		t.Errorf("maps link earliest TS = %q, want 2022-03-01 09:02:00", links[0].TS)
	}
	// www. stripped from yelp domain.
	if links[1].Domain != "yelp.com" {
		t.Errorf("yelp domain = %q, want yelp.com (www stripped)", links[1].Domain)
	}
}

func TestCountMedia(t *testing.T) {
	st, harper, _ := seedGalleryCorpus(t)
	ctx := context.Background()

	all, err := st.CountMedia(ctx, GalleryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if all.Images != 2 || all.Files != 1 || all.Links != 2 {
		t.Errorf("counts = %+v, want {Images:2 Files:1 Links:2}", all)
	}

	h, err := st.CountMedia(ctx, GalleryFilter{ConversationID: harper})
	if err != nil {
		t.Fatal(err)
	}
	if h.Images != 1 || h.Files != 1 || h.Links != 2 {
		t.Errorf("harper counts = %+v, want {Images:1 Files:1 Links:2}", h)
	}
}
