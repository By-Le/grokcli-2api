package minimax

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/hm2899/grokcli-2api/internal/upstream/grok"
)

type Client struct {
	OpenAIBaseURL    string
	AnthropicBaseURL string
	HTTP             *http.Client
}

func (c *Client) Headers(token, _ string, _ ...string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(token),
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	}
}

func (c *Client) ModelsURL() string {
	baseURL := strings.TrimRight(strings.TrimSpace(c.OpenAIBaseURL), "/")
	if baseURL == "" {
		defaults, _ := EndpointsForRegion(RegionGlobalEN)
		baseURL = defaults.OpenAIBaseURL
	}
	return baseURL + "/models"
}

func (c *Client) Open(ctx context.Context, account grok.Account, model string, body map[string]any) (*http.Response, error) {
	payload := cloneMap(body)
	payload["model"] = ResolveModel(model, DefaultModel)
	payload["stream"] = true
	if _, ok := payload["reasoning_split"]; !ok {
		payload["reasoning_split"] = true
	}
	delete(payload, "prompt_cache_key")
	delete(payload, "prompt_cache_retention")

	if payload["max_completion_tokens"] == nil && payload["max_tokens"] != nil {
		payload["max_completion_tokens"] = payload["max_tokens"]
	}
	delete(payload, "max_tokens")
	normalizeThinking(payload)

	streamOptions, _ := payload["stream_options"].(map[string]any)
	if streamOptions == nil {
		streamOptions = map[string]any{}
	}
	streamOptions["include_usage"] = true
	payload["stream_options"] = streamOptions

	baseURL := strings.TrimRight(strings.TrimSpace(c.OpenAIBaseURL), "/")
	if baseURL == "" {
		defaults, _ := EndpointsForRegion(RegionGlobalEN)
		baseURL = defaults.OpenAIBaseURL
	}
	return c.postJSON(ctx, account.Token, baseURL+"/chat/completions", payload, map[string]string{
		"Accept": "text/event-stream",
	})
}

func (c *Client) OpenMessages(ctx context.Context, account grok.Account, model string, body map[string]any, headers http.Header) (*http.Response, error) {
	payload := cloneMap(body)
	payload["model"] = ResolveModel(model, DefaultModel)

	baseURL := strings.TrimRight(strings.TrimSpace(c.AnthropicBaseURL), "/")
	if baseURL == "" {
		defaults, _ := EndpointsForRegion(RegionGlobalEN)
		baseURL = defaults.AnthropicBaseURL
	}
	extraHeaders := map[string]string{
		"Accept":            "application/json",
		"anthropic-version": strings.TrimSpace(headers.Get("anthropic-version")),
		"anthropic-beta":    strings.TrimSpace(headers.Get("anthropic-beta")),
	}
	if extraHeaders["anthropic-version"] == "" {
		extraHeaders["anthropic-version"] = "2023-06-01"
	}
	if stream, _ := payload["stream"].(bool); stream {
		extraHeaders["Accept"] = "text/event-stream"
	}
	return c.postJSON(ctx, account.Token, baseURL+"/v1/messages", payload, extraHeaders)
}

func (c *Client) postJSON(ctx context.Context, token, url string, payload map[string]any, headers map[string]string) (*http.Response, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		if strings.TrimSpace(value) != "" {
			request.Header.Set(name, value)
		}
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return response, nil
	}
	defer response.Body.Close()
	errBody, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	return nil, &grok.UpstreamError{
		Status:     response.StatusCode,
		Body:       string(errBody),
		RetryAfter: response.Header.Get("Retry-After"),
	}
}

func normalizeThinking(payload map[string]any) {
	if payload["thinking"] != nil {
		delete(payload, "reasoning_effort")
		return
	}
	effort, _ := payload["reasoning_effort"].(string)
	effort = strings.ToLower(strings.TrimSpace(effort))
	delete(payload, "reasoning_effort")
	if effort == "" {
		return
	}
	thinkingType := "adaptive"
	switch effort {
	case "none", "off", "disabled":
		thinkingType = "disabled"
	}
	payload["thinking"] = map[string]any{"type": thinkingType}
}

func cloneMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
