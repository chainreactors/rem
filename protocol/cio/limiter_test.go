package cio

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// BenchmarkLimiter_NoContention: tokens always available, measures lock overhead.
func BenchmarkLimiter_NoContention(b *testing.B) {
	l := NewTokenBucketLimiter(1<<40, true)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.WaitForTokens(ctx, 1)
	}
}

// BenchmarkLimiter_Disabled: limiter disabled, measures fast-path overhead.
func BenchmarkLimiter_Disabled(b *testing.B) {
	l := NewTokenBucketLimiter(1<<20, false)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.WaitForTokens(ctx, 1024)
	}
}

// BenchmarkLimiter_Contention: N consumers + 1 refiller, measures wake-up latency.
func BenchmarkLimiter_Contention(b *testing.B) {
	for _, consumers := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("c=%d", consumers), func(b *testing.B) {
			l := NewTokenBucketLimiter(0, true)
			ctx := context.Background()
			var wg sync.WaitGroup

			perConsumer := b.N / consumers
			if perConsumer == 0 {
				perConsumer = 1
			}
			total := perConsumer * consumers

			// Pre-fill enough tokens for all operations
			l.AddTokens(int64(total) * 1024)

			b.ResetTimer()
			for i := 0; i < consumers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for j := 0; j < perConsumer; j++ {
						l.WaitForTokens(ctx, 1024)
					}
				}()
			}
			wg.Wait()
		})
	}
}
