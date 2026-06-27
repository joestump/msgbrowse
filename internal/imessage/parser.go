// Package imessage parses the plain-text corpus produced by imessage-exporter
// (https://github.com/ReagentX/imessage-exporter) with `-f txt`. It targets the
// 4.2.0 output format and turns each per-conversation <ChatName>.txt file into
// the shared [signal.Message] records the rest of msgbrowse consumes, tagged at
// import time with source="imessage".
//
// The 4.2.0 txt format renders each message as:
//
//	May 20, 2020  9:10:11 AM            ← timestamp (space-padded hour), optional " (Read by …)"
//	Me                                 ← sender ("Me", a contact name, or a handle)
//	the message body                   ← zero or more body lines
//	attachments/AB/CD/IMG_1234.HEIC    ← attachment path lines (no "Attachment:" prefix)
//	Tapbacks:                          ← reactions, with indented detail lines
//	    Loved by Sample
//	This message responded to an earlier message.
//	                                   ← blank line separates messages
//
// Because attachments appear as bare path lines with no marker, they are
// detected heuristically (a spaceless path ending in a known media extension).
// Tapbacks, quoted replies (indented), and status notices are skipped. This is
// best-effort and should be validated against a real export; the format is
// version-sensitive.
package imessage

import (
	"bufio"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/joestump/msgbrowse/internal/signal"
)

// timestampLayout parses an imessage-exporter timestamp after runs of spaces are
// collapsed to one (the source format space-pads single-digit hours).
const timestampLayout = "Jan 2, 2006 3:04:05 PM"

// timestampRe anchors the start-of-message line: "Mon DD, YYYY  H:MM:SS AM/PM".
// Any trailing text (e.g. a read receipt) is ignored.
var timestampRe = regexp.MustCompile(`^[A-Z][a-z]{2} \d{1,2}, \d{4}\s+\d{1,2}:\d{2}:\d{2} [AP]M`)

var spaceRun = regexp.MustCompile(`\s+`)

// attachmentExtRe matches a spaceless path/filename ending in a known media or
// document extension — the heuristic for an attachment path line.
var attachmentExtRe = regexp.MustCompile(`(?i)^\S+\.(jpe?g|png|gif|heic|heif|webp|bmp|tiff?|mov|mp4|m4v|3gp|avi|mkv|caf|m4a|amr|aac|wav|mp3|pdf|vcf|zip|docx?|xlsx?|pptx?|csv|txt|html?)$`)

// imageExts are extensions classified as images (others become file attachments).
var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".heic": true, ".heif": true, ".webp": true, ".bmp": true, ".tif": true, ".tiff": true,
}

// noticeLines are status lines emitted by imessage-exporter that are not message
// content and are skipped.
var noticeLines = map[string]bool{
	"Tapbacks:": true,
	"This message responded to an earlier message.":   true,
	"This message was deleted from the conversation!": true,
	"Attachment missing!":                             true,
}

// Parse streams an imessage-exporter txt file from r, emitting one
// [signal.Message] per message block for the given conversation. emit is called
// in file order; if it returns an error, Parse stops and returns it. onSkip, if
// non-nil, is called for non-blank content that appears before the first
// timestamp (a malformed/unsupported preamble).
func Parse(conversation string, r io.Reader, emit func(signal.Message) error, onSkip func(line int, text string)) error {
	br := bufio.NewReader(r)
	seq := newSeqCounter()

	var (
		cur       *signal.Message
		bodyLines []string
		atts      []signal.Attachment
		expect    senderState
		lineNo    int
	)

	flush := func() error {
		if cur == nil {
			return nil
		}
		cur.Body = strings.TrimRight(strings.Join(bodyLines, "\n"), "\n")
		cur.Attachments = atts
		cur.Links = signal.ExtractLinks(cur.Body)
		cur.Seq = seq.next(cur.Conversation, cur.TimestampRaw, cur.Sender, cur.Body)
		m := *cur
		cur, bodyLines, atts, expect = nil, nil, nil, stateNone
		return emit(m)
	}

	for {
		line, readErr := br.ReadString('\n')
		if len(line) > 0 || readErr == nil {
			lineNo++
			text := strings.TrimRight(line, "\r\n")

			if raw, ts, ok := parseTimestamp(text); ok {
				if err := flush(); err != nil {
					return err
				}
				cur = &signal.Message{Conversation: conversation, Timestamp: ts, TimestampRaw: raw}
				expect = stateSender
			} else if cur == nil {
				if strings.TrimSpace(text) != "" && onSkip != nil {
					onSkip(lineNo, text)
				}
			} else if expect == stateSender {
				cur.Sender = strings.TrimSpace(text)
				expect = stateBody
			} else {
				classifyContent(text, &bodyLines, &atts)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return readErr
		}
	}
	return flush()
}

// ParseAll collects every message into a slice (for tests / small files).
func ParseAll(conversation string, r io.Reader) ([]signal.Message, error) {
	var msgs []signal.Message
	err := Parse(conversation, r, func(m signal.Message) error { msgs = append(msgs, m); return nil }, nil)
	return msgs, err
}

// classifyContent sorts a content line into a body line, an attachment, or
// nothing (skipped: blanks, indented tapback/reply detail, status notices).
func classifyContent(line string, bodyLines *[]string, atts *[]signal.Attachment) {
	trimmed := strings.TrimSpace(line)
	switch {
	case trimmed == "":
		// Blank line: message separator / spacing — not body content.
	case line != trimmed && strings.HasPrefix(line, " "):
		// Indented: tapback detail or a quoted reply (the quoted parent is its
		// own top-level block elsewhere) — skip.
	case noticeLines[trimmed]:
		// Status notice — not content.
	case attachmentExtRe.MatchString(trimmed):
		kind := signal.KindFile
		if imageExts[strings.ToLower(filepath.Ext(trimmed))] {
			kind = signal.KindImage
		}
		*atts = append(*atts, signal.Attachment{
			Kind: kind, RelPath: trimmed, OriginalName: filepath.Base(trimmed),
		})
	default:
		*bodyLines = append(*bodyLines, line)
	}
}

// parseTimestamp returns the normalized timestamp text and parsed time if line
// begins with an imessage-exporter timestamp.
func parseTimestamp(line string) (raw string, t time.Time, ok bool) {
	m := timestampRe.FindString(line)
	if m == "" {
		return "", time.Time{}, false
	}
	norm := spaceRun.ReplaceAllString(strings.TrimSpace(m), " ")
	parsed, err := time.Parse(timestampLayout, norm)
	if err != nil {
		return "", time.Time{}, false
	}
	return norm, parsed, true
}

type senderState int

const (
	stateNone senderState = iota
	stateSender
	stateBody
)

// seqCounter assigns the per-conversation Seq disambiguator for byte-identical
// messages (mirrors the signal parser's counter).
type seqCounter struct{ counts map[string]int }

func newSeqCounter() *seqCounter { return &seqCounter{counts: map[string]int{}} }

func (s *seqCounter) next(conv, tsRaw, sender, body string) int {
	key := conv + "\x00" + tsRaw + "\x00" + sender + "\x00" + body
	n := s.counts[key]
	s.counts[key] = n + 1
	return n
}
