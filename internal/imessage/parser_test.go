package imessage

import (
	"strings"
	"testing"

	"github.com/joestump/msgbrowse/internal/signal"
)

func TestParseTimestamp(t *testing.T) {
	tests := []struct {
		in       string
		wantRaw  string
		wantOK   bool
		wantYear int
	}{
		{"May 20, 2020  9:10:11 AM", "May 20, 2020 9:10:11 AM", true, 2020},  // space-padded single-digit hour
		{"May 20, 2020 10:00:00 AM", "May 20, 2020 10:00:00 AM", true, 2020}, // double-digit hour
		{"Jun 05, 2020  2:30:00 PM", "Jun 05, 2020 2:30:00 PM", true, 2020},  // zero-padded day + PM
		{"Hey, are we still on?", "", false, 0},                              // body line
		{"Me", "", false, 0},                                                 // sender line
		{"", "", false, 0},
	}
	for _, tt := range tests {
		raw, ts, ok := parseTimestamp(tt.in)
		if ok != tt.wantOK {
			t.Errorf("parseTimestamp(%q) ok = %v, want %v", tt.in, ok, tt.wantOK)
			continue
		}
		if ok {
			if raw != tt.wantRaw {
				t.Errorf("parseTimestamp(%q) raw = %q, want %q", tt.in, raw, tt.wantRaw)
			}
			if ts.Year() != tt.wantYear {
				t.Errorf("parseTimestamp(%q) year = %d, want %d", tt.in, ts.Year(), tt.wantYear)
			}
		}
	}
}

func TestParseMessages(t *testing.T) {
	const in = `May 20, 2020  9:10:11 AM
Me
Hey, are we still on for tomorrow?

May 20, 2020  9:12:00 AM
MJ
Yep, 10am works
this is a second line

May 20, 2020  9:15:00 AM
MJ
Here's the cabin photo
attachments/AB/CD/cabin.jpeg

May 20, 2020  9:16:00 AM
Me
Check this out https://maps.example.com/cabin
Tapbacks:
    Loved by MJ

May 20, 2020 10:00:00 AM
MJ
ok
This message responded to an earlier message.
`
	msgs, err := ParseAll("MJ", strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 {
		t.Fatalf("got %d messages, want 5: %+v", len(msgs), msgs)
	}

	// m0: owner, single-line body.
	if msgs[0].Sender != "Me" || msgs[0].Body != "Hey, are we still on for tomorrow?" {
		t.Errorf("m0 = %+v", msgs[0])
	}
	// m1: multi-line body preserved.
	if msgs[1].Sender != "MJ" || msgs[1].Body != "Yep, 10am works\nthis is a second line" {
		t.Errorf("m1 body = %q", msgs[1].Body)
	}
	// m2: image attachment detected, path line not in body.
	if len(msgs[2].Attachments) != 1 || msgs[2].Attachments[0].Kind != signal.KindImage {
		t.Errorf("m2 attachments = %+v", msgs[2].Attachments)
	}
	if msgs[2].Attachments[0].RelPath != "attachments/AB/CD/cabin.jpeg" {
		t.Errorf("m2 attachment path = %q", msgs[2].Attachments[0].RelPath)
	}
	if strings.Contains(msgs[2].Body, "cabin.jpeg") {
		t.Errorf("m2 body should not contain the attachment path: %q", msgs[2].Body)
	}
	// m3: link extracted; tapbacks skipped (not in body, no extra message).
	if len(msgs[3].Links) != 1 || msgs[3].Links[0].URL != "https://maps.example.com/cabin" {
		t.Errorf("m3 links = %+v", msgs[3].Links)
	}
	if strings.Contains(msgs[3].Body, "Tapbacks") || strings.Contains(msgs[3].Body, "Loved by") {
		t.Errorf("m3 body leaked tapbacks: %q", msgs[3].Body)
	}
	// m4: reply notice skipped from body.
	if msgs[4].Body != "ok" {
		t.Errorf("m4 body = %q, want \"ok\"", msgs[4].Body)
	}
}

func TestParseOwnerVsHandle(t *testing.T) {
	const in = `Jun 05, 2020  2:30:00 PM
+15551234567
hi from a phone handle

Jun 05, 2020  2:31:00 PM
Me
reply
`
	msgs, err := ParseAll("Group", strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d, want 2", len(msgs))
	}
	if msgs[0].Sender != "+15551234567" {
		t.Errorf("handle sender = %q", msgs[0].Sender)
	}
	if msgs[1].Sender != signal.OwnerSender {
		t.Errorf("owner sender = %q, want %q", msgs[1].Sender, signal.OwnerSender)
	}
}

func TestParseSeqDistinctForIdenticalMessages(t *testing.T) {
	const in = `May 20, 2020  9:00:00 AM
MJ
ping

May 20, 2020  9:00:00 AM
MJ
ping
`
	msgs, err := ParseAll("MJ", strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d, want 2", len(msgs))
	}
	if msgs[0].Seq == msgs[1].Seq {
		t.Errorf("byte-identical messages share Seq %d", msgs[0].Seq)
	}
	if msgs[0].HashWithSource("imessage") == msgs[1].HashWithSource("imessage") {
		t.Errorf("byte-identical messages share storage hash")
	}
}
