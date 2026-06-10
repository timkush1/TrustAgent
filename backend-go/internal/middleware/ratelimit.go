package middleware

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// Limiter answers "may this client perform this action now?".
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) bool
}

// ---------------------------------------------------------------------------
// Redis fixed-window limiter (shared across proxy replicas)
// ---------------------------------------------------------------------------

type RedisLimiter struct {
	client *redis.Client
}

func NewRedisLimiter(redisURL string) (*RedisLimiter, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis URL: %w", err)
	}
	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis unreachable: %w", err)
	}
	return &RedisLimiter{client: client}, nil
}

func (l *RedisLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) bool {
	bucket := fmt.Sprintf("ratelimit:%s:%d", key, time.Now().Unix()/int64(window.Seconds()))
	count, err := l.client.Incr(ctx, bucket).Result()
	if err != nil {
		// Fail open: a degraded Redis must not take the API down with it.
		log.Printf("Rate limiter Redis error (failing open): %v", err)
		return true
	}
	if count == 1 {
		l.client.Expire(ctx, bucket, window)
	}
	return count <= int64(limit)
}

// ---------------------------------------------------------------------------
// In-memory fixed-window limiter (single-instance fallback)
// ---------------------------------------------------------------------------

type MemoryLimiter struct {
	mu      sync.Mutex
	buckets map[string]*memoryBucket
}

type memoryBucket struct {
	windowStart int64
	count       int
}

func NewMemoryLimiter() *MemoryLimiter {
	return &MemoryLimiter{buckets: make(map[string]*memoryBucket)}
}

func (l *MemoryLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) bool {
	now := time.Now().Unix() / int64(window.Seconds())

	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, ok := l.buckets[key]
	if !ok || bucket.windowStart != now {
		// Lazily evict stale buckets to bound memory.
		if len(l.buckets) > 10000 {
			l.buckets = make(map[string]*memoryBucket)
		}
		l.buckets[key] = &memoryBucket{windowStart: now, count: 1}
		return limit >= 1
	}
	bucket.count++
	return bucket.count <= limit
}

// ---------------------------------------------------------------------------
// Gin middleware
// ---------------------------------------------------------------------------

// RateLimit enforces a per-client fixed-window limit. Clients are identified
// by API key when present (so NAT'd users don't share a bucket), falling back
// to client IP.
func RateLimit(limiter Limiter, limit int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		clientKey := extractKey(c.Request)
		if clientKey == "" {
			clientKey = c.ClientIP()
		}
		key := fmt.Sprintf("%s:%s", c.FullPath(), clientKey)

		if !limiter.Allow(c.Request.Context(), key, limit, window) {
			c.Header("Retry-After", fmt.Sprintf("%d", int(window.Seconds())))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "rate limit exceeded",
			})
			return
		}
		c.Next()
	}
}
