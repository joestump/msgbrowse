// Governing: SPEC-0011 REQ "Pairing Initiation" — the pairing payload as QR
// bytes and manual code carrying the same fields; this package owns the
// schema SPEC-0010's Connect page consumes.
package devices

import (
	"encoding/json"
	"strings"
	"testing"
)

func testPayload(t *testing.T) *PairingPayload {
	t.Helper()
	id := mustIdentity(t, "importer")
	w, err := OpenWindow(0)
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewPairingPayload("192.168.1.10:8788", w.Token(), id.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPayloadWireSchema pins the exact v1 wire format: compact JSON with the
// four documented keys and no others. SPEC-0010's Connect page and future
// replicas depend on this shape byte-for-byte.
func TestPayloadWireSchema(t *testing.T) {
	p := testPayload(t)
	qr, err := p.EncodeQR()
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(qr, &raw); err != nil {
		t.Fatalf("QR bytes are not JSON: %v", err)
	}
	for _, key := range []string{"v", "endpoint", "token", "fp"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("payload missing required key %q", key)
		}
	}
	if len(raw) != 4 {
		t.Errorf("payload has %d keys, want exactly 4: %v", len(raw), raw)
	}
	if v, ok := raw["v"].(float64); !ok || int(v) != PayloadVersion {
		t.Errorf("v = %v, want %d", raw["v"], PayloadVersion)
	}
	if fp := raw["fp"].(string); len(fp) != 64 || strings.ToLower(fp) != fp {
		t.Errorf("fp %q is not 64-char lowercase hex", fp)
	}
}

// TestPayloadRoundTrips decodes both presentations back to identical fields.
func TestPayloadRoundTrips(t *testing.T) {
	p := testPayload(t)

	qr, err := p.EncodeQR()
	if err != nil {
		t.Fatal(err)
	}
	manual, err := p.EncodeManualCode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(manual, ManualCodePrefix) {
		t.Errorf("manual code %q lacks prefix %q", manual, ManualCodePrefix)
	}
	if strings.ContainsAny(manual, " \t\n") {
		t.Errorf("manual code contains whitespace: %q", manual)
	}

	tests := []struct {
		name string
		data []byte
	}{
		{name: "QR bytes", data: qr},
		{name: "manual code", data: []byte(manual)},
		{name: "manual code with pasted whitespace", data: []byte("  " + manual + "\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodePayload(tt.data)
			if err != nil {
				t.Fatalf("DecodePayload: %v", err)
			}
			if *got != *p {
				t.Errorf("round-trip mismatch: got %+v, want %+v", got, p)
			}
		})
	}
}

func TestNewPairingPayloadValidation(t *testing.T) {
	id := mustIdentity(t, "importer")
	fp := id.Fingerprint()

	tests := []struct {
		name              string
		endpoint, tok, fp string
		wantErr           bool
	}{
		{name: "valid", endpoint: "10.0.0.2:8788", tok: "tok", fp: fp},
		{name: "display-form fingerprint canonicalized", endpoint: "10.0.0.2:8788", tok: "tok", fp: strings.ToUpper(fp)},
		{name: "endpoint without port", endpoint: "10.0.0.2", tok: "tok", fp: fp, wantErr: true},
		{name: "empty endpoint", endpoint: "", tok: "tok", fp: fp, wantErr: true},
		{name: "empty token", endpoint: "10.0.0.2:8788", tok: "", fp: fp, wantErr: true},
		{name: "malformed fingerprint", endpoint: "10.0.0.2:8788", tok: "tok", fp: "beef", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewPairingPayload(tt.endpoint, tt.tok, tt.fp)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NewPairingPayload succeeded, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewPairingPayload: %v", err)
			}
			if p.Fingerprint != fp {
				t.Errorf("fingerprint = %q, want canonical %q", p.Fingerprint, fp)
			}
		})
	}
}

func TestDecodePayloadRejects(t *testing.T) {
	p := testPayload(t)
	valid, err := p.EncodeQR()
	if err != nil {
		t.Fatal(err)
	}

	mutate := func(f func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(valid, &m); err != nil {
			t.Fatal(err)
		}
		f(m)
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	tests := []struct {
		name string
		data []byte
	}{
		{name: "not JSON", data: []byte("nope")},
		{name: "empty", data: nil},
		{name: "bad manual base64", data: []byte(ManualCodePrefix + "!!!not-base64!!!")},
		{name: "future version", data: mutate(func(m map[string]any) { m["v"] = 2 })},
		{name: "version zero", data: mutate(func(m map[string]any) { delete(m, "v") })},
		{name: "missing token", data: mutate(func(m map[string]any) { m["token"] = "" })},
		{name: "missing endpoint", data: mutate(func(m map[string]any) { m["endpoint"] = "" })},
		{name: "corrupt fingerprint", data: mutate(func(m map[string]any) { m["fp"] = "abc" })},
		{name: "unknown extra field", data: mutate(func(m map[string]any) { m["extra"] = true })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := DecodePayload(tt.data); err == nil {
				t.Errorf("DecodePayload accepted %s: %+v", tt.data, got)
			}
		})
	}
}
