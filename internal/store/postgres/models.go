package postgres

import (
	"context"
	"encoding/json"
	"time"
)

type ModelRecord struct {
	ID                      string
	Name                    *string
	Description             *string
	OwnedBy                 string
	Hidden                  bool
	Synthetic               bool
	ContextWindow           *int64
	SupportsReasoningEffort *bool
	Extra                   map[string]any
	SortOrder               int
	FetchedAt               *time.Time
	UpdatedAt               *time.Time
}

func (c *Connector) ListModels(ctx context.Context, includeHidden bool) ([]ModelRecord, error) {
	query := `
		SELECT id, name, description, owned_by, hidden, synthetic,
		       context_window, supports_reasoning_effort, extra,
		       sort_order, fetched_at, updated_at
		FROM models
		WHERE ($1::boolean OR hidden = false)
		ORDER BY sort_order ASC, id ASC`
	rows, err := c.Pool.Query(ctx, query, includeHidden)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ModelRecord{}
	for rows.Next() {
		var rec ModelRecord
		var extra []byte
		if err := rows.Scan(
			&rec.ID,
			&rec.Name,
			&rec.Description,
			&rec.OwnedBy,
			&rec.Hidden,
			&rec.Synthetic,
			&rec.ContextWindow,
			&rec.SupportsReasoningEffort,
			&extra,
			&rec.SortOrder,
			&rec.FetchedAt,
			&rec.UpdatedAt,
		); err != nil {
			return nil, err
		}
		rec.Extra = decodeJSONObject(extra)
		if rec.OwnedBy == "" {
			rec.OwnedBy = "xai"
		}
		if rec.SortOrder == 0 && rec.ID != "" {
			rec.SortOrder = 100
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func decodeJSONObject(data []byte) map[string]any {
	if len(data) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}
