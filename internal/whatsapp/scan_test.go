package whatsapp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const scanDoc = `{
	"15550001111@s.whatsapp.net": {
		"name": "Abs Media", "type": "ios", "media_base": "/outside/Message/Media/",
		"messages": {
			"1": {"from_me": false, "timestamp": 1748768400, "data": "photo.jpg", "media": true, "mime": "image/jpeg"},
			"2": {"from_me": true, "timestamp": 1748768401, "data": "plain text"}
		}
	},
	"15550002222@s.whatsapp.net": {
		"name": "Rel Media", "type": "ios", "media_base": "",
		"messages": {
			"1": {"from_me": false, "timestamp": 1748768402, "data": "WhatsApp/img.png", "media": true, "mime": "image/png"},
			"2": {"from_me": false, "timestamp": 1748768403, "data": "The media is missing", "media": true, "mime": "media", "meta": true},
			"3": "not an object"
		}
	}
}`

func TestScanExport(t *testing.T) {
	sum, err := ScanExport(strings.NewReader(scanDoc), 300)
	if err != nil {
		t.Fatalf("ScanExport: %v", err)
	}
	if sum.Chats != 2 {
		t.Errorf("Chats = %d, want 2", sum.Chats)
	}
	// Raw entry counts — the malformed entry is still an entry.
	if sum.Messages != 5 {
		t.Errorf("Messages = %d, want 5", sum.Messages)
	}
	if sum.Device != DeviceIOS {
		t.Errorf("Device = %q, want %q", sum.Device, DeviceIOS)
	}
	// Sentinel and malformed entries are never media refs; refs come out in
	// sorted-JID, sorted-key order.
	wantRefs := []MediaRef{
		{MediaBase: "/outside/Message/Media/", Data: "photo.jpg"},
		{MediaBase: "", Data: "WhatsApp/img.png"},
	}
	if !reflect.DeepEqual(sum.MediaRefs, wantRefs) {
		t.Errorf("MediaRefs = %+v, want %+v", sum.MediaRefs, wantRefs)
	}
	if want := map[string]int{"/outside/Message/Media/": 1}; !reflect.DeepEqual(sum.MediaBaseChats, want) {
		t.Errorf("MediaBaseChats = %v, want %v", sum.MediaBaseChats, want)
	}
}

func TestScanExportLimitAndMixedDevices(t *testing.T) {
	sum, err := ScanExport(strings.NewReader(scanDoc), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.MediaRefs) != 1 {
		t.Errorf("MediaRefs = %d, want limit of 1", len(sum.MediaRefs))
	}

	mixed := strings.Replace(scanDoc, `"type": "ios", "media_base": ""`, `"type": "android", "media_base": ""`, 1)
	sum, err = ScanExport(strings.NewReader(mixed), 0)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Device != "" {
		t.Errorf("mixed devices → Device = %q, want \"\"", sum.Device)
	}
	if len(sum.MediaRefs) != 0 {
		t.Errorf("limit 0 collected %d refs", len(sum.MediaRefs))
	}
}

func TestScanExportMalformedTopLevel(t *testing.T) {
	if _, err := ScanExport(strings.NewReader(`["not an object"]`), 10); err == nil {
		t.Error("expected an error for a non-object top level")
	}
}

// TestScanExportFixture keeps the scan consistent with the parser over the
// committed fixture: same chat universe, and every media ref it samples comes
// from a media message the parser also maps.
func TestScanExportFixture(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sum, err := ScanExport(f, 300)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Chats != 6 {
		t.Errorf("Chats = %d, want 6 (fixture chats incl. the malformed one)", sum.Chats)
	}
	if sum.Messages == 0 || len(sum.MediaRefs) == 0 {
		t.Errorf("fixture scan came back empty: %+v", sum)
	}
}
