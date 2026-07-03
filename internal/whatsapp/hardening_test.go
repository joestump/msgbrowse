package whatsapp

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// parseString parses an inline result.json document with a fixed UTC zone.
func parseString(t *testing.T, doc string) ([]Conversation, []ParseError) {
	t.Helper()
	convs, skips, err := ParseAll(strings.NewReader(doc), ParseOptions{Location: time.UTC})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return convs, skips
}

// TestThreeWayNameCollision: the suffixed collision name is re-checked against
// taken names, so three chats sharing a display name AND a JID local part
// (possible across the @g.us/@lid/@s.whatsapp.net domains) never merge into
// one conversation (issue #90 fold-in from #94's review).
func TestThreeWayNameCollision(t *testing.T) {
	const doc = `{
		"15551230000@g.us":           {"name": "Dup", "messages": {"1": {"from_me": false, "timestamp": 1748768400, "data": "a"}}},
		"15551230000@lid":            {"name": "Dup", "messages": {"1": {"from_me": false, "timestamp": 1748768401, "data": "b"}}},
		"15551230000@s.whatsapp.net": {"name": "Dup", "messages": {"1": {"from_me": false, "timestamp": 1748768402, "data": "c"}}}
	}`
	convs, skips := parseString(t, doc)
	if len(skips) != 0 {
		t.Fatalf("skips = %v, want none", skips)
	}
	if len(convs) != 3 {
		t.Fatalf("conversations = %d (%v), want 3", len(convs), convNames(convs))
	}
	want := []string{"Dup", "Dup (15551230000)", "Dup (15551230000) #2"}
	got := convNames(convs)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
	// Determinism: a re-parse assigns the same names in the same order.
	again, _ := parseString(t, doc)
	if !reflect.DeepEqual(convNames(again), want) {
		t.Errorf("re-parse names = %v, want %v", convNames(again), want)
	}
}

// TestEmptyChatsAreSkipped: chats with no messages — either none in the JSON
// or all entries malformed — are never emitted, so empty conversation rows
// can't persist (issue #90 fold-in from #94's review).
func TestEmptyChatsAreSkipped(t *testing.T) {
	const doc = `{
		"15550001111@s.whatsapp.net": {"name": "Keep", "messages": {"1": {"from_me": false, "timestamp": 1748768400, "data": "hello"}}},
		"15550002222@s.whatsapp.net": {"name": "No Messages", "messages": {}},
		"15550003333@s.whatsapp.net": {"name": "All Malformed", "messages": {"1": {"from_me": false, "data": "no timestamp"}}}
	}`
	convs, skips := parseString(t, doc)
	if got := convNames(convs); !reflect.DeepEqual(got, []string{"Keep"}) {
		t.Errorf("conversations = %v, want [Keep] only", got)
	}
	// The malformed entry is still skip-logged (REQ-0009-003) even though its
	// chat is dropped.
	if len(skips) != 1 || skips[0].Chat != "15550003333@s.whatsapp.net" {
		t.Errorf("skips = %v, want the one malformed entry", skips)
	}
}
