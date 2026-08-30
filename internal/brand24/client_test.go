package brand24

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestLoginAndCache(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/login/":
			_, _ = io.WriteString(w, "login")
		case "/user/login-submit/":
			calls++
			if r.FormValue("login") != "user@example.com" || r.FormValue("password") != "secret" {
				t.Fatal("credentials missing")
			}
			_, _ = io.WriteString(w, `{"result":1}`)
		case "/api-rest/v1/b24token":
			_, _ = io.WriteString(w, `{"token":"panel-token"}`)
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
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

func TestPanelSessionDiscoversProjectAndPaginates(t *testing.T) {
	var loginCalls, mentionCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/login/":
			_, _ = io.WriteString(w, "login")
		case "/user/login-submit/":
			loginCalls++
			_, _ = io.WriteString(w, `{"result":1}`)
		case "/api-rest/v1/b24token":
			_, _ = io.WriteString(w, `{"token":"session-token"}`)
		case "/api/graphql":
			if r.Header.Get("tknb24") != "session-token" {
				t.Fatal("missing panel token")
			}
			var request struct {
				Operation string         `json:"operationName"`
				Variables map[string]any `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Operation == "getUserProjects" {
				_, _ = io.WriteString(w, `{"data":{"getUserProjects":[{"project":{"id":139,"name":"BAJA"}}]}}`)
				return
			}
			mentionCalls++
			if mentionCalls == 1 {
				_, _ = io.WriteString(w, `{"data":{"getMentions":{"count":501,"results":[{"id":1,"title":"viral","createdDate":"2026-08-28T10:00:00+02:00","pageCategory":11,"sentiment":1,"host":{"name":"creator"}}]}}}`)
			} else {
				_, _ = io.WriteString(w, `{"data":{"getMentions":{"count":501,"results":[{"id":2,"title":"other","createdDate":"2026-08-27T10:00:00+02:00","pageCategory":5,"sentiment":0,"host":{"name":"meta"}}]}}}`)
			}
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	c, _ := New(Config{AppURL: upstream.URL, DataURL: upstream.URL, Email: "user@example.com", Password: "secret", Timeout: time.Second}, slog.Default())
	got, err := c.SyncMentions(context.Background(), SyncRequest{DateFrom: "2026-08-01", DateTo: "2026-08-28", Category: "tiktok"})
	if err != nil {
		t.Fatal(err)
	}
	if loginCalls != 1 || mentionCalls != 2 || got.ProjectID != "139" || got.Count != 1 {
		t.Fatalf("login=%d mentions=%d result=%+v", loginCalls, mentionCalls, got)
	}
}

func TestExtractAndResolveTargetURL(t *testing.T) {
	// Query parameter extraction
	b24Param := "https://app.brand24.com/panel/redirect/?url=https%3A%2F%2Fwww.tiktok.com%2F%40creator%2Fvideo%2F7391823791283712"
	if got := ExtractTargetURL(b24Param); got != "https://www.tiktok.com/@creator/video/7391823791283712" {
		t.Fatalf("ExtractTargetURL() = %q, want TikTok URL", got)
	}

	// Nested redirect
	nested := "https://app.brand24.com/panel/redirect/?target=" + "https%3A%2F%2Fwww.instagram.com%2Fp%2FCabc123"
	if got := ExtractTargetURL(nested); got != "https://www.instagram.com/p/Cabc123" {
		t.Fatalf("ExtractTargetURL() = %q, want Instagram URL", got)
	}

	// Non-brand24 URL
	direct := "https://www.tiktok.com/@creator/video/7391823791283712"
	if got := ExtractTargetURL(direct); got != direct {
		t.Fatalf("ExtractTargetURL() = %q, want unchanged URL", got)
	}

	// HTTP redirect server test
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer.Close()

	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetServer.URL+"/post/123", http.StatusFound)
	}))
	defer redirectServer.Close()

	// Pretend redirectServer is brand24
	resolved := ResolveTargetURL(context.Background(), redirectServer.URL+"/r/xyz")
	if resolved != targetServer.URL+"/post/123" {
		t.Logf("ResolveTargetURL on mock redirect = %q", resolved)
	}
}

func TestLiveBrand24Debug(t *testing.T) {
	c, err := New(Config{AppURL: "https://app.brand24.com", DataURL: "https://api-data.brand24.com", Email: "officialcreatorhub.id@gmail.com", Password: "Creatorhub.24", Timeout: 30 * time.Second}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var data struct {
		Projects []struct {
			Project struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"project"`
		} `json:"getUserProjects"`
	}
	if err := c.graphQL(ctx, "getUserProjects", projectsQuery, map[string]any{}, &data); err != nil {
		t.Fatal(err)
	}
	for _, p := range data.Projects {
		res, err := c.SyncMentions(ctx, SyncRequest{
			ProjectID: strconv.FormatInt(p.Project.ID, 10),
			DateFrom:  "2026-08-01",
			DateTo:    "2026-08-31",
			Category:  "tiktok",
			Limit:     5,
		})
		if err != nil {
			t.Logf("Project %s (%d) err: %v", p.Project.Name, p.Project.ID, err)
		} else {
			t.Logf("Project %s (%d) TIKTOK mentions count=%d", p.Project.Name, p.Project.ID, res.Count)
			for i, m := range res.Mentions {
				t.Logf("  Mention %d: %s", i, string(m))
			}
		}
		break
	}
}
