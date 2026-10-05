package webui

import (
	"bytes"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSessionsCreateValidateDelete(t *testing.T) {
	s := NewSessions()
	s.now = func() time.Time { return time.Unix(1000, 0) }

	id := s.Create()
	if id == "" {
		t.Fatal("Create returned an empty id")
	}
	if !s.Validate(id) {
		t.Fatal("Validate(fresh session) = false, want true")
	}
	if s.Validate("not-a-session") {
		t.Fatal("Validate(unknown) = true, want false")
	}
	s.Delete(id)
	if s.Validate(id) {
		t.Fatal("Validate(deleted session) = true, want false")
	}
}

func TestSessionsIdleExpiry(t *testing.T) {
	s := NewSessions()
	s.ttl = time.Minute
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }

	id := s.Create()

	// Activity refreshes the idle clock.
	now = now.Add(30 * time.Second)
	if !s.Validate(id) {
		t.Fatal("Validate after 30s = false, want true")
	}
	now = now.Add(30 * time.Second)
	if !s.Validate(id) {
		t.Fatal("Validate after another 30s = false, want true")
	}

	// A full TTL of silence drops it.
	now = now.Add(time.Minute)
	if s.Validate(id) {
		t.Fatal("Validate after a full idle TTL = true, want false")
	}
}

func TestSessionsCapEvictsOldest(t *testing.T) {
	s := NewSessions()
	base := time.Unix(1000, 0)
	tick := 0
	s.now = func() time.Time { return base.Add(time.Duration(tick) * time.Second) }

	ids := make([]string, 0, maxSessions+1)
	for i := 0; i < maxSessions+1; i++ {
		tick++
		ids = append(ids, s.Create())
	}
	if s.Len() != maxSessions {
		t.Fatalf("Len = %d, want %d", s.Len(), maxSessions)
	}
	if s.Validate(ids[0]) {
		t.Error("the oldest session survived eviction")
	}
	if !s.Validate(ids[len(ids)-1]) {
		t.Error("the newest session was evicted")
	}
}

func TestOneTimeCodesSingleUseAndExpiry(t *testing.T) {
	c := NewOneTimeCodes()
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }

	code := c.Issue()
	if len(code) != tokenBytes*2 {
		t.Fatalf("code length = %d, want %d hex chars", len(code), tokenBytes*2)
	}
	if !c.Redeem(code) {
		t.Fatal("first Redeem = false, want true")
	}
	if c.Redeem(code) {
		t.Fatal("second Redeem = true, want single use")
	}
	if c.Redeem("") {
		t.Fatal("Redeem(empty) = true, want false")
	}

	expiring := c.Issue()
	now = now.Add(OneTimeCodeTTL)
	if c.Redeem(expiring) {
		t.Fatal("Redeem(expired code) = true, want false")
	}
}

func TestLogRingFiltersBelowInfo(t *testing.T) {
	ring := NewLogRing()
	logger := slog.New(ring)

	logger.Debug("dropped")
	logger.Info("kept", "pin", "bt")
	logger.Warn("also kept")

	resp := ring.Entries(0, DefaultLogsLimit)
	if len(resp.Entries) != 2 {
		t.Fatalf("entries = %d, want 2 (debug must be dropped): %+v", len(resp.Entries), resp.Entries)
	}
	if resp.Entries[0].Msg != "kept" || resp.Entries[0].Level != "info" {
		t.Errorf("first entry = %+v", resp.Entries[0])
	}
	if resp.Entries[0].Attrs["pin"] != "bt" {
		t.Errorf("attrs = %v, want pin=bt", resp.Entries[0].Attrs)
	}
	if resp.Entries[1].Level != "warn" {
		t.Errorf("second entry level = %q, want warn", resp.Entries[1].Level)
	}

	if ring.Enabled(t.Context(), slog.LevelDebug) {
		t.Error("Enabled(debug) = true, want false")
	}
	if !ring.Enabled(t.Context(), slog.LevelInfo) {
		t.Error("Enabled(info) = false, want true")
	}
}

func TestLogRingWraparoundAndPaging(t *testing.T) {
	ring := NewLogRing()
	logger := slog.New(ring)
	for i := 0; i < LogRingCapacity+5; i++ {
		logger.Info("line")
	}

	// The five oldest records are evicted, so the first retained seq is 6.
	first := ring.Entries(0, 1)
	if len(first.Entries) != 1 || first.Entries[0].Seq != 6 {
		t.Fatalf("first retained = %+v, want seq 6", first.Entries)
	}

	page1 := ring.Entries(0, 500)
	if len(page1.Entries) != 500 {
		t.Fatalf("page1 len = %d, want 500", len(page1.Entries))
	}
	if page1.Entries[0].Seq != 6 || page1.Entries[499].Seq != 505 {
		t.Fatalf("page1 seq range = %d..%d, want 6..505", page1.Entries[0].Seq, page1.Entries[499].Seq)
	}
	if page1.Next != 505 {
		t.Fatalf("page1.Next = %d, want 505", page1.Next)
	}

	page2 := ring.Entries(page1.Next, 500)
	if len(page2.Entries) != 500 || page2.Entries[0].Seq != 506 || page2.Next != 1005 {
		t.Fatalf("page2 = %d entries, next %d, first %d", len(page2.Entries), page2.Next, page2.Entries[0].Seq)
	}

	page3 := ring.Entries(page2.Next, 500)
	if len(page3.Entries) != 0 || page3.Next != 1005 {
		t.Fatalf("page3 = %d entries, next %d; want empty and next 1005", len(page3.Entries), page3.Next)
	}
}

// TestEmbeddedAssetsContainNoBrowserStorageOrTokenAPIs enforces the hard rule:
// the browser holds only an HttpOnly session cookie. No asset may read/write
// localStorage or sessionStorage, and none may set an Authorization header.
func TestEmbeddedAssetsContainNoBrowserStorageOrTokenAPIs(t *testing.T) {
	forbidden := []string{"localStorage", "sessionStorage", "Authorization"}
	err := fs.WalkDir(assetsFS, "assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(assetsFS, path)
		if err != nil {
			return err
		}
		for _, needle := range forbidden {
			if bytes.Contains(data, []byte(needle)) {
				t.Errorf("%s contains forbidden string %q", path, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded assets: %v", err)
	}
}

func TestRenderIndexInjectsModeAndBase(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := RenderIndex(rec, Page{Mode: "agent", APIBase: "/v1"}); err != nil {
		t.Fatalf("RenderIndex: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `data-yab-mode="agent"`) {
		t.Errorf("index does not carry the agent mode:\n%s", out)
	}
	if !strings.Contains(out, `data-yab-api="/v1"`) {
		t.Errorf("index does not carry the API base:\n%s", out)
	}
}

func TestRenderLoginCarriesCodeAndError(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := RenderLogin(rec, LoginPage{Code: "abc123", Error: "nope"}, 401); err != nil {
		t.Fatalf("RenderLogin: %v", err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `value="abc123"`) {
		t.Errorf("login form does not carry the one-time code:\n%s", out)
	}
	if !strings.Contains(out, "nope") {
		t.Errorf("login form does not carry the error:\n%s", out)
	}
}
