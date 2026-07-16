package postgres

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type usageTotals struct {
	Requests         int64 `json:"requests"`
	Success          int64 `json:"success"`
	Fail             int64 `json:"fail"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type UsageRecord struct {
	RequestID           string
	Implementation      string
	OwnershipEpoch      int64
	APIKeyID            string
	AccountID           string
	Model               string
	Protocol            string
	Path                string
	Stream              *bool
	OK                  bool
	PromptTokens        int64
	CompletionTokens    int64
	TotalTokens         int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	ReasoningTokens     int64
	ClientIP            string
	UserAgent           string
	StatusCode          *int
	LatencyMS           *int
	TTFTMS              *int
	Error               string
	Detail              map[string]any
}

func (u usageTotals) mapWithRate() map[string]any {
	return map[string]any{
		"requests":          u.Requests,
		"success":           u.Success,
		"fail":              u.Fail,
		"prompt_tokens":     u.PromptTokens,
		"completion_tokens": u.CompletionTokens,
		"total_tokens":      u.TotalTokens,
		"success_rate":      successRate(u.Success, u.Requests),
	}
}

func (c *Connector) RecordUsage(ctx context.Context, rec UsageRecord) (int64, bool, error) {
	rec = normalizeUsageRecord(rec)
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)

	if rec.RequestID != "" {
		var existing *int64
		err := tx.QueryRow(ctx, `
			INSERT INTO request_usage_idempotency (request_id, implementation)
			VALUES ($1, $2)
			ON CONFLICT (request_id) DO NOTHING
			RETURNING usage_event_id`, rec.RequestID, rec.Implementation).Scan(&existing)
		if err != nil {
			if err == pgx.ErrNoRows {
				var eventID int64
				err = tx.QueryRow(ctx, "SELECT usage_event_id FROM request_usage_idempotency WHERE request_id = $1", rec.RequestID).Scan(&eventID)
				if err == nil {
					if commitErr := tx.Commit(ctx); commitErr != nil {
						return 0, false, commitErr
					}
					return eventID, false, nil
				}
			}
			return 0, false, err
		}
	}

	detail, err := json.Marshal(rec.Detail)
	if err != nil {
		return 0, false, err
	}
	var eventID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO usage_events (
			api_key_id, account_id, model, protocol, path, stream, ok,
			prompt_tokens, completion_tokens, total_tokens, cache_read_tokens,
			cache_creation_tokens, reasoning_tokens, client_ip, user_agent,
			status_code, latency_ms, ttft_ms, error, detail,
			request_id, implementation, ownership_epoch
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11,
			$12, $13, $14, $15,
			$16, $17, $18, $19, $20,
			$21, $22, $23
		)
		RETURNING id`, nilIfEmpty(rec.APIKeyID), nilIfEmpty(rec.AccountID), nilIfEmpty(rec.Model), nilIfEmpty(rec.Protocol), nilIfEmpty(rec.Path), rec.Stream, rec.OK,
		rec.PromptTokens, rec.CompletionTokens, rec.TotalTokens, rec.CacheReadTokens,
		rec.CacheCreationTokens, rec.ReasoningTokens, nilIfEmpty(rec.ClientIP), nilIfEmpty(rec.UserAgent),
		rec.StatusCode, rec.LatencyMS, rec.TTFTMS, nilIfEmpty(rec.Error), detail,
		nilIfEmpty(rec.RequestID), rec.Implementation, epochOrNil(rec.OwnershipEpoch)).Scan(&eventID)
	if err != nil {
		return 0, false, err
	}

	if rec.RequestID != "" {
		if _, err := tx.Exec(ctx, "UPDATE request_usage_idempotency SET usage_event_id = $2 WHERE request_id = $1", rec.RequestID, eventID); err != nil {
			return 0, false, err
		}
	}
	if err := upsertUsageDaily(ctx, tx, rec); err != nil {
		return 0, false, err
	}
	if rec.OK && (rec.PromptTokens != 0 || rec.CompletionTokens != 0 || rec.TotalTokens != 0) {
		if rec.APIKeyID != "" && rec.APIKeyID != "env" {
			if _, err := tx.Exec(ctx, `
				UPDATE api_keys
				SET prompt_tokens_total = COALESCE(prompt_tokens_total, 0) + $1,
				    completion_tokens_total = COALESCE(completion_tokens_total, 0) + $2,
				    total_tokens_total = COALESCE(total_tokens_total, 0) + $3
				WHERE id = $4`, rec.PromptTokens, rec.CompletionTokens, rec.TotalTokens, rec.APIKeyID); err != nil {
				return 0, false, err
			}
		}
		if rec.AccountID != "" {
			if _, err := tx.Exec(ctx, `
				UPDATE account_pool
				SET prompt_tokens_total = COALESCE(prompt_tokens_total, 0) + $1,
				    completion_tokens_total = COALESCE(completion_tokens_total, 0) + $2,
				    total_tokens_total = COALESCE(total_tokens_total, 0) + $3,
				    updated_at = now()
				WHERE account_id = $4`, rec.PromptTokens, rec.CompletionTokens, rec.TotalTokens, rec.AccountID); err != nil {
				return 0, false, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, err
	}
	return eventID, true, nil
}

func normalizeUsageRecord(rec UsageRecord) UsageRecord {
	rec.Implementation = strings.TrimSpace(rec.Implementation)
	if rec.Implementation == "" {
		rec.Implementation = "go"
	}
	rec.RequestID = strings.TrimSpace(rec.RequestID)
	rec.APIKeyID = limitString(rec.APIKeyID, 256)
	rec.AccountID = limitString(rec.AccountID, 256)
	rec.Model = limitString(rec.Model, 120)
	rec.Protocol = limitString(rec.Protocol, 40)
	rec.Path = limitString(rec.Path, 200)
	rec.ClientIP = limitString(rec.ClientIP, 80)
	rec.UserAgent = limitString(rec.UserAgent, 300)
	rec.Error = limitString(rec.Error, 500)
	rec.PromptTokens = nonNegative(rec.PromptTokens)
	rec.CompletionTokens = nonNegative(rec.CompletionTokens)
	rec.TotalTokens = nonNegative(rec.TotalTokens)
	if rec.TotalTokens <= 0 {
		rec.TotalTokens = rec.PromptTokens + rec.CompletionTokens
	}
	rec.CacheReadTokens = nonNegative(rec.CacheReadTokens)
	rec.CacheCreationTokens = nonNegative(rec.CacheCreationTokens)
	rec.ReasoningTokens = nonNegative(rec.ReasoningTokens)
	if !rec.OK {
		rec.PromptTokens = 0
		rec.CompletionTokens = 0
		rec.TotalTokens = 0
		rec.CacheReadTokens = 0
		rec.CacheCreationTokens = 0
		rec.ReasoningTokens = 0
	}
	if rec.Detail == nil {
		rec.Detail = map[string]any{}
	}
	return rec
}

func upsertUsageDaily(ctx context.Context, tx pgx.Tx, rec UsageRecord) error {
	request, success, fail := int64(1), int64(0), int64(1)
	if rec.OK {
		success, fail = 1, 0
	}
	dims := [][2]string{{"global", ""}}
	if rec.APIKeyID != "" && rec.APIKeyID != "env" {
		dims = append(dims, [2]string{"key", rec.APIKeyID})
	}
	if rec.AccountID != "" {
		dims = append(dims, [2]string{"account", rec.AccountID})
	}
	if rec.Model != "" {
		dims = append(dims, [2]string{"model", rec.Model})
	}
	for _, dim := range dims {
		if _, err := tx.Exec(ctx, `
			INSERT INTO usage_daily (
				day, dim, dim_id, requests, success, fail,
				prompt_tokens, completion_tokens, total_tokens, updated_at
			) VALUES (
				CURRENT_DATE, $1, $2, $3, $4, $5, $6, $7, $8, now()
			)
			ON CONFLICT (day, dim, dim_id) DO UPDATE SET
				requests = usage_daily.requests + EXCLUDED.requests,
				success = usage_daily.success + EXCLUDED.success,
				fail = usage_daily.fail + EXCLUDED.fail,
				prompt_tokens = usage_daily.prompt_tokens + EXCLUDED.prompt_tokens,
				completion_tokens = usage_daily.completion_tokens + EXCLUDED.completion_tokens,
				total_tokens = usage_daily.total_tokens + EXCLUDED.total_tokens,
				updated_at = now()`, dim[0], dim[1], request, success, fail, rec.PromptTokens, rec.CompletionTokens, rec.TotalTokens); err != nil {
			return err
		}
	}
	return nil
}

func limitString(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func epochOrNil(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func UsageFromOpenAI(value any) (prompt, completion, total, cacheRead, cacheCreate, reasoning int64) {
	usage, _ := value.(map[string]any)
	if len(usage) == 0 {
		return 0, 0, 0, 0, 0, 0
	}
	prompt = int64FromAny(usage["prompt_tokens"])
	completion = int64FromAny(usage["completion_tokens"])
	total = int64FromAny(usage["total_tokens"])
	if total <= 0 {
		total = prompt + completion
	}
	cacheRead = int64FromAny(usage["cached_tokens"])
	for _, key := range []string{"prompt_tokens_details", "input_tokens_details"} {
		if details, _ := usage[key].(map[string]any); details != nil {
			if cacheRead == 0 {
				cacheRead = int64FromAny(details["cached_tokens"])
			}
			if cacheCreate == 0 {
				cacheCreate = int64FromAny(details["cache_creation_tokens"])
			}
		}
	}
	for _, key := range []string{"completion_tokens_details", "output_tokens_details"} {
		if details, _ := usage[key].(map[string]any); details != nil && reasoning == 0 {
			reasoning = int64FromAny(details["reasoning_tokens"])
		}
	}
	if reasoning == 0 {
		reasoning = int64FromAny(usage["reasoning_tokens"])
	}
	return prompt, completion, total, cacheRead, cacheCreate, reasoning
}

func int64FromAny(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

func (c *Connector) UsageSummary(ctx context.Context, days int) (map[string]any, error) {
	days = clamp(days, 1, 90, 7)
	today, err := c.usageRange(ctx, 1)
	if err != nil {
		return nil, err
	}
	window, err := c.usageRange(ctx, days)
	if err != nil {
		return nil, err
	}
	life, err := c.usageLifetime(ctx)
	if err != nil {
		return nil, err
	}
	series, err := c.UsageSeries(ctx, days)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":       true,
		"days":     days,
		"today":    today.mapWithRate(),
		"window":   window.mapWithRate(),
		"lifetime": life.mapWithRate(),
		"cache":    map[string]any{"ok": false, "source": "postgres", "today": map[string]any{}, "window": map[string]any{}, "lifetime": map[string]any{}, "days": days},
		"series":   series["items"],
		"source":   "postgres",
		"light":    map[string]any{"today_requests": today.Requests, "today_tokens": today.TotalTokens, "total_tokens": life.TotalTokens},
	}, nil
}

func (c *Connector) UsageSeries(ctx context.Context, days int) (map[string]any, error) {
	days = clamp(days, 1, 90, 7)
	rows, err := c.Pool.Query(ctx, `
		SELECT day, requests, success, fail, prompt_tokens, completion_tokens, total_tokens
		FROM usage_daily
		WHERE dim = 'global' AND dim_id = '' AND day >= CURRENT_DATE - (($1::int - 1) * INTERVAL '1 day')
		ORDER BY day ASC`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var day time.Time
		var totals usageTotals
		if err := rows.Scan(&day, &totals.Requests, &totals.Success, &totals.Fail, &totals.PromptTokens, &totals.CompletionTokens, &totals.TotalTokens); err != nil {
			return nil, err
		}
		item := totals.mapWithRate()
		item["day"] = day.Format("2006-01-02")
		items = append(items, item)
	}
	return map[string]any{"ok": true, "days": days, "items": items, "source": "postgres"}, rows.Err()
}

func (c *Connector) UsageBreakdown(ctx context.Context, dim string, days, limit int) (map[string]any, error) {
	dim = strings.TrimSpace(strings.ToLower(dim))
	if dim == "api_key" {
		dim = "key"
	}
	if dim != "key" && dim != "account" && dim != "model" {
		return map[string]any{"ok": false, "error": "dim must be key|account|model", "items": []any{}}, nil
	}
	days = clamp(days, 1, 90, 7)
	limit = clamp(limit, 1, 200, 50)
	rows, err := c.Pool.Query(ctx, `
		SELECT dim_id,
		       COALESCE(SUM(requests), 0), COALESCE(SUM(success), 0), COALESCE(SUM(fail), 0),
		       COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0), COALESCE(SUM(total_tokens), 0)
		FROM usage_daily
		WHERE dim = $1 AND day >= CURRENT_DATE - (($2::int - 1) * INTERVAL '1 day')
		GROUP BY dim_id
		ORDER BY COALESCE(SUM(total_tokens), 0) DESC, COALESCE(SUM(requests), 0) DESC, dim_id ASC
		LIMIT $3`, dim, days, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id string
		var totals usageTotals
		if err := rows.Scan(&id, &totals.Requests, &totals.Success, &totals.Fail, &totals.PromptTokens, &totals.CompletionTokens, &totals.TotalTokens); err != nil {
			return nil, err
		}
		item := totals.mapWithRate()
		item["id"] = id
		item["dim_id"] = id
		item["dim"] = dim
		items = append(items, item)
	}
	return map[string]any{"ok": true, "dim": dim, "days": days, "items": items, "source": "postgres"}, rows.Err()
}

func (c *Connector) UsageEvents(ctx context.Context, page, pageSize int, filters map[string]string, okFlag *bool) (map[string]any, error) {
	page = clamp(page, 1, 1_000_000, 1)
	pageSize = clamp(pageSize, 1, 200, 50)
	where := []string{}
	args := []any{}
	for _, field := range []string{"api_key_id", "account_id", "model", "protocol", "client_ip"} {
		if value := strings.TrimSpace(filters[field]); value != "" {
			args = append(args, value)
			where = append(where, field+" = $"+itoaSQL(len(args)))
		}
	}
	if q := strings.TrimSpace(filters["q"]); q != "" {
		args = append(args, "%"+q+"%")
		where = append(where, "(COALESCE(error,'') ILIKE $"+itoaSQL(len(args))+" OR COALESCE(path,'') ILIKE $"+itoaSQL(len(args))+" OR COALESCE(model,'') ILIKE $"+itoaSQL(len(args))+")")
	}
	if okFlag != nil {
		args = append(args, *okFlag)
		where = append(where, "ok = $"+itoaSQL(len(args)))
	}
	wh := ""
	if len(where) > 0 {
		wh = " WHERE " + strings.Join(where, " AND ")
	}
	var total int64
	if err := c.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM usage_events"+wh, args...).Scan(&total); err != nil {
		return nil, err
	}
	totalPages := int(math.Max(1, math.Ceil(float64(total)/float64(pageSize))))
	if page > totalPages {
		page = totalPages
	}
	offset := (page - 1) * pageSize
	queryArgs := append(append([]any{}, args...), pageSize, offset)
	rows, err := c.Pool.Query(ctx, `
		SELECT id, created_at, api_key_id, account_id, model, protocol, path, stream, ok,
		       prompt_tokens, completion_tokens, total_tokens, cache_read_tokens,
		       cache_creation_tokens, reasoning_tokens, client_ip, user_agent,
		       status_code, latency_ms, ttft_ms, error, detail
		FROM usage_events`+wh+`
		ORDER BY created_at DESC, id DESC
		LIMIT $`+itoaSQL(len(args)+1)+` OFFSET $`+itoaSQL(len(args)+2), queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var createdAt *time.Time
		var apiKeyID, accountID, model, protocol, path, clientIP, userAgent, errText *string
		var stream, okValue *bool
		var prompt, completion, totalTok, cacheRead, cacheCreate, reasoning int64
		var statusCode, latency, ttft *int
		var detail []byte
		if scanErr := rows.Scan(&id, &createdAt, &apiKeyID, &accountID, &model, &protocol, &path, &stream, &okValue, &prompt, &completion, &totalTok, &cacheRead, &cacheCreate, &reasoning, &clientIP, &userAgent, &statusCode, &latency, &ttft, &errText, &detail); scanErr != nil {
			return nil, scanErr
		}
		items = append(items, map[string]any{
			"id": id, "created_at": unixOrNil(createdAt), "api_key_id": stringPtr(apiKeyID), "account_id": stringPtr(accountID),
			"model": stringPtr(model), "protocol": stringPtr(protocol), "path": stringPtr(path), "stream": boolPtr(stream), "ok": boolPtr(okValue),
			"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": totalTok, "cache_read_tokens": cacheRead,
			"cache_creation_tokens": cacheCreate, "reasoning_tokens": reasoning, "client_ip": stringPtr(clientIP), "user_agent": stringPtr(userAgent),
			"status_code": intPtr(statusCode), "latency_ms": intPtr(latency), "ttft_ms": intPtr(ttft), "error": stringPtr(errText), "detail": decodeMap(detail),
		})
	}
	return map[string]any{"ok": true, "items": items, "total": total, "page": page, "page_size": pageSize, "total_pages": totalPages, "source": "postgres"}, rows.Err()
}

func (c *Connector) usageRange(ctx context.Context, days int) (usageTotals, error) {
	var totals usageTotals
	err := c.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(requests),0), COALESCE(SUM(success),0), COALESCE(SUM(fail),0),
		       COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COALESCE(SUM(total_tokens),0)
		FROM usage_daily
		WHERE dim = 'global' AND dim_id = '' AND day >= CURRENT_DATE - (($1::int - 1) * INTERVAL '1 day')`, days,
	).Scan(&totals.Requests, &totals.Success, &totals.Fail, &totals.PromptTokens, &totals.CompletionTokens, &totals.TotalTokens)
	return totals, err
}

func (c *Connector) usageLifetime(ctx context.Context) (usageTotals, error) {
	var totals usageTotals
	err := c.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(requests),0), COALESCE(SUM(success),0), COALESCE(SUM(fail),0),
		       COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COALESCE(SUM(total_tokens),0)
		FROM usage_daily
		WHERE dim = 'global' AND dim_id = ''`,
	).Scan(&totals.Requests, &totals.Success, &totals.Fail, &totals.PromptTokens, &totals.CompletionTokens, &totals.TotalTokens)
	return totals, err
}

func successRate(success, requests int64) any {
	if requests <= 0 {
		return nil
	}
	return math.Round(10000*float64(success)/float64(requests)) / 100
}

func intPtr(ptr *int) any {
	if ptr == nil {
		return nil
	}
	return *ptr
}

func clamp(value, min, max, fallback int) int {
	if value == 0 {
		value = fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
