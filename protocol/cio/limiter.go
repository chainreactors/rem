package cio

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/chainreactors/rem/protocol/core"
)

// TokenBucketLimiter 基于令牌桶数量的限速器（手动恢复）
type TokenBucketLimiter struct {
	tokens     int64        // 当前令牌数量
	enabled    atomic.Value // 限速开关
	mu         sync.Mutex   // 保护令牌操作
	cond       *sync.Cond   // 令牌到达通知
	readCount  int64        // 读取计数器
	writeCount int64        // 写入计数器
}

// NewTokenBucketLimiter 创建新的令牌桶限速器
func NewTokenBucketLimiter(maxTokens int64, enable bool) *TokenBucketLimiter {
	l := &TokenBucketLimiter{
		tokens: maxTokens + int64(core.MaxPacketSize),
	}
	l.cond = sync.NewCond(&l.mu)
	l.enabled.Store(enable)
	return l
}

// TryConsume 尝试消费指定数量的令牌（调用者必须持有 mu）
func (l *TokenBucketLimiter) tryConsumeLocked(tokens int64) bool {
	if l.tokens >= tokens {
		l.tokens -= tokens
		return true
	}
	return false
}

// TryConsume 尝试消费指定数量的令牌
func (l *TokenBucketLimiter) TryConsume(tokens int64) bool {
	if !l.IsEnabled() {
		return true
	}
	l.mu.Lock()
	ok := l.tryConsumeLocked(tokens)
	l.mu.Unlock()
	return ok
}

// WaitForTokens 等待获取指定数量的令牌
func (l *TokenBucketLimiter) WaitForTokens(ctx context.Context, tokens int64) error {
	if !l.IsEnabled() {
		return nil
	}

	l.mu.Lock()
	for !l.tryConsumeLocked(tokens) {
		// Check context before waiting
		select {
		case <-ctx.Done():
			l.mu.Unlock()
			return ctx.Err()
		default:
		}
		l.cond.Wait()
	}
	l.mu.Unlock()
	return nil
}

// AddTokens 手动添加令牌
func (l *TokenBucketLimiter) AddTokens(tokens int64) {
	l.mu.Lock()
	l.tokens += tokens
	l.mu.Unlock()
	l.cond.Broadcast()
}

// SetTokens 手动设置令牌数量
func (l *TokenBucketLimiter) SetTokens(tokens int64) {
	l.mu.Lock()
	if tokens < 0 {
		tokens = 0
	}
	l.tokens = tokens
	l.mu.Unlock()
	l.cond.Broadcast()
}

// Enable 启用/禁用限速
func (l *TokenBucketLimiter) Enable(enable bool) {
	l.enabled.Store(enable)
}

// IsEnabled 检查限速是否启用
func (l *TokenBucketLimiter) IsEnabled() bool {
	if val := l.enabled.Load(); val != nil {
		return val.(bool)
	}
	return false
}

// GetCounts 获取读写计数
func (l *TokenBucketLimiter) GetCounts() (readCount, writeCount int64) {
	return atomic.LoadInt64(&l.readCount), atomic.LoadInt64(&l.writeCount)
}

// GetTokens 获取当前令牌数量
func (l *TokenBucketLimiter) GetTokens() int64 {
	return l.tokens
}
