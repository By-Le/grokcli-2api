package postgres

import "context"

type KeyStats struct {
	Total         int64 `json:"total"`
	Enabled       int64 `json:"enabled"`
	Disabled      int64 `json:"disabled"`
	TotalRequests int64 `json:"total_requests"`
}

type PoolSummary struct {
	Mode          string `json:"mode,omitempty"`
	Total         int64  `json:"total"`
	Live          int64  `json:"live"`
	Rotatable     int64  `json:"rotatable"`
	Enabled       int64  `json:"enabled"`
	InCooldown    int64  `json:"in_cooldown"`
	QuotaDisabled int64  `json:"quota_disabled"`
	ModelBlocked  int64  `json:"model_blocked"`
	Expired       int64  `json:"expired"`
	Disabled      int64  `json:"disabled"`
	Source        string `json:"source"`
}

func (c *Connector) CountAccounts(ctx context.Context) (int64, error) {
	return countQuery(ctx, c, "SELECT COUNT(*) FROM accounts")
}

func (c *Connector) CountModels(ctx context.Context, includeHidden bool) (int64, error) {
	if includeHidden {
		return countQuery(ctx, c, "SELECT COUNT(*) FROM models")
	}
	return countQuery(ctx, c, "SELECT COUNT(*) FROM models WHERE hidden = false")
}

func (c *Connector) KeyStats(ctx context.Context, legacyEnvKey bool, authRequired bool) (map[string]any, error) {
	var stats KeyStats
	err := c.Pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE enabled = true),
		       COUNT(*) FILTER (WHERE enabled = false),
		       COALESCE(SUM(request_count), 0)
		FROM api_keys`,
	).Scan(&stats.Total, &stats.Enabled, &stats.Disabled, &stats.TotalRequests)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"total":          stats.Total,
		"enabled":        stats.Enabled,
		"disabled":       stats.Disabled,
		"total_requests": stats.TotalRequests,
		"auth_required":  authRequired,
		"legacy_env_key": legacyEnvKey,
	}, nil
}

func (c *Connector) PoolSummary(ctx context.Context) (PoolSummary, error) {
	var summary PoolSummary
	err := c.Pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE enabled = true),
		       COUNT(*) FILTER (WHERE enabled = true AND disabled_for_quota = false AND (cooldown_until IS NULL OR cooldown_until <= now())),
		       COUNT(*) FILTER (WHERE enabled = true AND disabled_for_quota = false AND (cooldown_until IS NULL OR cooldown_until <= now())),
		       COUNT(*) FILTER (WHERE cooldown_until IS NOT NULL AND cooldown_until > now()),
		       COUNT(*) FILTER (WHERE disabled_for_quota = true),
		       COUNT(*) FILTER (WHERE COALESCE(blocked_models, '{}'::jsonb) <> '{}'::jsonb),
		       COUNT(*) FILTER (WHERE enabled = false)
		FROM account_pool`,
	).Scan(
		&summary.Total,
		&summary.Enabled,
		&summary.Live,
		&summary.Rotatable,
		&summary.InCooldown,
		&summary.QuotaDisabled,
		&summary.ModelBlocked,
		&summary.Disabled,
	)
	if err != nil {
		return summary, err
	}
	summary.Mode = "round_robin"
	summary.Source = "postgres"
	return summary, nil
}

func countQuery(ctx context.Context, c *Connector, sql string) (int64, error) {
	var count int64
	if err := c.Pool.QueryRow(ctx, sql).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
