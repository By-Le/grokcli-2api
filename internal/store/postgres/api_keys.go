package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type APIKeyRecord struct {
	ID           string
	Name         string
	Prefix       string
	KeyHash      string
	Secret       *string
	Enabled      bool
	Note         string
	CreatedAt    *time.Time
	LastUsedAt   *time.Time
	RequestCount int64
}

func (c *Connector) ListAPIKeys(ctx context.Context) ([]APIKeyRecord, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id, name, prefix, key_hash, secret, enabled, note,
		       created_at, last_used_at, request_count
		FROM api_keys ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []APIKeyRecord{}
	for rows.Next() {
		var rec APIKeyRecord
		if err := rows.Scan(
			&rec.ID,
			&rec.Name,
			&rec.Prefix,
			&rec.KeyHash,
			&rec.Secret,
			&rec.Enabled,
			&rec.Note,
			&rec.CreatedAt,
			&rec.LastUsedAt,
			&rec.RequestCount,
		); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (c *Connector) FindAPIKeyByHash(ctx context.Context, keyHash string) (*APIKeyRecord, error) {
	if keyHash == "" {
		return nil, nil
	}
	row := c.Pool.QueryRow(ctx, `
		SELECT id, name, prefix, key_hash, secret, enabled, note,
		       created_at, last_used_at, request_count
		FROM api_keys WHERE key_hash = $1 LIMIT 1`, keyHash)
	var rec APIKeyRecord
	if err := row.Scan(
		&rec.ID,
		&rec.Name,
		&rec.Prefix,
		&rec.KeyHash,
		&rec.Secret,
		&rec.Enabled,
		&rec.Note,
		&rec.CreatedAt,
		&rec.LastUsedAt,
		&rec.RequestCount,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &rec, nil
}

func (c *Connector) HasEnabledAPIKeys(ctx context.Context) (bool, error) {
	row := c.Pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM api_keys WHERE enabled = true)")
	var exists bool
	if err := row.Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (c *Connector) TouchAPIKeyUsage(ctx context.Context, keyID string) error {
	if keyID == "" || keyID == "env" {
		return nil
	}
	_, err := c.Pool.Exec(ctx, `
		UPDATE api_keys
		SET request_count = request_count + 1,
		    last_used_at = now()
		WHERE id = $1`, keyID)
	return err
}

func (r APIKeyRecord) PublicMap() map[string]any {
	out := map[string]any{
		"id":            r.ID,
		"name":          nonEmpty(r.Name, "unnamed"),
		"prefix":        r.Prefix,
		"created_at":    unixOrZero(r.CreatedAt),
		"enabled":       r.Enabled,
		"note":          r.Note,
		"last_used_at":  unixOrNil(r.LastUsedAt),
		"request_count": r.RequestCount,
		"key_hint":      r.Prefix + "…****",
		"has_secret":    r.Secret != nil && strings.TrimSpace(*r.Secret) != "",
	}
	if r.Secret != nil {
		secret := strings.TrimSpace(*r.Secret)
		if secret != "" && !strings.HasPrefix(secret, "enc:v1:") {
			out["secret"] = secret
		}
	}
	return out
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func unixOrZero(value *time.Time) float64 {
	if value == nil {
		return 0
	}
	return float64(value.UnixNano()) / 1e9
}

func unixOrNil(value *time.Time) any {
	if value == nil {
		return nil
	}
	return float64(value.UnixNano()) / 1e9
}
