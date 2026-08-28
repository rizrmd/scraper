package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rizrmd/scraper/internal/brand24"
)

func TestHealthAndAuth(t *testing.T) {
	c, _ := brand24.New(brand24.Config{AppURL: "https://example.com", DataURL: "https://example.com"}, slog.Default())
	h := New(c, "internal-token", slog.Default()).Handler()
	for _, tc := range []struct {
		path string
		want int
	}{{"/healthz", 200}, {"/readyz", 503}, {"/v1/brand24/session", 401}} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s status=%d want=%d", tc.path, w.Code, tc.want)
		}
	}
}
