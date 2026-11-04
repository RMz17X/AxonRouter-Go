package speedlayer

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RedisStore is the cluster primary backend: a Valkey/Redis server
// reached through the minimal RESP client in resp.go. Keys live in the
// server's own expiry machinery, so a dead node's state ages out on its
// own — the property the counters' sliding TTL and the cooldown locks'
// bounded TTLs rely on.
type RedisStore struct {
	client *respClient
}

// NewRedisStore parses a redis:// or valkey:// URL (rediss:// for TLS)
// and returns the backend. It does not dial: reachability is the
// failover breaker's problem, checked by Ping, so a boot with a dead
// Redis still comes up (on the degradation backend) instead of failing.
func NewRedisStore(rawURL string) (*RedisStore, error) {
	client, err := parseRedisURL(rawURL)
	if err != nil {
		return nil, err
	}
	return &RedisStore{client: client}, nil
}

func parseRedisURL(raw string) (*respClient, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("redis: unparseable URL: %w", err)
	}
	switch u.Scheme {
	case "redis", "valkey", "rediss":
	default:
		return nil, fmt.Errorf("redis: unsupported URL scheme %q (want redis://, valkey:// or rediss://)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("redis: URL has no host")
	}
	port := u.Port()
	if port == "" {
		port = "6379"
	}
	c := &respClient{addr: host + ":" + port, useTLS: u.Scheme == "rediss"}
	if u.User != nil {
		if pw, ok := u.User.Password(); ok {
			c.password = pw
		}
	}
	if p := strings.Trim(u.Path, "/"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("redis: bad database index in URL path %q", u.Path)
		}
		c.dbIndex = n
	}
	return c, nil
}

func (s *RedisStore) Name() string { return "redis" }

func (s *RedisStore) Ping(ctx context.Context) error {
	v, err := s.client.do(ctx, "PING")
	if err != nil {
		return err
	}
	if str, _ := replyString(v); str != "PONG" {
		return fmt.Errorf("redis: unexpected PING reply %v", v)
	}
	return nil
}

func (s *RedisStore) AddCounter(ctx context.Context, key string, delta int64) (int64, error) {
	v, err := s.client.doPair(ctx,
		[]any{"INCRBY", key, delta},
		[]any{"EXPIRE", key, int64(counterTTL / time.Second)},
	)
	if err != nil {
		return 0, err
	}
	n, ok := v.(int64)
	if !ok {
		return 0, fmt.Errorf("redis: INCRBY reply is %T, want integer", v)
	}
	return n, nil
}

func (s *RedisStore) Counters(ctx context.Context, prefix string) (map[string]int64, error) {
	keys, err := s.scanKeys(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(keys)+1)
	args = append(args, "MGET")
	for _, k := range keys {
		args = append(args, k)
	}
	v, err := s.client.do(ctx, args...)
	if err != nil {
		return nil, err
	}
	items, ok := v.([]any)
	if !ok || len(items) != len(keys) {
		return nil, fmt.Errorf("redis: MGET reply shape mismatch")
	}
	for i, item := range items {
		str, ok := replyString(item)
		if !ok {
			continue // raced with the key's expiry between SCAN and MGET
		}
		if n, err := strconv.ParseInt(str, 10, 64); err == nil && n != 0 {
			out[keys[i]] = n
		}
	}
	return out, nil
}

func (s *RedisStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	args := []any{"SET", key, value}
	if ttl > 0 {
		args = append(args, "PX", ttl.Milliseconds())
	}
	_, err := s.client.do(ctx, args...)
	return err
}

func (s *RedisStore) Get(ctx context.Context, key string) (string, bool, error) {
	v, err := s.client.do(ctx, "GET", key)
	if err != nil {
		return "", false, err
	}
	if v == nil {
		return "", false, nil
	}
	str, ok := replyString(v)
	return str, ok, nil
}

func (s *RedisStore) GetMany(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(keys)+1)
	args = append(args, "MGET")
	for _, k := range keys {
		args = append(args, k)
	}
	v, err := s.client.do(ctx, args...)
	if err != nil {
		return nil, err
	}
	items, ok := v.([]any)
	if !ok || len(items) != len(keys) {
		return nil, fmt.Errorf("redis: MGET reply shape mismatch")
	}
	for i, item := range items {
		if str, ok := replyString(item); ok {
			out[keys[i]] = str
		}
	}
	return out, nil
}

func (s *RedisStore) Delete(ctx context.Context, key string) error {
	_, err := s.client.do(ctx, "DEL", key)
	return err
}

func (s *RedisStore) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	keys, err := s.scanKeys(ctx, prefix)
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(keys)+1)
	args = append(args, "DEL")
	for _, k := range keys {
		args = append(args, k)
	}
	v, err := s.client.do(ctx, args...)
	if err != nil {
		return 0, err
	}
	n, _ := v.(int64)
	return int(n), nil
}

func (s *RedisStore) RingPush(ctx context.Context, key, value string, cap int) error {
	if cap <= 0 {
		cap = 1
	}
	_, err := s.client.doPair(ctx,
		[]any{"LPUSH", key, value},
		[]any{"LTRIM", key, 0, int64(cap - 1)},
	)
	return err
}

func (s *RedisStore) RingRead(ctx context.Context, key string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 1
	}
	v, err := s.client.do(ctx, "LRANGE", key, 0, int64(limit-1))
	if err != nil {
		return nil, err
	}
	if v == nil {
		return []string{}, nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("redis: LRANGE reply is %T, want array", v)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if str, ok := replyString(item); ok {
			out = append(out, str)
		}
	}
	return out, nil
}

// scanKeys walks SCAN with a MATCH on prefix until the cursor closes.
func (s *RedisStore) scanKeys(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	cursor := "0"
	for {
		v, err := s.client.do(ctx, "SCAN", cursor, "MATCH", prefix+"*", "COUNT", 200)
		if err != nil {
			return nil, err
		}
		parts, ok := v.([]any)
		if !ok || len(parts) != 2 {
			return nil, fmt.Errorf("redis: SCAN reply shape mismatch")
		}
		next, ok := replyString(parts[0])
		if !ok {
			return nil, fmt.Errorf("redis: SCAN cursor is not a string")
		}
		if batch, ok := parts[1].([]any); ok {
			for _, item := range batch {
				if str, ok := replyString(item); ok {
					keys = append(keys, str)
				}
			}
		}
		cursor = next
		if cursor == "0" {
			return keys, nil
		}
	}
}
