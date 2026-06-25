package transfer

import (
	"context"
	"testing"
	"time"
)

func TestLimiterNilNoBlock(t *testing.T) {
	var l *limiter // nil = без лимита
	start := time.Now()
	if err := l.wait(context.Background(), 1<<20); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("nil-лимитер не должен блокировать")
	}
}

func TestLimiterThrottles(t *testing.T) {
	// rate=10000 Б/с; ёмкость ведра = max(burst, rate) = 10000.
	l := newLimiter(10000, 1000)
	ctx := context.Background()
	_ = l.wait(ctx, 10000) // осушаем полное ведро
	start := time.Now()
	if err := l.wait(ctx, 5000); err != nil { // 5000 Б при 10000 Б/с ≈ 0.5с
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 300*time.Millisecond {
		t.Fatalf("лимитер не тормозит: прошло %v (ожидалось ≈0.5с)", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("лимитер слишком медленный: %v", elapsed)
	}
}

func TestLimiterContextCancel(t *testing.T) {
	l := newLimiter(1, 1) // очень медленно
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := l.wait(ctx, 1_000_000); err == nil {
		t.Fatal("ожидалась отмена по контексту")
	}
}
