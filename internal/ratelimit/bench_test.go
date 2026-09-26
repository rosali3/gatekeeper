package ratelimit

import (
	"context"
	"testing"
)

func BenchmarkLocal_Allow(b *testing.B) {
	l := NewLocal(RealClock{}, Config{RPS: 1e9, Burst: 1_000_000}) // effectively never denies, isolating Allow's own cost
	ctx := context.Background()

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = l.Allow(ctx, "bench-key")
	}
}

func BenchmarkLocal_Allow_ManyKeys(b *testing.B) {
	l := NewLocal(RealClock{}, Config{RPS: 1e9, Burst: 1_000_000})
	ctx := context.Background()
	keys := []string{"key-a", "key-b", "key-c", "key-d", "key-e", "key-f", "key-g", "key-h"}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = l.Allow(ctx, keys[i%len(keys)])
	}
}

func BenchmarkLocal_Allow_Parallel(b *testing.B) {
	l := NewLocal(RealClock{}, Config{RPS: 1e9, Burst: 1_000_000})
	ctx := context.Background()

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = l.Allow(ctx, "bench-key")
		}
	})
}
