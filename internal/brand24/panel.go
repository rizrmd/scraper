package brand24

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const projectsQuery = `query getUserProjects { getUserProjects { project { id name createdDate competitors } newMentionsCount presenceScore } }`

const mentionsQuery = `query getMentions($projectId: Int!, $dateRange: DateRangeInput!, $filters: MentionFilterInput, $page: Int, $order: Int, $limit: Int, $source: ESource) {
  getMentions(projectId: $projectId, dateRange: $dateRange, filters: $filters, page: $page, order: $order, limit: $limit, source: $source) {
    count
    results {
      id title content host { name isSocialMedia visitsMonthly } createdDate pageCategory followersCount sentiment
      author { id name url avatarUrl avatarUrlRelative } openUrl hasImage influencerScore aiImpact aiImpactPotential
      viewsCount isVisited tags { id title } avatarUrl avatarUrlRelative titleIconUrl titleIconUrlRelative emotions
      youtubeTranscriptSegments { text timestampSeconds timestampLabel url }
    }
  }
}`

type panelProject struct {
	Project struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
}
type panelMention struct {
	ID           any     `json:"id"`
	Title        *string `json:"title"`
	Content      *string `json:"content"`
	CreatedDate  string  `json:"createdDate"`
	PageCategory int     `json:"pageCategory"`
	Sentiment    any     `json:"sentiment"`
	OpenURL      string  `json:"openUrl"`
	Tags         any     `json:"tags"`
	Host         struct {
		Name          string `json:"name"`
		IsSocialMedia bool   `json:"isSocialMedia"`
		VisitsMonthly any    `json:"visitsMonthly"`
	} `json:"host"`
	Author          any `json:"author"`
	FollowersCount  any `json:"followersCount"`
	ViewsCount      any `json:"viewsCount"`
	InfluencerScore any `json:"influencerScore"`
	Emotions        any `json:"emotions"`
}

func (c *Client) graphQL(ctx context.Context, operation, query string, variables any, target any) error {
	if err := c.Login(ctx); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"operationName": operation, "variables": variables, "query": query})
	u := strings.TrimRight(c.cfg.AppURL, "/") + "/api/graphql?on=" + url.QueryEscape(operation)
	for attempt := 0; attempt < 2; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("tknb24", c.panelToken)
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		var envelope struct {
			Data   json.RawMessage `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return fmt.Errorf("decode Brand24 panel response: %w", err)
		}
		if resp.StatusCode == http.StatusUnauthorized || len(envelope.Errors) > 0 && strings.Contains(strings.ToLower(envelope.Errors[0].Message), "not logged") {
			c.mu.Lock()
			c.authenticated = false
			c.panelToken = ""
			c.mu.Unlock()
			if attempt == 0 {
				if err := c.Login(ctx); err != nil {
					return err
				}
				continue
			}
		}
		if resp.StatusCode >= 400 {
			return &UpstreamError{Status: resp.StatusCode, Body: body}
		}
		if len(envelope.Errors) > 0 {
			return errors.New("Brand24 panel: " + envelope.Errors[0].Message)
		}
		return json.Unmarshal(envelope.Data, target)
	}
	return errors.New("Brand24 panel session could not be refreshed")
}

func (c *Client) discoverProject(ctx context.Context) (panelProject, error) {
	var data struct {
		Projects []panelProject `json:"getUserProjects"`
	}
	if err := c.graphQL(ctx, "getUserProjects", projectsQuery, map[string]any{}, &data); err != nil {
		return panelProject{}, err
	}
	if len(data.Projects) == 0 {
		return panelProject{}, errors.New("Brand24 account has no projects")
	}
	return data.Projects[0], nil
}

func (c *Client) syncPanelMentions(ctx context.Context, in SyncRequest) (*SyncResult, error) {
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
	projectID := int64(0)
	if strings.TrimSpace(in.ProjectID) != "" {
		projectID, err = strconv.ParseInt(in.ProjectID, 10, 64)
		if err != nil {
			return nil, errors.New("project_id must be numeric")
		}
	} else {
		project, discoverErr := c.discoverProject(ctx)
		if discoverErr != nil {
			return nil, discoverErr
		}
		projectID = project.Project.ID
	}
	maxLimit := in.Limit
	if maxLimit <= 0 {
		maxLimit = 500
	}
	pageSize := 500
	if maxLimit < pageSize {
		pageSize = maxLimit
	}
	result := &SyncResult{ProjectID: strconv.FormatInt(projectID, 10), DateFrom: in.DateFrom, DateTo: in.DateTo, Mentions: []json.RawMessage{}}
	for start := from; !start.After(to); {
		end := start.AddDate(0, 0, 30)
		if end.After(to) {
			end = to
		}
		result.Windows++
		for page := 1; ; page++ {
			variables := map[string]any{
				"projectId": projectID,
				"dateRange": map[string]string{"from": start.Format(time.DateOnly), "to": end.Format(time.DateOnly)},
				"filters":   panelFilters(in.Category),
				"page":      page,
				"order":     0,
				"limit":     pageSize,
			}
			var data struct {
				Mentions struct {
					Count   int            `json:"count"`
					Results []panelMention `json:"results"`
				} `json:"getMentions"`
			}
			if err := c.graphQL(ctx, "getMentions", mentionsQuery, variables, &data); err != nil {
				return nil, err
			}
			result.Pages++

			// Parallel URL redirect resolution
			var wg sync.WaitGroup
			sem := make(chan struct{}, 20)
			for i := range data.Mentions.Results {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					data.Mentions.Results[idx].OpenURL = c.ResolveRedirect(ctx, data.Mentions.Results[idx].OpenURL)
					if authorMap, ok := data.Mentions.Results[idx].Author.(map[string]any); ok {
						if rawAuthorURL, ok := authorMap["url"].(string); ok && rawAuthorURL != "" {
							authorMap["url"] = c.ResolveRedirect(ctx, rawAuthorURL)
						}
					}
				}(i)
			}
			wg.Wait()

			for _, mention := range data.Mentions.Results {
				if !matchesPanelCategory(panelCategoryName(mention.PageCategory), in.Category) {
					continue
				}
				normalized := normalizePanelMention(mention)
				raw, _ := json.Marshal(normalized)
				result.Mentions = append(result.Mentions, raw)
				if len(result.Mentions) >= maxLimit {
					break
				}
			}
			if len(data.Mentions.Results) == 0 || len(result.Mentions) >= maxLimit || page*pageSize >= data.Mentions.Count {
				break
			}
		}
		if len(result.Mentions) >= maxLimit {
			break
		}
		start = end.AddDate(0, 0, 1)
	}
	result.Count = len(result.Mentions)
	return result, nil
}

func panelFilters(categories string) map[string]any {
	return map[string]any{"va": 1, "rt": panelCategoryIDs(categories), "se": []any{}, "tan": []any{}, "at": []any{}, "emi": []any{}, "vi": nil, "gr": []any{}, "sq": "", "do": "", "au": "", "lem": false, "ctr": []any{}, "nctr": false, "is": []int{0, 10}, "tp": nil, "anom": "", "lang": []any{}, "nlang": false, "aue": nil, "htg": nil, "mt": false, "mtri": nil, "cxs": []any{}, "aim": []any{}, "em": []any{}, "ol": []any{}}
}

func panelCategoryIDs(categories string) []int {
	ids := map[string]int{"twitter": 1, "instagram": 2, "blogs": 3, "blog": 3, "youtube": 4, "videos": 4, "facebook": 5, "forums": 6, "forum": 6, "news": 7, "web": 8, "podcasts": 9, "tiktok": 11}
	result := []int{}
	for _, category := range strings.Split(strings.ToLower(categories), ",") {
		if id, ok := ids[strings.TrimSpace(category)]; ok {
			result = append(result, id)
		}
	}
	return result
}

func matchesPanelCategory(category, requested string) bool {
	if requested == "" {
		return true
	}
	category = strings.ToLower(category)
	for _, item := range strings.Split(requested, ",") {
		item = strings.TrimSpace(strings.ToLower(item))
		if item == "web" && (category == "blog" || category == "forum" || category == "web") {
			return true
		}
		if category == item || strings.Contains(category, item) {
			return true
		}
	}
	return false
}

var (
	redirectCache  sync.Map
	redirectClient = &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
)

// ExtractTargetURL extracts the direct target URL from redirect parameters if present.
func ExtractTargetURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || (!strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://")) {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	host := strings.ToLower(u.Hostname())
	if !isBrand24OrRedirectHost(host) {
		return raw
	}

	q := u.Query()
	paramKeys := []string{"url", "target", "dest", "destination", "link", "to", "r", "redirect", "redirect_url", "orig", "original", "href", "u"}
	for _, key := range paramKeys {
		val := q.Get(key)
		if val == "" {
			continue
		}
		if unescaped, err := url.QueryUnescape(val); err == nil && unescaped != "" {
			val = unescaped
		}
		val = strings.TrimSpace(val)
		if strings.HasPrefix(val, "http://") || strings.HasPrefix(val, "https://") {
			return ExtractTargetURL(val)
		}
		if decoded, err := base64.StdEncoding.DecodeString(val); err == nil {
			decStr := strings.TrimSpace(string(decoded))
			if strings.HasPrefix(decStr, "http://") || strings.HasPrefix(decStr, "https://") {
				return ExtractTargetURL(decStr)
			}
		}
		if decoded, err := base64.URLEncoding.DecodeString(val); err == nil {
			decStr := strings.TrimSpace(string(decoded))
			if strings.HasPrefix(decStr, "http://") || strings.HasPrefix(decStr, "https://") {
				return ExtractTargetURL(decStr)
			}
		}
	}
	return raw
}

func isBrand24OrRedirectHost(host string) bool {
	return strings.Contains(host, "brand24") || host == "b24.io" || host == "b24.am"
}

// ResolveRedirect extracts the direct target URL or follows HTTP redirects if it is a Brand24 redirect link.
func (c *Client) ResolveRedirect(ctx context.Context, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || (!strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://")) {
		return raw
	}
	if cached, ok := redirectCache.Load(raw); ok {
		if s, ok := cached.(string); ok && s != "" {
			return s
		}
	}

	extracted := ExtractTargetURL(raw)
	if extracted != raw {
		if u, err := url.Parse(extracted); err == nil && !isBrand24OrRedirectHost(strings.ToLower(u.Hostname())) {
			redirectCache.Store(raw, extracted)
			return extracted
		}
	}

	u, err := url.Parse(raw)
	if err != nil || !isBrand24OrRedirectHost(strings.ToLower(u.Hostname())) {
		redirectCache.Store(raw, raw)
		return raw
	}

	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, raw, nil)
	if err != nil {
		return raw
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	client := c.http
	if client == nil {
		client = redirectClient
	}
	resp, err := client.Do(req)
	if err != nil {
		redirectCache.Store(raw, extracted)
		return extracted
	}
	defer resp.Body.Close()

	finalURL := resp.Request.URL.String()
	if finalURL != "" && !isBrand24OrRedirectHost(strings.ToLower(resp.Request.URL.Hostname())) {
		redirectCache.Store(raw, finalURL)
		return finalURL
	}

	if loc := resp.Header.Get("Location"); loc != "" {
		if locURL, err := url.Parse(loc); err == nil && !isBrand24OrRedirectHost(strings.ToLower(locURL.Hostname())) {
			redirectCache.Store(raw, loc)
			return loc
		}
	}

	redirectCache.Store(raw, extracted)
	return extracted
}

// ResolveTargetURL is a standalone function for redirect resolution using default client.
func ResolveTargetURL(ctx context.Context, raw string) string {
	c := &Client{http: redirectClient}
	return c.ResolveRedirect(ctx, raw)
}

func normalizePanelMention(m panelMention) map[string]any {
	resolvedURL := ExtractTargetURL(m.OpenURL)
	date, timePart := m.CreatedDate, ""
	parsed, err := time.Parse(time.RFC3339, m.CreatedDate)
	if err != nil {
		parsed, err = time.Parse("2006-01-02T15:04:05-0700", m.CreatedDate)
	}
	if err == nil {
		date = parsed.Format(time.DateOnly)
		timePart = parsed.Format("15:04:05")
	}
	author := m.Author
	if authorMap, ok := author.(map[string]any); ok {
		if rawAuthorURL, ok := authorMap["url"].(string); ok && rawAuthorURL != "" {
			authorMap["url"] = ExtractTargetURL(rawAuthorURL)
		}
	}
	return map[string]any{
		"id":              m.ID,
		"date":            date,
		"time":            timePart,
		"title":           m.Title,
		"content":         m.Content,
		"source":          resolvedURL,
		"url":             resolvedURL,
		"open_url":        resolvedURL,
		"openUrl":         resolvedURL,
		"host":            m.Host.Name,
		"category":        panelCategoryName(m.PageCategory),
		"sentiment":       panelSentimentName(m.Sentiment),
		"tags":            m.Tags,
		"author":          author,
		"followers_count": m.FollowersCount,
		"views_count":     m.ViewsCount,
		"influencer_score": m.InfluencerScore,
		"emotions":        m.Emotions,
	}
}

func panelCategoryName(id int) string {
	return map[int]string{1: "twitter", 2: "instagram", 3: "blogs", 4: "youtube", 5: "facebook", 6: "forums", 7: "news", 8: "web", 9: "podcasts", 11: "tiktok"}[id]
}

func panelSentimentName(value any) string {
	values := map[int]string{0: "neutral", 1: "positive", 2: "negative"}
	switch typed := value.(type) {
	case string:
		return strings.ToLower(typed)
	case float64:
		return values[int(typed)]
	case int:
		return values[typed]
	}
	return "neutral"
}
