package brand24

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	AppURL, DataURL, Email, Password, APIKey, AccountID string
	Retries                                             int
	Timeout                                             time.Duration
}
type Client struct {
	cfg             Config
	http            *http.Client
	logger          *slog.Logger
	mu              sync.Mutex
	authenticated   bool
	authenticatedAt time.Time
}
type UpstreamError struct {
	Status int
	Body   []byte
}

func (e *UpstreamError) Error() string { return fmt.Sprintf("Brand24 returned HTTP %d", e.Status) }

func New(cfg Config, logger *slog.Logger) (*Client, error) {
	if _, err := url.ParseRequestURI(cfg.AppURL); err != nil {
		return nil, fmt.Errorf("BRAND24_APP_URL: %w", err)
	}
	if _, err := url.ParseRequestURI(cfg.DataURL); err != nil {
		return nil, fmt.Errorf("BRAND24_DATA_URL: %w", err)
	}
	if cfg.Retries < 1 {
		cfg.Retries = 3
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 45 * time.Second
	}
	jar, _ := cookiejar.New(nil)
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout, Jar: jar}, logger: logger}, nil
}

func (c *Client) DataConfigured() bool { return c.cfg.APIKey != "" }
func (c *Client) AccountID() string    { return c.cfg.AccountID }

func (c *Client) Login(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.authenticated && time.Since(c.authenticatedAt) < 30*time.Minute {
		return nil
	}
	if c.cfg.Email == "" || c.cfg.Password == "" {
		return errors.New("BRAND24_EMAIL and BRAND24_PASSWORD are required")
	}
	form := url.Values{"login": {c.cfg.Email}, "password": {c.cfg.Password}, "remember_me": {"1"}, "backurl": {"/panel"}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.AppURL, "/")+"/user/login-submit/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var result struct {
		Result  int    `json:"result"`
		Message string `json:"message"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &result) != nil || result.Result != 1 {
		return fmt.Errorf("Brand24 login rejected (HTTP %d, result %d)", resp.StatusCode, result.Result)
	}
	c.authenticated, c.authenticatedAt = true, time.Now()
	return nil
}

func (c *Client) DoData(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, int, error) {
	if c.cfg.APIKey == "" {
		return nil, 0, errors.New("BRAND24_API_KEY is not configured; enable Brand24 Data API for this account")
	}
	if !strings.HasPrefix(path, "/api-data/v1/") || strings.Contains(path, "..") {
		return nil, 0, errors.New("invalid Brand24 API path")
	}
	u := strings.TrimRight(c.cfg.DataURL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var last error
	for attempt := 0; attempt < c.cfg.Retries; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
		req.Header.Set("X-Api-Key", c.cfg.APIKey)
		req.Header.Set("Accept", "application/json")
		if len(body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err == nil {
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
			resp.Body.Close()
			if readErr != nil {
				last = readErr
			} else if resp.StatusCode < 500 && resp.StatusCode != 429 {
				if resp.StatusCode >= 400 {
					return data, resp.StatusCode, &UpstreamError{resp.StatusCode, data}
				}
				return data, resp.StatusCode, nil
			} else {
				last = &UpstreamError{resp.StatusCode, data}
			}
		} else {
			last = err
		}
		if attempt+1 < c.cfg.Retries {
			delay := time.Duration(1<<attempt)*300*time.Millisecond + time.Duration(rand.IntN(250))*time.Millisecond
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return nil, 0, fmt.Errorf("Brand24 request failed after %d attempts: %w", c.cfg.Retries, last)
}

type SyncRequest struct {
	ProjectID string `json:"project_id"`
	DateFrom  string `json:"date_from"`
	DateTo    string `json:"date_to"`
	Sentiment string `json:"sentiment,omitempty"`
	Category  string `json:"category,omitempty"`
}
type SyncResult struct {
	ProjectID string            `json:"project_id"`
	DateFrom  string            `json:"date_from"`
	DateTo    string            `json:"date_to"`
	Count     int               `json:"count"`
	Windows   int               `json:"windows"`
	Pages     int               `json:"pages"`
	Mentions  []json.RawMessage `json:"mentions"`
}

func (c *Client) SyncMentions(ctx context.Context, in SyncRequest) (*SyncResult, error) {
	if _, err := strconv.ParseInt(in.ProjectID, 10, 64); err != nil {
		return nil, errors.New("project_id must be numeric")
	}
	from, err := time.Parse(time.DateOnly, in.DateFrom)
	if err != nil {
		return nil, errors.New("date_from must be YYYY-MM-DD")
	}
	to, err := time.Parse(time.DateOnly, in.DateTo)
	if err != nil {
		return nil, errors.New("date_to must be YYYY-MM-DD")
	}
	if to.Before(from) {
		return nil, errors.New("date_to must not precede date_from")
	}
	result := &SyncResult{ProjectID: in.ProjectID, DateFrom: in.DateFrom, DateTo: in.DateTo, Mentions: []json.RawMessage{}}
	for start := from; !start.After(to); {
		end := start.AddDate(0, 0, 30)
		if end.After(to) {
			end = to
		}
		result.Windows++
		cursor := ""
		for {
			q := url.Values{"date_from": {start.Format(time.DateOnly)}, "date_to": {end.Format(time.DateOnly)}, "limit": {"500"}}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			if in.Sentiment != "" {
				q.Set("sentiment", in.Sentiment)
			}
			if in.Category != "" {
				q.Set("category", in.Category)
			}
			data, _, err := c.DoData(ctx, http.MethodGet, "/api-data/v1/project/"+in.ProjectID+"/mentions", q, nil)
			if err != nil {
				return nil, err
			}
			result.Pages++
			var envelope map[string]json.RawMessage
			if err = json.Unmarshal(data, &envelope); err != nil {
				return nil, fmt.Errorf("decode mentions: %w", err)
			}
			items, cursorNext, err := extractPage(envelope)
			if err != nil {
				return nil, err
			}
			result.Mentions = append(result.Mentions, items...)
			if cursorNext == "" || cursorNext == cursor {
				break
			}
			cursor = cursorNext
		}
		start = end.AddDate(0, 0, 1)
	}
	result.Count = len(result.Mentions)
	return result, nil
}

func extractPage(envelope map[string]json.RawMessage) ([]json.RawMessage, string, error) {
	root := envelope["data"]
	if len(root) == 0 {
		root = envelope["message"]
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(root, &obj); err != nil {
		return nil, "", errors.New("unexpected mentions response")
	}
	var items []json.RawMessage
	for _, k := range []string{"mentions", "results", "items"} {
		if raw := obj[k]; len(raw) > 0 && json.Unmarshal(raw, &items) == nil {
			break
		}
	}
	var cursor string
	for _, k := range []string{"next_cursor", "nextCursor", "cursor"} {
		if raw := obj[k]; len(raw) > 0 {
			_ = json.Unmarshal(raw, &cursor)
			if cursor != "" {
				break
			}
		}
	}
	return items, cursor, nil
}
