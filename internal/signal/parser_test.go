package signal

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name      string
		conv      string
		input     string
		want      []Message
		wantSkips int
	}{
		{
			name: "basic two messages",
			conv: "MJ",
			input: "[2021-12-30 02:58:19] MJ: hey are we still on for tomorrow?\n" +
				"[2021-12-30 02:59:01] Me: yep, 10am\n",
			want: []Message{
				{Conversation: "MJ", TimestampRaw: "2021-12-30 02:58:19", Sender: "MJ", Body: "hey are we still on for tomorrow?"},
				{Conversation: "MJ", TimestampRaw: "2021-12-30 02:59:01", Sender: "Me", Body: "yep, 10am"},
			},
		},
		{
			name:  "system event with empty body",
			conv:  "MJ",
			input: "[2021-12-18 03:14:37] No-Sender:\n",
			want: []Message{
				{Conversation: "MJ", TimestampRaw: "2021-12-18 03:14:37", Sender: "No-Sender", Body: "", IsSystem: true},
			},
		},
		{
			name: "multi-line body preserves newlines",
			conv: "Harper",
			input: "[2022-01-01 10:00:00] Harper: line one\n" +
				"line two\n" +
				"line three\n" +
				"[2022-01-01 10:05:00] Me: ok\n",
			want: []Message{
				{Conversation: "Harper", TimestampRaw: "2022-01-01 10:00:00", Sender: "Harper", Body: "line one\nline two\nline three"},
				{Conversation: "Harper", TimestampRaw: "2022-01-01 10:05:00", Sender: "Me", Body: "ok"},
			},
		},
		{
			name:  "colon in body keeps remainder",
			conv:  "MJ",
			input: "[2022-02-02 09:00:00] MJ: time is 09:00 sharp\n",
			want: []Message{
				{Conversation: "MJ", TimestampRaw: "2022-02-02 09:00:00", Sender: "MJ", Body: "time is 09:00 sharp"},
			},
		},
		{
			name:  "image attachment",
			conv:  "Harper",
			input: "[2022-03-03 12:00:00] Harper: look ![a cat](media/cat.jpg)\n",
			want: []Message{{
				Conversation: "Harper", TimestampRaw: "2022-03-03 12:00:00", Sender: "Harper",
				Body:        "look ![a cat](media/cat.jpg)",
				Attachments: []Attachment{{Kind: KindImage, OriginalName: "a cat", RelPath: "media/cat.jpg"}},
			}},
		},
		{
			name:  "file attachment",
			conv:  "Harper",
			input: "[2022-03-03 12:01:00] Me: [lease.pdf](media/lease.pdf)\n",
			want: []Message{{
				Conversation: "Harper", TimestampRaw: "2022-03-03 12:01:00", Sender: "Me",
				Body:        "[lease.pdf](media/lease.pdf)",
				Attachments: []Attachment{{Kind: KindFile, OriginalName: "lease.pdf", RelPath: "media/lease.pdf"}},
			}},
		},
		{
			name:  "bare and markdown links deduped",
			conv:  "MJ",
			input: "[2022-04-04 08:00:00] MJ: see https://example.com/x and [same](https://example.com/x).\n",
			want: []Message{{
				Conversation: "MJ", TimestampRaw: "2022-04-04 08:00:00", Sender: "MJ",
				Body:  "see https://example.com/x and [same](https://example.com/x).",
				Links: []Link{{URL: "https://example.com/x"}},
			}},
		},
		{
			name:  "trailing punctuation stripped from bare url",
			conv:  "MJ",
			input: "[2022-04-05 08:00:00] MJ: go to https://example.com/page.\n",
			want: []Message{{
				Conversation: "MJ", TimestampRaw: "2022-04-05 08:00:00", Sender: "MJ",
				Body:  "go to https://example.com/page.",
				Links: []Link{{URL: "https://example.com/page"}},
			}},
		},
		{
			name: "duplicate identical lines get distinct seq",
			conv: "MJ",
			input: "[2022-05-05 07:00:00] MJ: ping\n" +
				"[2022-05-05 07:00:00] MJ: ping\n",
			want: []Message{
				{Conversation: "MJ", TimestampRaw: "2022-05-05 07:00:00", Sender: "MJ", Body: "ping", Seq: 0},
				{Conversation: "MJ", TimestampRaw: "2022-05-05 07:00:00", Sender: "MJ", Body: "ping", Seq: 1},
			},
		},
		{
			name: "malformed leading line skipped",
			conv: "MJ",
			input: "garbage with no anchor\n" +
				"[2022-06-06 06:00:00] MJ: real\n",
			want: []Message{
				{Conversation: "MJ", TimestampRaw: "2022-06-06 06:00:00", Sender: "MJ", Body: "real"},
			},
			wantSkips: 1,
		},
		{
			name:      "invalid timestamp skipped",
			conv:      "MJ",
			input:     "[2022-13-40 99:99:99] MJ: nope\n",
			want:      nil,
			wantSkips: 1,
		},
		{
			name:  "unterminated final line still parsed",
			conv:  "MJ",
			input: "[2022-07-07 05:00:00] MJ: no trailing newline",
			want: []Message{
				{Conversation: "MJ", TimestampRaw: "2022-07-07 05:00:00", Sender: "MJ", Body: "no trailing newline"},
			},
		},
		{
			name:  "emoji and unicode preserved",
			conv:  "Harper",
			input: "[2022-08-08 04:00:00] Harper: café ☕ 🚀 naïve\n",
			want: []Message{
				{Conversation: "Harper", TimestampRaw: "2022-08-08 04:00:00", Sender: "Harper", Body: "café ☕ 🚀 naïve"},
			},
		},
		{
			// An anchor whose sender field is empty (e.g. an upstream export
			// glitch) must be skipped, not started. The next real message must
			// still be parsed normally.
			name: "empty sender is skipped",
			conv: "MJ",
			input: "[2022-01-01 10:00:00] : ignored body\n" +
				"[2022-01-01 10:05:00] MJ: real message\n",
			want: []Message{
				{Conversation: "MJ", TimestampRaw: "2022-01-01 10:05:00", Sender: "MJ", Body: "real message"},
			},
			wantSkips: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs, skips, err := ParseAll(tt.conv, strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("Parse returned error: %v", err)
			}
			if len(skips) != tt.wantSkips {
				t.Errorf("skips = %d, want %d (%v)", len(skips), tt.wantSkips, skips)
			}
			if len(msgs) != len(tt.want) {
				t.Fatalf("got %d messages, want %d: %#v", len(msgs), len(tt.want), msgs)
			}
			for i := range tt.want {
				assertMessage(t, i, msgs[i], tt.want[i])
			}
		})
	}
}

func assertMessage(t *testing.T, i int, got, want Message) {
	t.Helper()
	if got.Sender != want.Sender {
		t.Errorf("msg[%d].Sender = %q, want %q", i, got.Sender, want.Sender)
	}
	if got.TimestampRaw != want.TimestampRaw {
		t.Errorf("msg[%d].TimestampRaw = %q, want %q", i, got.TimestampRaw, want.TimestampRaw)
	}
	if got.Body != want.Body {
		t.Errorf("msg[%d].Body = %q, want %q", i, got.Body, want.Body)
	}
	if got.IsSystem != want.IsSystem {
		t.Errorf("msg[%d].IsSystem = %v, want %v", i, got.IsSystem, want.IsSystem)
	}
	if got.Seq != want.Seq {
		t.Errorf("msg[%d].Seq = %d, want %d", i, got.Seq, want.Seq)
	}
	if !got.Timestamp.Equal(want.Timestamp) && !want.Timestamp.IsZero() {
		t.Errorf("msg[%d].Timestamp = %v, want %v", i, got.Timestamp, want.Timestamp)
	}
	if len(got.Attachments) != len(want.Attachments) {
		t.Fatalf("msg[%d] attachments = %#v, want %#v", i, got.Attachments, want.Attachments)
	}
	for j := range want.Attachments {
		if got.Attachments[j] != want.Attachments[j] {
			t.Errorf("msg[%d].Attachments[%d] = %#v, want %#v", i, j, got.Attachments[j], want.Attachments[j])
		}
	}
	if len(got.Links) != len(want.Links) {
		t.Fatalf("msg[%d] links = %#v, want %#v", i, got.Links, want.Links)
	}
	for j := range want.Links {
		if got.Links[j] != want.Links[j] {
			t.Errorf("msg[%d].Links[%d] = %#v, want %#v", i, j, got.Links[j], want.Links[j])
		}
	}
}

func TestMessageIDStableAndSeqDistinct(t *testing.T) {
	in := "[2022-05-05 07:00:00] MJ: ping\n[2022-05-05 07:00:00] MJ: ping\n"
	a, _, err := ParseAll("MJ", strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := ParseAll("MJ", strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	// Stable across runs.
	for i := range a {
		if a[i].ID() != b[i].ID() {
			t.Errorf("ID not stable for msg %d: %s != %s", i, a[i].ID(), b[i].ID())
		}
	}
	// Distinct between the two identical lines (seq disambiguation).
	if a[0].ID() == a[1].ID() {
		t.Errorf("identical lines produced identical IDs: %s", a[0].ID())
	}
}
