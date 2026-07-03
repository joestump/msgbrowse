package store

import (
	"context"
	"strings"
	"testing"

	"github.com/joestump/msgbrowse/internal/signal"
	"github.com/joestump/msgbrowse/internal/source"
)

// TestConversationRefs verifies the lightweight dropdown listing: every
// conversation appears exactly once as an id+name pair, alphabetically
// (case-insensitive), with no dependency on messages existing.
func TestConversationRefs(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	names := []string{"zeta", "Alpha", "mike"}
	ids := map[string]int64{}
	for _, n := range names {
		id, err := st.UpsertConversation(ctx, source.Signal, n)
		if err != nil {
			t.Fatalf("upsert %q: %v", n, err)
		}
		ids[n] = id
	}
	// Only one conversation has messages — refs must list all three regardless.
	if _, err := st.ReplaceConversationMessages(ctx, ids["mike"], source.Signal,
		[]signal.Message{msg("mike", "2022-03-01 09:00:00", "Mike", "hey", nil, nil)}); err != nil {
		t.Fatalf("replace messages: %v", err)
	}

	refs, err := st.ConversationRefs(ctx)
	if err != nil {
		t.Fatalf("ConversationRefs: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("refs = %d, want 3", len(refs))
	}
	wantOrder := []string{"Alpha", "mike", "zeta"} // case-insensitive alphabetical
	for i, want := range wantOrder {
		if refs[i].Name != want {
			t.Errorf("refs[%d].Name = %q, want %q", i, refs[i].Name, want)
		}
		if refs[i].ID != ids[want] {
			t.Errorf("refs[%d].ID = %d, want %d", i, refs[i].ID, ids[want])
		}
	}
}

// TestArchiveStats verifies the one-statement global counts agree with the
// values full renders derive from the summary listing (REQ-0008-004/006).
func TestArchiveStats(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	empty, err := st.ArchiveStats(ctx)
	if err != nil {
		t.Fatalf("ArchiveStats(empty): %v", err)
	}
	if empty.Conversations != 0 || empty.Messages != 0 {
		t.Errorf("empty stats = %+v, want zeros", empty)
	}

	a, _ := st.UpsertConversation(ctx, source.Signal, "Harper")
	b, _ := st.UpsertConversation(ctx, source.Signal, "MJ")
	if _, err := st.ReplaceConversationMessages(ctx, a, source.Signal, []signal.Message{
		msg("Harper", "2022-03-01 09:00:00", "Harper", "one", nil, nil),
		msg("Harper", "2022-03-01 09:01:00", "Me", "two", nil, nil),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReplaceConversationMessages(ctx, b, source.Signal, []signal.Message{
		msg("MJ", "2022-03-02 10:00:00", "MJ", "three", nil, nil),
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := st.ArchiveStats(ctx)
	if err != nil {
		t.Fatalf("ArchiveStats: %v", err)
	}
	if stats.Conversations != 2 || stats.Messages != 3 {
		t.Errorf("stats = %+v, want {Conversations:2 Messages:3}", stats)
	}

	// Cross-check against the listing-derived totals the full render uses.
	convs, err := st.ListConversations(ctx)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	total := 0
	for _, c := range convs {
		total += c.MessageCount
	}
	if len(convs) != stats.Conversations || total != stats.Messages {
		t.Errorf("listing-derived totals (%d convs / %d msgs) disagree with ArchiveStats %+v",
			len(convs), total, stats)
	}
}

// TestListConversationsPreviewSurvivesFrontLoadedWhitespace guards the
// substr(lm.body, 1, 1024) prefix width: a last message that front-loads
// hundreds of whitespace characters before its text must still produce a
// non-empty sidebar preview (the #80 review's substr-starvation finding —
// at the old 320-char prefix this preview came back empty).
func TestListConversationsPreviewSurvivesFrontLoadedWhitespace(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	id, err := st.UpsertConversation(ctx, source.Signal, "Padded")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat(" ", 600) + "actual preview text"
	if _, err := st.ReplaceConversationMessages(ctx, id, source.Signal,
		[]signal.Message{msg("Padded", "2022-03-01 09:00:00", "Pad", body, nil, nil)}); err != nil {
		t.Fatal(err)
	}

	convs, err := st.ListConversations(ctx)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(convs) != 1 {
		t.Fatalf("conversations = %d, want 1", len(convs))
	}
	if got := convs[0].LastPreview; got != "actual preview text" {
		t.Errorf("LastPreview = %q, want %q", got, "actual preview text")
	}
}
