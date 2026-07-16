package postgres

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

type AccountList struct {
	Accounts   []map[string]any `json:"accounts"`
	Total      int64            `json:"total"`
	Page       int              `json:"page"`
	PageSize   int              `json:"page_size"`
	TotalPages int              `json:"total_pages"`
	Query      string           `json:"q"`
	Sort       string           `json:"sort"`
}

func (c *Connector) ListAccountSummaries(ctx context.Context, page, pageSize int, query, sort string) (AccountList, error) {
	sort = normalizeAccountSort(sort)
	orderBy := accountOrderSQL(sort)
	query = strings.TrimSpace(strings.ToLower(query))
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize >= 10000 {
		pageSize = 0
	} else if pageSize > 200 {
		pageSize = 200
	}

	where := ""
	args := []any{}
	if query != "" {
		where = "WHERE lower(COALESCE(email,'')) LIKE $1 OR lower(id) LIKE $1 OR lower(COALESCE(user_id,'')) LIKE $1"
		args = append(args, "%"+query+"%")
	}

	var total int64
	if err := c.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM accounts "+where, args...).Scan(&total); err != nil {
		return AccountList{}, err
	}
	limitClause := ""
	pageOut := page
	pageSizeOut := pageSize
	totalPages := 1
	if pageSize == 0 {
		pageOut = 1
		pageSizeOut = int(total)
	} else {
		totalPages = int(math.Max(1, math.Ceil(float64(total)/float64(pageSize))))
		if pageOut > totalPages {
			pageOut = totalPages
		}
		offset := (pageOut - 1) * pageSize
		limitClause = " LIMIT $" + itoaSQL(len(args)+1) + " OFFSET $" + itoaSQL(len(args)+2)
		args = append(args, pageSize, offset)
	}

	sql := `
		SELECT a.id, a.email, a.user_id, a.team_id, a.payload, a.expires_at, a.updated_at,
		       ap.enabled, ap.weight, ap.request_count, ap.success_count, ap.fail_count,
		       ap.last_used_at, ap.last_error, ap.cooldown_until, ap.disabled_for_quota,
		       ap.disabled_reason, ap.quota_disabled_at, ap.quota_source, ap.last_quota,
		       ap.last_probe, ap.blocked_models
		FROM accounts a
		LEFT JOIN account_pool ap ON ap.account_id = a.id ` + where + ` ORDER BY ` + orderBy + limitClause
	rows, err := c.Pool.Query(ctx, sql, args...)
	if err != nil {
		return AccountList{}, err
	}
	defer rows.Close()

	now := time.Now()
	accounts := []map[string]any{}
	for rows.Next() {
		var id string
		var email, userID, teamID *string
		var payloadBytes []byte
		var expiresAt, updatedAt, lastUsedAt, cooldownUntil, quotaDisabledAt *time.Time
		var enabled, disabledForQuota *bool
		var weight *int
		var requestCount, successCount, failCount *int64
		var lastError, disabledReason, quotaSource *string
		var lastQuota, lastProbe, blockedModels []byte
		if err := rows.Scan(&id, &email, &userID, &teamID, &payloadBytes, &expiresAt, &updatedAt, &enabled, &weight, &requestCount, &successCount, &failCount, &lastUsedAt, &lastError, &cooldownUntil, &disabledForQuota, &disabledReason, &quotaDisabledAt, &quotaSource, &lastQuota, &lastProbe, &blockedModels); err != nil {
			return AccountList{}, err
		}
		_ = lastQuota
		_ = lastProbe
		payload := decodeMap(payloadBytes)
		token, _ := firstString(payload, "key", "access_token", "token")
		expired := expiresAt != nil && now.After(*expiresAt)
		poolEnabled := true
		if enabled != nil {
			poolEnabled = *enabled
		}
		poolWeight := int64(1)
		if weight != nil {
			poolWeight = int64(*weight)
		}
		quotaDisabled := false
		if disabledForQuota != nil {
			quotaDisabled = *disabledForQuota
		}
		blocked := decodeMap(blockedModels)
		accounts = append(accounts, map[string]any{
			"id":                id,
			"email":             firstNonNilString(email, stringFromMap(payload, "email")),
			"user_id":           firstNonNilString(userID, firstMapString(payload, "user_id", "principal_id")),
			"team_id":           firstNonNilString(teamID, stringFromMap(payload, "team_id")),
			"auth_mode":         payload["auth_mode"],
			"create_time":       payload["create_time"],
			"updated_at":        unixOrNil(updatedAt),
			"expires_at":        unixOrNil(expiresAt),
			"expired":           expired,
			"has_refresh_token": strings.TrimSpace(stringFromMap(payload, "refresh_token")) != "",
			"has_sso":           hasSSO(payload),
			"token_hint":        tokenHint(token),
			"first_name":        payload["first_name"],
			"last_name":         payload["last_name"],
			"principal_type":    payload["principal_type"],
			"source":            payload["source"],
			"_pool": map[string]any{
				"id":                     id,
				"enabled":                poolEnabled,
				"weight":                 poolWeight,
				"request_count":          int64OrZero(requestCount),
				"success_count":          int64OrZero(successCount),
				"fail_count":             int64OrZero(failCount),
				"last_used_at":           unixOrNil(lastUsedAt),
				"last_error":             stringPtr(lastError),
				"cooldown_until":         unixOrNil(cooldownUntil),
				"cooldown_remaining_sec": cooldownRemaining(now, cooldownUntil),
				"in_cooldown":            cooldownRemaining(now, cooldownUntil) > 0,
				"disabled_for_quota":     quotaDisabled,
				"disabled_reason":        stringPtr(disabledReason),
				"quota_disabled_at":      unixOrNil(quotaDisabledAt),
				"quota_source":           stringPtr(quotaSource),
				"last_quota":             decodeMap(lastQuota),
				"last_probe":             decodeMap(lastProbe),
				"blocked_model_ids":      mapKeys(blocked),
			}})
	}
	if err := rows.Err(); err != nil {
		return AccountList{}, err
	}
	return AccountList{Accounts: accounts, Total: total, Page: pageOut, PageSize: pageSizeOut, TotalPages: totalPages, Query: query, Sort: sort}, nil
}

func normalizeAccountSort(sort string) string {
	key := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(sort)), "-", "_")
	switch key {
	case "old", "updated_asc":
		return "oldest"
	case "new", "updated_desc", "":
		return "newest"
	case "email_asc", "email_desc", "expires_desc", "expires_asc", "last_used_desc", "last_used_asc", "requests_desc", "cooldown_first", "disabled_first":
		return key
	default:
		return "newest"
	}
}

func accountOrderSQL(sort string) string {
	switch sort {
	case "oldest":
		return "a.updated_at ASC NULLS LAST, a.id ASC"
	case "email_asc":
		return "lower(COALESCE(a.email, '')) ASC, a.id ASC"
	case "email_desc":
		return "lower(COALESCE(a.email, '')) DESC, a.id DESC"
	case "expires_desc":
		return "a.expires_at DESC NULLS LAST, a.updated_at DESC"
	case "expires_asc":
		return "a.expires_at ASC NULLS LAST, a.updated_at DESC"
	case "last_used_desc":
		return "ap.last_used_at DESC NULLS LAST, a.updated_at DESC"
	case "last_used_asc":
		return "ap.last_used_at ASC NULLS LAST, a.updated_at DESC"
	case "requests_desc":
		return "COALESCE(ap.request_count, 0) DESC, a.updated_at DESC"
	case "cooldown_first":
		return "(CASE WHEN ap.cooldown_until IS NOT NULL AND ap.cooldown_until > now() THEN 0 ELSE 1 END) ASC, a.updated_at DESC"
	case "disabled_first":
		return "(CASE WHEN COALESCE(ap.enabled, true) = false OR COALESCE(ap.disabled_for_quota, false) = true THEN 0 ELSE 1 END) ASC, a.updated_at DESC"
	default:
		return "a.updated_at DESC NULLS LAST, a.id DESC"
	}
}

func decodeMap(data []byte) map[string]any {
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func firstString(m map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if s := stringFromMap(m, key); s != "" {
			return s, true
		}
	}
	return "", false
}

func firstMapString(m map[string]any, keys ...string) string {
	s, _ := firstString(m, keys...)
	return s
}

func stringFromMap(m map[string]any, key string) string {
	if value, ok := m[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func firstNonNilString(ptr *string, fallback string) any {
	if ptr != nil && *ptr != "" {
		return *ptr
	}
	if fallback != "" {
		return fallback
	}
	return nil
}

func stringPtr(ptr *string) any {
	if ptr == nil {
		return nil
	}
	return *ptr
}

func int64OrZero(ptr *int64) int64 {
	if ptr == nil {
		return 0
	}
	return *ptr
}

func cooldownRemaining(now time.Time, until *time.Time) float64 {
	if until == nil || !until.After(now) {
		return 0
	}
	return until.Sub(now).Seconds()
}

func tokenHint(token string) string {
	if len(token) > 12 {
		return token[:6] + "..." + token[len(token)-4:]
	}
	if token != "" {
		return "****"
	}
	return ""
}

func hasSSO(payload map[string]any) bool {
	for _, key := range []string{"sso", "sso_cookie", "sso_token", "cookie", "cookies", "set_cookie", "set-cookie", "set_cookies"} {
		if strings.Contains(strings.ToLower(stringFromMap(payload, key)), "sso") || stringFromMap(payload, key) != "" && strings.HasPrefix(key, "sso") {
			return true
		}
	}
	return false
}

func mapKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	return keys
}

func itoaSQL(value int) string {
	return strconv.Itoa(value)
}
