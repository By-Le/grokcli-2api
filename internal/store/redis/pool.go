package redis

import (
	"context"
	"strconv"
	"strings"
	"time"
)

const (
	InflightTTLSeconds = 90
	SoftUsedTTLSeconds = 45
)

func (c *Client) RRNext(ctx context.Context) (int64, error) {
	value, err := c.command(ctx, "INCR", c.key("rr", "index"))
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
}

func (c *Client) MarkInflight(ctx context.Context, accountID string, ttlSeconds int) (int64, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return 0, nil
	}
	if ttlSeconds <= 0 {
		ttlSeconds = InflightTTLSeconds
	}
	key := c.key("inflight", accountID)
	value, err := c.command(ctx, "INCR", key)
	if err != nil {
		return 0, err
	}
	_, _ = c.command(ctx, "EXPIRE", key, strconv.Itoa(ttlSeconds))
	return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
}

func (c *Client) ReleaseInflight(ctx context.Context, accountID string) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil
	}
	key := c.key("inflight", accountID)
	value, err := c.command(ctx, "DECR", key)
	if err != nil {
		return err
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if n <= 0 {
		_, err = c.command(ctx, "DEL", key)
		return err
	}
	_, err = c.command(ctx, "EXPIRE", key, strconv.Itoa(InflightTTLSeconds))
	return err
}

func (c *Client) GetInflight(ctx context.Context, accountID string) (int64, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return 0, nil
	}
	value, err := c.command(ctx, "GET", c.key("inflight", accountID))
	if err != nil || strings.TrimSpace(value) == "" {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || n < 0 {
		return 0, nil
	}
	return n, nil
}

func (c *Client) MarkSoftUsed(ctx context.Context, accountID string, ttlSeconds int, now time.Time) (float64, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return 0, nil
	}
	if ttlSeconds <= 0 {
		ttlSeconds = SoftUsedTTLSeconds
	}
	if now.IsZero() {
		now = time.Now()
	}
	stamp := float64(now.UnixNano()) / 1e9
	_, err := c.command(ctx, "SET", c.key("soft_used", accountID), strconv.FormatFloat(stamp, 'f', 6, 64), "EX", strconv.Itoa(ttlSeconds))
	return stamp, err
}

func (c *Client) MirrorCooldown(ctx context.Context, accountID string, until time.Time) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil
	}
	key := c.key("cooldown", accountID)
	if until.IsZero() || !until.After(time.Now()) {
		_, err := c.command(ctx, "DEL", key)
		return err
	}
	ttl := int(time.Until(until).Seconds())
	if ttl < 1 {
		ttl = 1
	}
	_, err := c.command(ctx, "SET", key, strconv.FormatInt(until.Unix(), 10), "EX", strconv.Itoa(ttl))
	return err
}
