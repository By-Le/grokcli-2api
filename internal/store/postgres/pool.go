package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hm2899/grokcli-2api/internal/pool"
)

func (c *Connector) ListPoolCandidates(ctx context.Context) ([]pool.Candidate, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT a.id, a.payload, a.email, a.user_id, a.team_id, a.expires_at,
		       COALESCE(ap.enabled, true), COALESCE(ap.disabled_for_quota, false),
		       ap.cooldown_until, COALESCE(ap.blocked_models, '{}'::jsonb),
		       COALESCE(ap.request_count, 0), COALESCE(ap.weight, 1)
		FROM accounts a
		LEFT JOIN account_pool ap ON ap.account_id = a.id
		ORDER BY COALESCE(ap.weight, 1) DESC, COALESCE(ap.request_count, 0) ASC, a.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pool.Candidate{}
	for rows.Next() {
		var candidate pool.Candidate
		var payloadBytes, blockedBytes []byte
		var email, userID, teamID *string
		var expiresAt, cooldownUntil *time.Time
		if err := rows.Scan(&candidate.ID, &payloadBytes, &email, &userID, &teamID, &expiresAt, &candidate.Enabled, &candidate.DisabledForQuota, &cooldownUntil, &blockedBytes, &candidate.RequestCount, &candidate.Weight); err != nil {
			return nil, err
		}
		payload := decodeMap(payloadBytes)
		candidate.Token, _ = firstString(payload, "key", "access_token", "token")
		candidate.Email = stringValue(email, stringFromMap(payload, "email"))
		candidate.UserID = stringValue(userID, firstMapString(payload, "user_id", "principal_id"))
		candidate.TeamID = stringValue(teamID, stringFromMap(payload, "team_id"))
		candidate.ExpiresAt = expiresAt
		candidate.CooldownUntil = cooldownUntil
		candidate.BlockedModels = decodeMap(blockedBytes)
		if strings.TrimSpace(candidate.Token) != "" {
			out = append(out, candidate)
		}
	}
	return out, rows.Err()
}

type PoolFailure struct {
	AccountID            string
	Error                string
	StatusCode           *int
	CooldownUntil        *time.Time
	CooldownReason       string
	CooldownCode         string
	CooldownModel        string
	CooldownTokensActual *int64
	CooldownTokensLimit  *int64
	BlockedModel         string
	BlockedUntil         *time.Time
	Detail               map[string]any
}

func (c *Connector) ReportPoolSuccess(ctx context.Context, accountID string, preserveCooldown bool) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil
	}
	if preserveCooldown {
		_, err := c.Pool.Exec(ctx, `
			INSERT INTO account_pool (account_id, request_count, success_count, last_used_at, extra, updated_at)
			VALUES ($1, 1, 1, now(), '{}'::jsonb, now())
			ON CONFLICT (account_id) DO UPDATE SET
				request_count = account_pool.request_count + 1,
				success_count = account_pool.success_count + 1,
				last_used_at = now(),
				extra = jsonb_set(COALESCE(account_pool.extra, '{}'::jsonb), '{consecutive_fails}', '0'::jsonb, true),
				pool_status = CASE WHEN account_pool.cooldown_until IS NOT NULL AND account_pool.cooldown_until > now() THEN 'cooldown' ELSE account_pool.pool_status END,
				updated_at = now()`, accountID)
		return err
	}
	_, err := c.Pool.Exec(ctx, `
		INSERT INTO account_pool (account_id, request_count, success_count, last_used_at, pool_status, extra, updated_at)
		VALUES ($1, 1, 1, now(), 'normal', '{}'::jsonb, now())
		ON CONFLICT (account_id) DO UPDATE SET
			request_count = account_pool.request_count + 1,
			success_count = account_pool.success_count + 1,
			last_used_at = now(),
			last_error = NULL,
			cooldown_until = NULL,
			cooldown_reason = NULL,
			cooldown_code = NULL,
			cooldown_model = NULL,
			cooldown_tokens_actual = NULL,
			cooldown_tokens_limit = NULL,
			pool_status = CASE WHEN account_pool.enabled = false OR account_pool.disabled_for_quota = true THEN 'disabled' ELSE 'normal' END,
			extra = jsonb_set(COALESCE(account_pool.extra, '{}'::jsonb), '{consecutive_fails}', '0'::jsonb, true),
			updated_at = now()`, accountID)
	return err
}

func (c *Connector) ReportPoolFailure(ctx context.Context, failure PoolFailure) error {
	failure.AccountID = strings.TrimSpace(failure.AccountID)
	if failure.AccountID == "" {
		return nil
	}
	detailBytes, err := json.Marshal(failure.Detail)
	if err != nil {
		return err
	}
	blockedBytes := []byte(`{}`)
	if model := strings.TrimSpace(failure.BlockedModel); model != "" {
		blocked := map[string]any{model: true}
		if failure.BlockedUntil != nil {
			blocked[model] = failure.BlockedUntil.Unix()
		}
		blockedBytes, err = json.Marshal(blocked)
		if err != nil {
			return err
		}
	}
	_, err = c.Pool.Exec(ctx, `
		INSERT INTO account_pool (
			account_id, request_count, fail_count, last_used_at, last_error,
			cooldown_until, pool_status, cooldown_count, cooldown_reason,
			cooldown_code, cooldown_model, cooldown_tokens_actual, cooldown_tokens_limit,
			blocked_models, extra, updated_at
		) VALUES (
			$1, 1, 1, now(), $2,
			$3, CASE WHEN $3::timestamptz IS NULL THEN 'normal' ELSE 'cooldown' END,
			CASE WHEN $3::timestamptz IS NULL THEN 0 ELSE 1 END, $4,
			$5, $6, $7, $8,
			$9::jsonb, jsonb_build_object('last_status_code', $10::int, 'cooldown_detail', $11::jsonb, 'consecutive_fails', 1), now()
		)
		ON CONFLICT (account_id) DO UPDATE SET
			request_count = account_pool.request_count + 1,
			fail_count = account_pool.fail_count + 1,
			last_used_at = now(),
			last_error = COALESCE($2, account_pool.last_error),
			cooldown_until = COALESCE($3, account_pool.cooldown_until),
			pool_status = CASE WHEN COALESCE($3, account_pool.cooldown_until) IS NOT NULL AND COALESCE($3, account_pool.cooldown_until) > now() THEN 'cooldown' WHEN account_pool.enabled = false OR account_pool.disabled_for_quota = true THEN 'disabled' ELSE 'normal' END,
			cooldown_count = account_pool.cooldown_count + CASE WHEN $3::timestamptz IS NULL THEN 0 ELSE 1 END,
			cooldown_reason = COALESCE($4, account_pool.cooldown_reason),
			cooldown_code = COALESCE($5, account_pool.cooldown_code),
			cooldown_model = COALESCE($6, account_pool.cooldown_model),
			cooldown_tokens_actual = COALESCE($7, account_pool.cooldown_tokens_actual),
			cooldown_tokens_limit = COALESCE($8, account_pool.cooldown_tokens_limit),
			blocked_models = COALESCE(account_pool.blocked_models, '{}'::jsonb) || $9::jsonb,
			extra = COALESCE(account_pool.extra, '{}'::jsonb) || jsonb_build_object(
				'last_status_code', $10::int,
				'cooldown_detail', $11::jsonb,
				'consecutive_fails', COALESCE((account_pool.extra->>'consecutive_fails')::int, 0) + 1
			),
			updated_at = now()`, failure.AccountID, nilIfEmpty(failure.Error), failure.CooldownUntil, nilIfEmpty(failure.CooldownReason), nilIfEmpty(failure.CooldownCode), nilIfEmpty(failure.CooldownModel), failure.CooldownTokensActual, failure.CooldownTokensLimit, blockedBytes, failure.StatusCode, detailBytes)
	return err
}

func (c *Connector) BlockPoolModel(ctx context.Context, accountID, model string, until *time.Time) error {
	accountID = strings.TrimSpace(accountID)
	model = strings.TrimSpace(model)
	if accountID == "" || model == "" {
		return nil
	}
	value := any(true)
	if until != nil {
		value = until.Unix()
	}
	blocked, err := json.Marshal(map[string]any{model: value})
	if err != nil {
		return err
	}
	_, err = c.Pool.Exec(ctx, `
		INSERT INTO account_pool (account_id, blocked_models, extra, updated_at)
		VALUES ($1, $2::jsonb, '{}'::jsonb, now())
		ON CONFLICT (account_id) DO UPDATE SET
			blocked_models = COALESCE(account_pool.blocked_models, '{}'::jsonb) || $2::jsonb,
			updated_at = now()`, accountID, blocked)
	return err
}

func stringValue(ptr *string, fallback string) string {
	if ptr != nil && strings.TrimSpace(*ptr) != "" {
		return *ptr
	}
	return fallback
}
