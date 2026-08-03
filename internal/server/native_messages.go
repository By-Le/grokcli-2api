package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hm2899/grokcli-2api/internal/auth"
	"github.com/hm2899/grokcli-2api/internal/pool"
	"github.com/hm2899/grokcli-2api/internal/upstream/grok"
)

type nativeMessagesUpstream interface {
	OpenMessages(context.Context, grok.Account, string, map[string]any, http.Header) (*http.Response, error)
}

func serveNativeMessages(w http.ResponseWriter, r *http.Request, options Options, apiKey *auth.APIKeyRecord, raw map[string]any, model string, stream bool) bool {
	client, ok := upstreamClient(options).(nativeMessagesUpstream)
	if !ok {
		return false
	}

	started := time.Now()
	candidates, err := listCandidates(r.Context(), options)
	if err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, err.Error(), "api_error")
		return true
	}
	picked, err := pool.Pick(candidates, model, resolvePickMode(options), time.Now())
	if err != nil {
		writeAnthropicProxyError(w, err)
		return true
	}

	response, err := client.OpenMessages(r.Context(), picked.UpstreamAccount(), model, raw, r.Header)
	if err != nil {
		recordAnthropicUsage(r, options, apiKey, picked.ID, model, stream, false, http.StatusBadGateway, started, nil, err, 0, raw)
		reportChatPool(r, options, picked.ID, false, err, http.StatusBadGateway, model)
		writeAnthropicProxyError(w, err)
		return true
	}
	defer response.Body.Close()
	copyNativeMessagesHeaders(w.Header(), response.Header, stream)

	if stream {
		w.WriteHeader(response.StatusCode)
		usage, firstTokenMS, relayErr := relayNativeMessagesStream(w, response.Body)
		requestOK := relayErr == nil || errors.Is(relayErr, r.Context().Err())
		status := response.StatusCode
		if !requestOK {
			status = http.StatusBadGateway
		}
		recordAnthropicUsage(r, options, apiKey, picked.ID, model, true, requestOK, status, started, usage, relayErr, firstTokenMS, raw)
		reportChatPool(r, options, picked.ID, requestOK, relayErr, status, model)
		return true
	}

	payload, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		recordAnthropicUsage(r, options, apiKey, picked.ID, model, false, false, http.StatusBadGateway, started, nil, err, 0, raw)
		writeAnthropicProxyError(w, err)
		return true
	}
	var decoded map[string]any
	_ = json.Unmarshal(payload, &decoded)
	usage, _ := decoded["usage"].(map[string]any)
	if returnedModel, _ := decoded["model"].(string); strings.TrimSpace(returnedModel) != "" {
		model = strings.TrimSpace(returnedModel)
	}
	w.WriteHeader(response.StatusCode)
	_, writeErr := w.Write(payload)
	requestOK := writeErr == nil
	recordAnthropicUsage(r, options, apiKey, picked.ID, model, false, requestOK, response.StatusCode, started, usage, writeErr, 0, raw)
	reportChatPool(r, options, picked.ID, requestOK, writeErr, response.StatusCode, model)
	return true
}

func copyNativeMessagesHeaders(dst, src http.Header, stream bool) {
	contentType := strings.TrimSpace(src.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
		if stream {
			contentType = "text/event-stream"
		}
	}
	dst.Set("Content-Type", contentType)
	for _, name := range []string{"request-id", "x-request-id"} {
		if value := strings.TrimSpace(src.Get(name)); value != "" {
			dst.Set(name, value)
		}
	}
}

func relayNativeMessagesStream(w http.ResponseWriter, body io.Reader) (map[string]any, int, error) {
	reader := bufio.NewReader(body)
	flusher, _ := w.(http.Flusher)
	usage := map[string]any{}
	started := time.Now()
	firstTokenMS := 0

	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			if data, ok := nativeMessagesData(line); ok {
				if mergeNativeMessagesEvent(usage, data) && firstTokenMS == 0 {
					firstTokenMS = int(time.Since(started).Milliseconds())
					if firstTokenMS <= 0 {
						firstTokenMS = 1
					}
				}
			}
			if _, writeErr := io.WriteString(w, line); writeErr != nil {
				return finalizeNativeMessagesUsage(usage), firstTokenMS, writeErr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			return finalizeNativeMessagesUsage(usage), firstTokenMS, nil
		}
		return finalizeNativeMessagesUsage(usage), firstTokenMS, err
	}
}

func nativeMessagesData(line string) ([]byte, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return nil, false
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" {
		return nil, false
	}
	return []byte(data), true
}

func mergeNativeMessagesEvent(usage map[string]any, data []byte) bool {
	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil {
		return false
	}
	if message, _ := event["message"].(map[string]any); message != nil {
		if values, _ := message["usage"].(map[string]any); values != nil {
			mergeNativeMessagesUsage(usage, values)
		}
	}
	if values, _ := event["usage"].(map[string]any); values != nil {
		mergeNativeMessagesUsage(usage, values)
	}
	eventType, _ := event["type"].(string)
	if eventType == "content_block_start" {
		return true
	}
	if eventType != "content_block_delta" {
		return false
	}
	delta, _ := event["delta"].(map[string]any)
	deltaType, _ := delta["type"].(string)
	switch deltaType {
	case "text_delta", "thinking_delta", "input_json_delta":
		return true
	default:
		return false
	}
}

func mergeNativeMessagesUsage(dst, src map[string]any) {
	for key, value := range src {
		if value != nil {
			dst[key] = value
		}
	}
}

func finalizeNativeMessagesUsage(usage map[string]any) map[string]any {
	if len(usage) == 0 {
		return nil
	}
	if anyToInt64(usage["total_tokens"]) == 0 {
		usage["total_tokens"] = anyToInt64(usage["input_tokens"]) + anyToInt64(usage["output_tokens"])
	}
	return usage
}
