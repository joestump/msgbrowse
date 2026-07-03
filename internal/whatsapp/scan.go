package whatsapp

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// This file is the doctor-facing summary scan of a result.json export
// (SPEC-0009 REQ-0009-009). It deliberately reuses the parser's decode types
// so the JSON knowledge stays in one package, but it does NOT map messages —
// it only counts chats/messages, records the device platform, and samples raw
// media references (media_base + data) so doctor can verify they resolve
// inside the archive root without importing anything.

// Device platforms as the exporter writes them in each chat's "type" field.
// The platform decides which backup-prerequisite remediation doctor prints
// (iOS: local Finder/iTunes backup; Android: backup + 64-digit key).
const (
	DeviceIOS     = "ios"
	DeviceAndroid = "android"
)

// MediaRef is one raw media reference from the export: the chat's media_base
// prefix plus the message's data path (full path = MediaBase + Data, per the
// exporter's own <base href> semantics).
type MediaRef struct {
	MediaBase string
	Data      string
}

// ExportSummary is what doctor needs to know about a result.json without
// importing it.
type ExportSummary struct {
	// Chats is the number of top-level chat objects (malformed ones included).
	Chats int
	// Messages is the total message-entry count across all chats.
	Messages int
	// Device is the exporting platform ("ios"/"android") when every decodable
	// chat agrees, or "" when absent or mixed.
	Device string
	// MediaRefs holds up to the requested limit of media references, in
	// deterministic (sorted-JID, ascending message-key) order.
	MediaRefs []MediaRef
	// MediaBaseChats maps each distinct non-empty media_base prefix to the
	// number of chats carrying it. An ABSOLUTE media_base outside the archive
	// root is the signature of an export whose media was referenced in place
	// rather than copied under the root — doctor's headline WhatsApp check.
	MediaBaseChats map[string]int
}

// scanChat mirrors the chat fields the summary scan consumes; messages stay
// raw so one malformed message never hides the rest.
type scanChat struct {
	Type      string                     `json:"type"`
	MediaBase string                     `json:"media_base"`
	Messages  map[string]json.RawMessage `json:"messages"`
}

// scanMessage is the minimal per-message view: enough to recognize a real
// media reference (media==true with a non-empty, non-sentinel data path).
type scanMessage struct {
	Media bool    `json:"media"`
	Data  *string `json:"data"`
}

// ScanExport summarizes a result.json stream for doctor. It never fails on
// malformed chats or messages (they are simply not counted as media refs);
// only an undecodable top level is an error. limit caps how many media
// references are collected (<=0 means none).
func ScanExport(r io.Reader, limit int) (ExportSummary, error) {
	var sum ExportSummary
	var top map[string]json.RawMessage
	if err := json.NewDecoder(r).Decode(&top); err != nil {
		return sum, fmt.Errorf("decode %s: %w", ResultFile, err)
	}
	sum.Chats = len(top)
	sum.MediaBaseChats = map[string]int{}

	jids := make([]string, 0, len(top))
	for jid := range top {
		jids = append(jids, jid)
	}
	sort.Strings(jids)

	device, mixed := "", false
	for _, jid := range jids {
		var c scanChat
		if err := json.Unmarshal(top[jid], &c); err != nil {
			continue // malformed chat: import will skip-log it; nothing to sample
		}
		sum.Messages += len(c.Messages)
		if c.MediaBase != "" {
			sum.MediaBaseChats[c.MediaBase]++
		}
		switch {
		case c.Type == "":
		case device == "":
			device = c.Type
		case device != c.Type:
			mixed = true
		}

		keys := make([]string, 0, len(c.Messages))
		for k := range c.Messages {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if len(sum.MediaRefs) >= limit {
				break
			}
			var m scanMessage
			if err := json.Unmarshal(c.Messages[k], &m); err != nil {
				continue
			}
			if !m.Media || m.Data == nil || *m.Data == "" || *m.Data == mediaMissingSentinel {
				continue
			}
			sum.MediaRefs = append(sum.MediaRefs, MediaRef{MediaBase: c.MediaBase, Data: *m.Data})
		}
	}
	if !mixed {
		sum.Device = device
	}
	return sum, nil
}
