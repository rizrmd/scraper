package brand24

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestLoginAndCache(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/user/login-submit/" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.FormValue("login") != "user@example.com" || r.FormValue("password") != "secret" {
			t.Fatal("credentials missing")
		}
		_, _ = io.WriteString(w, `{"result":1}`)
	}))
	defer upstream.Close()
	c, err := New(Config{AppURL: upstream.URL, DataURL: upstream.URL, Email: "user@example.com", Password: "secret"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("login calls = %d, want 1", calls)
	}
}

func TestSyncMentionsSplitsWindowsAndPaginates(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "key" {
			t.Fatal("missing API key")
		}
		mu.Lock()
		calls = append(calls, r.URL.RawQuery)
		mu.Unlock()
		cursor := r.URL.Query().Get("cursor")
		if cursor == "" {
			_, _ = io.WriteString(w, `{"data":{"mentions":[{"id":1}],"next_cursor":"next"}}`)
		} else {
			_, _ = io.WriteString(w, `{"data":{"mentions":[{"id":2}],"next_cursor":null}}`)
		}
	}))
	defer upstream.Close()
	c, _ := New(Config{AppURL: upstream.URL, DataURL: upstream.URL, APIKey: "key", Retries: 2, Timeout: time.Second}, slog.Default())
	got, err := c.SyncMentions(context.Background(), SyncRequest{ProjectID: "42", DateFrom: "2026-01-01", DateTo: "2026-02-15"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Windows != 2 || got.Pages != 4 || got.Count != 4 {
		t.Fatalf("result = %+v", got)
	}
	if len(calls) != 4 {
		t.Fatalf("calls = %d", len(calls))
	}
	for _, raw := range got.Mentions {
		var item map[string]int
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDoDataRejectsUnsafePath(t *testing.T) {
	c, _ := New(Config{AppURL: "https://example.com", DataURL: "https://example.com", APIKey: "key"}, slog.Default())
	if _, _, err := c.DoData(context.Background(), http.MethodGet, "/api-data/v1/../secret", nil, nil); err == nil {
		t.Fatal("expected unsafe path error")
	}
}
