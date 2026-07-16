package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

const adminSessionTTL = 7 * 24 * time.Hour

func (c *Connector) VerifyAdminSession(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" || c == nil || c.Pool == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var data []byte
	if err := c.Pool.QueryRow(ctx, "SELECT value FROM app_settings WHERE key = 'sessions'").Scan(&data); err != nil {
		return false
	}
	var sessions map[string]any
	if err := json.Unmarshal(data, &sessions); err != nil || sessions == nil {
		return false
	}
	raw, ok := sessions[token]
	if !ok {
		return false
	}
	ts, ok := toFloat(raw)
	if !ok || time.Since(time.Unix(int64(ts), 0)) > adminSessionTTL {
		return false
	}
	return true
}

func toFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int64:
		return float64(v), true
	case int:
		return float64(v), true
	default:
		return 0, false
	}
}
