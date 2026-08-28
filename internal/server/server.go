package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rizrmd/scraper/internal/brand24"
)

type Server struct {
	client *brand24.Client
	token  string
	logger *slog.Logger
}

func New(client *brand24.Client, token string, logger *slog.Logger) *Server {
	return &Server{client: client, token: token, logger: logger}
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /v1/brand24/session", s.auth(s.session))
	mux.HandleFunc("POST /v1/brand24/sync/mentions", s.auth(s.sync))
	mux.HandleFunc("/v1/brand24/data/", s.auth(s.proxy))
	return s.recover(mux)
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	write(w, 200, map[string]any{"status": "ok", "service": "brand24-scraper"})
}
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if !s.client.DataConfigured() {
		write(w, 503, map[string]any{"status": "not_ready", "reason": "BRAND24_API_KEY is not configured"})
		return
	}
	write(w, 200, map[string]any{"status": "ready"})
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextTimeout(r, 30*time.Second)
	defer cancel()
	if err := s.client.Login(ctx); err != nil {
		write(w, 502, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	write(w, 200, map[string]any{"status": "authenticated", "data_api_configured": s.client.DataConfigured(), "account_id_configured": s.client.AccountID() != ""})
}
func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	var in brand24.SyncRequest
	if err := decode(r, &in); err != nil {
		write(w, 400, map[string]any{"error": err.Error()})
		return
	}
	result, err := s.client.SyncMentions(r.Context(), in)
	if err != nil {
		s.failure(w, err)
		return
	}
	write(w, 200, result)
}
func (s *Server) proxy(w http.ResponseWriter, r *http.Request) {
	path := "/api-data/v1/" + strings.TrimPrefix(r.URL.Path, "/v1/brand24/data/")
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		write(w, 400, map[string]any{"error": "invalid body"})
		return
	}
	data, status, err := s.client.DoData(r.Context(), r.Method, path, cloneQuery(r.URL.Query()), body)
	if err != nil && status == 0 {
		s.failure(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && r.Header.Get("Authorization") != "Bearer "+s.token {
			write(w, 401, map[string]any{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() {
			if v := recover(); v != nil {
				s.logger.Error("panic", "value", v)
				write(w, 500, map[string]any{"error": "internal error"})
			}
			s.logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(w, r)
	})
}
func (s *Server) failure(w http.ResponseWriter, err error) {
	var upstream *brand24.UpstreamError
	if errors.As(err, &upstream) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(upstream.Status)
		_, _ = w.Write(upstream.Body)
		return
	}
	write(w, 503, map[string]any{"error": err.Error()})
}
func decode(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func cloneQuery(in url.Values) url.Values {
	out := url.Values{}
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}
