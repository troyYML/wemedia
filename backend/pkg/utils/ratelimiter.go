package utils

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"WeMediaSpider/backend/pkg/timeutil"
)

// RateLimiter 串行请求队列，确保每次 API 调用之间强制等待指定间隔
// 所有请求通过互斥锁串行化，每次请求前等待 [minInterval, maxInterval] 的随机间隔
type RateLimiter struct {
	mu           sync.Mutex    // 串行化所有 Wait 调用，保证一次只有一个请求在等待/执行
	minInterval  time.Duration // 最小请求间隔
	maxInterval  time.Duration // 最大请求间隔
	lastRequest  time.Time     // 上次请求完成时间
	failureCount int64         // 连续失败次数（使用原子操作，避免与 mu 死锁）
}

// NewRateLimiter 创建串行请求队列
// minInterval: 最小请求间隔
// maxInterval: 最大请求间隔
// maxRequests: 已废弃，保留兼容性
func NewRateLimiter(minInterval, maxInterval time.Duration, maxRequests int) *RateLimiter {
	if minInterval <= 0 {
		minInterval = 2 * time.Second
	}
	if maxInterval < minInterval {
		maxInterval = minInterval
	}
	return &RateLimiter{
		minInterval:  minInterval,
		maxInterval:  maxInterval,
		lastRequest:  time.Time{}, // 零值表示首次请求无需等待
		failureCount: 0,
	}
}

// Wait 等待直到可以发送下一个请求
// 通过互斥锁串行化：持有锁期间计算需要等待的时间并 sleep，
// 这样确保所有请求严格按顺序执行，且两次请求之间必定间隔 [min, max] 秒
func (rl *RateLimiter) Wait() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := timeutil.Now()

	// 非首次请求时，强制等待间隔
	if !rl.lastRequest.IsZero() {
		delay := rl.getNextDelay()
		nextAllowed := rl.lastRequest.Add(delay)

		if now.Before(nextAllowed) {
			waitDuration := nextAllowed.Sub(now)
			// 持有锁期间 sleep，阻塞其他所有 Wait 调用
			// 这保证了严格的串行：下一个请求必须等当前请求的间隔过去后才能开始
			time.Sleep(waitDuration)
		}
	}

	// 记录本次请求的开始时间，作为下次计算的基准
	rl.lastRequest = timeutil.Now()
}

// getNextDelay 获取下一次请求的等待间隔（含自适应调整）
func (rl *RateLimiter) getNextDelay() time.Duration {
	delay := GetRandomDelay(rl.minInterval, rl.maxInterval)

	// 自适应：根据失败次数动态增加间隔
	fc := atomic.LoadInt64(&rl.failureCount)
	if fc > 0 {
		multiplier := 1.0 + float64(fc)*0.3
		if multiplier > 2.0 {
			multiplier = 2.0
		}
		delay = time.Duration(float64(delay) * multiplier)
	}

	return delay
}

// RecordSuccess 记录成功请求（降低失败计数）
func (rl *RateLimiter) RecordSuccess() {
	fc := atomic.LoadInt64(&rl.failureCount)
	if fc > 0 {
		atomic.AddInt64(&rl.failureCount, -1)
	}
}

// RecordFailure 记录失败请求（增加失败计数）
func (rl *RateLimiter) RecordFailure() {
	fc := atomic.AddInt64(&rl.failureCount, 1)
	if fc > 10 {
		atomic.StoreInt64(&rl.failureCount, 10)
	}
}

// GetCurrentDelay 获取当前延迟时间（用于日志）
func (rl *RateLimiter) GetCurrentDelay() time.Duration {
	return rl.getNextDelay()
}

// GetRandomDelay 获取随机延迟时间
func GetRandomDelay(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	return min + time.Duration(rand.Int63n(int64(max-min)))
}

// GetExponentialBackoff 获取指数退避延迟
func GetExponentialBackoff(attempt int, baseDelay time.Duration) time.Duration {
	if attempt <= 0 {
		return baseDelay
	}
	// 2^attempt * baseDelay，最大不超过2分钟（降低最大延迟）
	delay := baseDelay * time.Duration(1<<uint(attempt))
	maxDelay := 2 * time.Minute
	if delay > maxDelay {
		delay = maxDelay
	}
	// 添加随机抖动（±20%）
	jitter := time.Duration(rand.Int63n(int64(delay) / 5))
	if rand.Intn(2) == 0 {
		delay += jitter
	} else {
		delay -= jitter
	}
	return delay
}
