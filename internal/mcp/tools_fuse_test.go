package mcp

import (
	"testing"

	"github.com/joestump/msgbrowse/internal/store"
)

// TestFuseResultsTieOrderDeterministic ensures equal-score hits are ordered by
// message_id, not by random map iteration.
func TestFuseResultsTieOrderDeterministic(t *testing.T) {
	// One keyword-only hit and one semantic-only hit, each at rank 0 → equal RRF
	// score (1/61). Tie must resolve by message_id ascending, every run.
	fts := []store.SearchHit{{MessageID: 50, ConversationName: "A", TS: "t", Snippet: "kw"}}
	sem := []store.ScoredMessage{{MessageID: 10, ConversationName: "B", TS: "t", Body: "sem"}}

	for i := 0; i < 50; i++ {
		out := fuseResults(fts, sem, 10)
		if len(out) != 2 {
			t.Fatalf("got %d hits", len(out))
		}
		if out[0].MessageID != 10 || out[1].MessageID != 50 {
			t.Fatalf("tie order not deterministic: %d then %d", out[0].MessageID, out[1].MessageID)
		}
	}
}

// TestFuseResultsKeepsKeywordSnippet ensures a message in both lists keeps the
// keyword snippet (fts iterated first) and sums both ranks' scores.
func TestFuseResultsKeepsKeywordSnippet(t *testing.T) {
	fts := []store.SearchHit{{MessageID: 7, ConversationName: "A", TS: "t", Snippet: "the <mark>lease</mark>"}}
	sem := []store.ScoredMessage{{MessageID: 7, ConversationName: "A", TS: "t", Body: "the lease agreement"}}
	out := fuseResults(fts, sem, 10)
	if len(out) != 1 {
		t.Fatalf("expected 1 fused hit, got %d", len(out))
	}
	if out[0].Text != "the <mark>lease</mark>" {
		t.Errorf("fused hit lost keyword snippet: %q", out[0].Text)
	}
	// Score is the sum of both lists' RRF contributions (2 * 1/61).
	if out[0].Score <= 1.0/61.0 {
		t.Errorf("fused score %v not greater than single-list contribution", out[0].Score)
	}
}
