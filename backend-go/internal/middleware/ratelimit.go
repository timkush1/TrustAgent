package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strconv"
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

// CodeRateLimited is the error code of a rate-limited request (SPEC §2.4: 429 rate_limited).
const CodeRateLimited = "rate_limited"

// Deny writes a refused request in the error shape of the route family (ApiErrorBody on /api, the
// upstream API's shape on /v1). The middleware aborts the chain after it.
type Deny func(c *gin.Context, status int, code, message string)

// KeyFunc names the rate-limit bucket of an authenticated caller (SPEC §2.4: per key id, per
// session id for cookie auth). "" means the caller is not authenticated by an identity the
// function knows, and the bucket falls back to the presented key or the client IP.
type KeyFunc func(c *gin.Context) string

// RateLimit enforces a per-client fixed-window limit. A caller that presents a key gets its own
// bucket (so NAT'd users don't share one); anyone else is limited per client IP. A refused request
// gets 429 rate_limited with Retry-After, written by deny (nil writes an ApiErrorBody).
func RateLimit(limiter Limiter, limit int, window time.Duration, deny Deny) gin.HandlerFunc {
	return RateLimitBy(limiter, limit, window, deny, nil)
}

// RateLimitBy is RateLimit with the caller's identity: when key names one (it runs after the
// authentication middleware), the bucket is that identity's, so cookie sessions behind one IP get
// one bucket each (M8). A nil key behaves as RateLimit.
func RateLimitBy(limiter Limiter, limit int, window time.Duration, deny Deny, key KeyFunc) gin.HandlerFunc {
	if deny == nil {
		deny = func(c *gin.Context, status int, code, message string) {
			c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
		}
	}
	retryAfter := strconv.Itoa(max(int(window.Seconds()), 1))
	return func(c *gin.Context) {
		bucket := c.FullPath() + ":" + clientKey(c, key)
		if !limiter.Allow(c.Request.Context(), bucket, limit, window) {
			c.Header("Retry-After", retryAfter)
			deny(c, http.StatusTooManyRequests, CodeRateLimited,
				fmt.Sprintf("Rate limit exceeded (%d requests per %s); retry after %s s.", limit, window, retryAfter))
			c.Abort()
			return
		}
		c.Next()
	}
}

// clientKey names a caller's bucket: a digest of its identity (key, when it names one), else a
// digest of the key it presents (X-TrustAgent-Key, else Authorization: Bearer or X-API-Key), else
// its IP. The raw value never becomes a bucket name: buckets are Redis keys, and on /v1 in
// passthrough mode Authorization is the caller's upstream credential.
func clientKey(c *gin.Context, key KeyFunc) string {
	if key != nil {
		if id := key(c); id != "" {
			sum := sha256.Sum256([]byte(id))
			return "id:" + hex.EncodeToString(sum[:16])
		}
	}
	k := c.GetHeader("X-TrustAgent-Key")
	if k == "" {
		k = extractKey(c.Request)
	}
	if k == "" {
		return "ip:" + c.ClientIP()
	}
	sum := sha256.Sum256([]byte(k))
	return "key:" + hex.EncodeToString(sum[:16])
}
