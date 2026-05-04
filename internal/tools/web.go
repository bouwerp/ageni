package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
)

// WebFetch retrieves a URL and returns the response body, converting HTML to
// markdown for token efficiency.
type WebFetch struct{}

func (WebFetch) Name() string { return "web_fetch" }
func (WebFetch) Description() string {
	return `Fetch a URL and return its content. HTML pages are converted to markdown automatically; JSON, plaintext, and markdown are returned as-is. Useful for reading docs, READMEs, blog posts, RFCs. Truncates to ~30KB.`
}
func (WebFetch) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "url":{"type":"string","description":"Full URL with scheme."},
  "max_bytes":{"type":"integer","description":"Truncate response to this many bytes. Default 30000, max 200000."}
},
"required":["url"]
}`)
}
func (WebFetch) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		URL      string `json:"url"`
		MaxBytes int    `json:"max_bytes"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.URL == "" {
		return "", errors.New("url is required")
	}
	if p.MaxBytes <= 0 {
		p.MaxBytes = 30000
	}
	if p.MaxBytes > 200000 {
		p.MaxBytes = 200000
	}

	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ageni-fetch/1.0 (+https://github.com/bouwerp/ageni)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json,text/plain,*/*")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, int64(p.MaxBytes)*3))
	if err != nil {
		return "", err
	}
	body := string(bodyBytes)
	ctype := resp.Header.Get("Content-Type")

	header := fmt.Sprintf("[%d %s — %s]\n", resp.StatusCode, resp.Status, ctype)

	if strings.Contains(ctype, "text/html") || (ctype == "" && strings.Contains(body, "<html")) {
		md, err := htmltomarkdown.ConvertString(body)
		if err == nil {
			body = md
		}
	}

	if len(body) > p.MaxBytes {
		body = body[:p.MaxBytes] + fmt.Sprintf("\n[truncated to %d bytes]", p.MaxBytes)
	}
	return header + body, nil
}

// compactSnippet collapses whitespace and truncates a snippet for inclusion
// in tool output.
func compactSnippet(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}

// WebSearch queries Tavily and returns ranked results with snippets.
type WebSearch struct{}

func (WebSearch) Name() string { return "web_search" }
func (WebSearch) Description() string {
	return `Search the web via Tavily. Returns ranked results (title, URL, snippet) tuned for LLM consumption. Requires TAVILY_API_KEY.`
}
func (WebSearch) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "query":{"type":"string"},
  "max_results":{"type":"integer","description":"Default 5, max 10."},
  "include_domains":{"type":"array","items":{"type":"string"},"description":"Whitelist domains."},
  "exclude_domains":{"type":"array","items":{"type":"string"},"description":"Blacklist domains."}
},
"required":["query"]
}`)
}
func (WebSearch) Call(ctx context.Context, args json.RawMessage) (string, error) {
	apiKey := os.Getenv("TAVILY_API_KEY")
	if apiKey == "" {
		return "", errors.New("TAVILY_API_KEY not set — get one at https://tavily.com (1k free requests/month) and add to ~/.ageni/.env")
	}
	var p struct {
		Query          string   `json:"query"`
		MaxResults     int      `json:"max_results"`
		IncludeDomains []string `json:"include_domains"`
		ExcludeDomains []string `json:"exclude_domains"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Query == "" {
		return "", errors.New("query is required")
	}
	if p.MaxResults <= 0 {
		p.MaxResults = 5
	}
	if p.MaxResults > 10 {
		p.MaxResults = 10
	}

	reqBody := map[string]any{
		"query":        p.Query,
		"max_results":  p.MaxResults,
		"search_depth": "basic",
	}
	if len(p.IncludeDomains) > 0 {
		reqBody["include_domains"] = p.IncludeDomains
	}
	if len(p.ExcludeDomains) > 0 {
		reqBody["exclude_domains"] = p.ExcludeDomains
	}
	bodyJSON, _ := json.Marshal(reqBody)

	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(bodyJSON))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("tavily returned %d: %s", resp.StatusCode, string(body))
	}

	var out struct {
		Answer  string `json:"answer"`
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}

	var sb strings.Builder
	if out.Answer != "" {
		sb.WriteString("Summary: " + out.Answer + "\n\n")
	}
	for i, r := range out.Results {
		sb.WriteString(fmt.Sprintf("%d. %s\n   %s\n   %s\n\n", i+1, r.Title, r.URL, compactSnippet(r.Content)))
	}
	if sb.Len() == 0 {
		return "(no results)", nil
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}
