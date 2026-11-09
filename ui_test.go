package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeUIBodyEscapedTextAndStrictJSON(t *testing.T) {
	raw := `{"version":1,"text":"` + strings.Repeat(`\u0001`, 64000) + `","attachments":[]}`
	var q struct {
		Version     int    `json:"version"`
		Text        string `json:"text"`
		Attachments []any  `json:"attachments"`
	}
	r := httptest.NewRequest("POST", "/v1/incoming", strings.NewReader(raw))
	w := httptest.NewRecorder()
	if !decodeUIBody(w, r, &q) || len(q.Text) != 64000 {
		t.Fatalf("escaped boundary rejected: %d", len(q.Text))
	}
	for _, bad := range []string{`{"version":1,"version":1,"text":"x","attachments":[]}`, `{"version":1,"text":"x","attachments":[]} junk`} {
		r = httptest.NewRequest("POST", "/v1/incoming", strings.NewReader(bad))
		w = httptest.NewRecorder()
		if decodeUIBody(w, r, &q) {
			t.Fatalf("accepted malformed JSON %q", bad)
		}
	}
}
