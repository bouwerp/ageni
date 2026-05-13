package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// ViewImage sends an image to a vision-capable model and returns its
// interpretation. Local files are base64-encoded; remote URLs are passed by
// reference. The tool is self-contained: it opens its own OpenAI-compatible
// client using the credentials supplied at registration time.
type ViewImage struct {
	// APIKey and BaseURL configure the OpenAI-compatible endpoint to use.
	// Leave BaseURL empty for the real OpenAI API.
	APIKey  string
	BaseURL string
	// Model is the vision model to use, e.g. "gpt-4o" or "gemini-2.0-flash".
	// If empty, defaults to "gpt-4o".
	Model string
}

func (ViewImage) Name() string { return "view_image" }
func (ViewImage) Description() string {
	return `View and interpret an image. Accepts a local file path or a remote URL.
Returns a detailed description or answers a specific question about the image.
Supported formats: JPEG, PNG, GIF, WebP. Use this for screenshots, diagrams,
UI mockups, photos, charts, or any visual content that needs understanding.`
}

func (ViewImage) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Absolute or relative path to a local image file."
    },
    "url": {
      "type": "string",
      "description": "Remote URL of the image (https://...)."
    },
    "question": {
      "type": "string",
      "description": "Specific question to answer about the image. Default: describe the image in detail."
    },
    "detail": {
      "type": "string",
      "enum": ["auto", "low", "high"],
      "description": "Vision detail level. 'low' is faster and cheaper; 'high' gives more accurate analysis of dense images. Default: auto."
    }
  },
  "required": []
}`)
}

func (t ViewImage) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Path     string `json:"path"`
		URL      string `json:"url"`
		Question string `json:"question"`
		Detail   string `json:"detail"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if a.Path == "" && a.URL == "" {
		return "", fmt.Errorf("provide either 'path' (local file) or 'url' (remote image)")
	}
	if a.Path != "" && a.URL != "" {
		return "", fmt.Errorf("provide only one of 'path' or 'url', not both")
	}

	question := a.Question
	if question == "" {
		question = "Describe this image in detail. Note any text, UI elements, diagrams, charts, code, or other notable content."
	}

	detail := "auto"
	switch a.Detail {
	case "low", "high":
		detail = a.Detail
	}

	// Resolve the image reference to a data URI (local) or raw URL (remote).
	imageURL, err := t.resolveImageURL(ctx, a.Path, a.URL)
	if err != nil {
		return "", err
	}

	// Build the client.
	model := t.Model
	if model == "" {
		model = "gpt-4o"
	}
	opts := []option.RequestOption{option.WithAPIKey(t.APIKey)}
	if t.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(t.BaseURL))
	}
	client := openai.NewClient(opts...)

	// Build a vision request: user message with text prompt + image part.
	parts := []openai.ChatCompletionContentPartUnionParam{
		openai.TextContentPart(question),
		openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
			URL:    imageURL,
			Detail: detail,
		}),
	}

	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:     model,
		MaxTokens: openai.Int(2048),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(parts),
		},
	})
	if err != nil {
		return "", fmt.Errorf("vision model error: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("vision model returned no choices")
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

// resolveImageURL returns the image as a data URI for local files, or the raw
// URL string for remote images.
func (t ViewImage) resolveImageURL(ctx context.Context, path, rawURL string) (string, error) {
	if path != "" {
		return loadLocalImage(path)
	}
	// Validate the URL.
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		return "", fmt.Errorf("invalid image URL %q: %w", rawURL, err)
	}
	// For data URIs passed directly, return as-is.
	if strings.HasPrefix(rawURL, "data:") {
		return rawURL, nil
	}
	// For remote http(s) URLs we can pass the URL directly to the API rather
	// than downloading it ourselves — the provider will fetch it. However, for
	// private/local URLs that the provider can't reach we download and base64.
	if strings.HasPrefix(rawURL, "http://localhost") ||
		strings.HasPrefix(rawURL, "http://127.") ||
		strings.HasPrefix(rawURL, "http://192.168.") {
		return downloadAndEncode(ctx, rawURL)
	}
	return rawURL, nil
}

// loadLocalImage reads a local image file and returns it as a base64 data URI.
func loadLocalImage(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read image %s: %w", abs, err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("image file is empty: %s", abs)
	}
	mime := detectMIME(path, data)
	encoded := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("data:%s;base64,%s", mime, encoded), nil
}

// downloadAndEncode fetches a remote image and returns it as a base64 data URI.
func downloadAndEncode(ctx context.Context, rawURL string) (string, error) {
	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("fetch image: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024)) // 20 MB cap
	if err != nil {
		return "", fmt.Errorf("read image body: %w", err)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = detectMIME(rawURL, data)
	}
	// Strip parameters (e.g. "image/jpeg; charset=...")
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("data:%s;base64,%s", ct, encoded), nil
}

// detectMIME infers the MIME type from the file extension and/or magic bytes.
func detectMIME(name string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	// Magic byte sniff fallback.
	if len(data) >= 4 {
		switch {
		case data[0] == 0xFF && data[1] == 0xD8:
			return "image/jpeg"
		case data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G':
			return "image/png"
		case string(data[:4]) == "RIFF" && len(data) >= 12 && string(data[8:12]) == "WEBP":
			return "image/webp"
		case data[0] == 'G' && data[1] == 'I' && data[2] == 'F':
			return "image/gif"
		}
	}
	return "image/jpeg" // safe default
}
