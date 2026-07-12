package scanner

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiskWatcher_AcquireRelease(t *testing.T) {
	dw := NewDiskWatcher(1024 * 1024) // 1MB budget

	// Acquire should succeed when under budget
	reserved, err := dw.Acquire(context.Background(), 512*1024) // 512KB
	require.NoError(t, err)
	assert.Equal(t, int64(512*1024), reserved)
	assert.Equal(t, int64(512*1024), dw.Current())

	// Release should free space
	dw.Release(reserved)
	assert.Equal(t, int64(0), dw.Current())
}

func TestDiskWatcher_AcquireZeroSize_DefaultReservation(t *testing.T) {
	dw := NewDiskWatcher(100 * 1024 * 1024) // 100MB budget

	// sizeHint=0 should use default reservation (budget/20 = 5MB)
	reserved, err := dw.Acquire(context.Background(), 0)
	require.NoError(t, err)
	assert.Equal(t, int64(5*1024*1024), reserved)
	assert.Equal(t, int64(5*1024*1024), dw.Current())

	dw.Release(reserved)
	assert.Equal(t, int64(0), dw.Current())
}

func TestDiskWatcher_BudgetExceeded_BlocksThenUnblocks(t *testing.T) {
	dw := NewDiskWatcher(1024 * 1024) // 1MB budget

	// Fill the budget
	_, err := dw.Acquire(context.Background(), 1024*1024)
	require.NoError(t, err)

	// Next acquire should block
	acquired := make(chan int64, 1)
	errCh := make(chan error, 1)
	go func() {
		r, e := dw.Acquire(context.Background(), 512*1024)
		if e != nil {
			errCh <- e
		} else {
			acquired <- r
		}
	}()

	// Should not acquire immediately
	select {
	case <-acquired:
		t.Fatal("Acquire should block when budget exceeded")
	case <-errCh:
		t.Fatal("Acquire should block, not error")
	case <-time.After(100 * time.Millisecond):
	}

	// Release space — should unblock the waiting Acquire
	dw.Release(1024 * 1024)

	select {
	case r := <-acquired:
		assert.Equal(t, int64(512*1024), r)
	case err := <-errCh:
		t.Fatalf("Acquire should succeed after Release, got error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire should unblock after Release")
	}
}

func TestDiskWatcher_ContextCancel(t *testing.T) {
	dw := NewDiskWatcher(1024 * 1024) // 1MB budget

	// Fill the budget
	_, err := dw.Acquire(context.Background(), 1024*1024)
	require.NoError(t, err)

	// Acquire with cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, e := dw.Acquire(ctx, 512*1024)
		errCh <- e
	}()

	// Cancel the context
	cancel()

	select {
	case err := <-errCh:
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cancelled")
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire should return error on context cancel")
	}

	// Clean up
	dw.Release(1024 * 1024)
}

func TestDiskWatcher_UnlimitedBudget(t *testing.T) {
	dw := NewDiskWatcher(0) // unlimited

	// Should never block
	reserved, err := dw.Acquire(context.Background(), 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), reserved)

	// Release should be no-op
	dw.Release(999)
	assert.Equal(t, int64(0), dw.Current()) // stays 0 because unlimited mode doesn't track
}

func TestDiskWatcher_Update(t *testing.T) {
	dw := NewDiskWatcher(100 * 1024 * 1024) // 100MB budget (default reservation = 5MB)

	// Acquire with default reservation (budget/20 = 5MB)
	reserved, err := dw.Acquire(context.Background(), 0)
	require.NoError(t, err)
	assert.Equal(t, int64(5*1024*1024), reserved)
	assert.Equal(t, int64(5*1024*1024), dw.Current())

	// Update to actual file size (2MB)
	dw.Update(reserved, 2*1024*1024)
	assert.Equal(t, int64(2*1024*1024), dw.Current())

	// Release actual size
	dw.Release(2 * 1024 * 1024)
	assert.Equal(t, int64(0), dw.Current())
}

func TestDiskWatcher_ConcurrentAcquireRelease(t *testing.T) {
	dw := NewDiskWatcher(5 * 1024 * 1024) // 5MB budget

	var acquired int64
	ctx := context.Background()

	// 10 goroutines each try to acquire 1MB
	done := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_, err := dw.Acquire(ctx, 1024*1024) // 1MB each
			if err == nil {
				atomic.AddInt64(&acquired, 1)
				time.Sleep(50 * time.Millisecond) // simulate work
				dw.Release(1024 * 1024)
			}
		}()
	}

	// All should complete (some will block then unblock)
	for i := 0; i < 10; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Concurrent acquire/release took too long")
		}
	}

	// All should have eventually acquired
	assert.Equal(t, int64(10), atomic.LoadInt64(&acquired))
	assert.Equal(t, int64(0), dw.Current()) // all released
}

func TestDiskWatcher_Budget(t *testing.T) {
	dw := NewDiskWatcher(42)
	assert.Equal(t, int64(42), dw.Budget())
}

func TestDiskWatcher_Update_UnlimitedBudget(t *testing.T) {
	// budget<=0 → Update 直接返回，不修改 current
	dw := NewDiskWatcher(0)
	dw.Update(100, 200)
	assert.Equal(t, int64(0), dw.Current(), "unlimited budget → Update no-op")
}

func TestDiskWatcher_Update_NegativeClamped(t *testing.T) {
	// oldSize > 当前 current → current 变负 → 钳为 0
	dw := NewDiskWatcher(100 * 1024 * 1024)
	dw.Acquire(context.Background(), 1*1024*1024) // current=1MB
	// Update 释放 5MB（比 current 大）→ current=-4MB → 钳为 0
	dw.Update(5*1024*1024, 0)
	assert.Equal(t, int64(0), dw.Current())
}

func TestDiskWatcher_Release_MoreThanCurrent(t *testing.T) {
	// Release 量 > current → current 变负 → 钳为 0（line 87-89）
	dw := NewDiskWatcher(10 * 1024 * 1024) // 10MB budget
	dw.Acquire(context.Background(), 1*1024*1024) // current=1MB
	// Release 5MB（比 current 1MB 大）→ current=-4MB → 钳为 0
	dw.Release(5 * 1024 * 1024)
	assert.Equal(t, int64(0), dw.Current())
}

func TestDiskWatcher_Release_NotifyBufferFull(t *testing.T) {
	// Release 的 select 在 notify channel 满（64 个未消费信号）时走 default 分支
	// （line 97）。注释说正常负载下不会发生（Acquire 会消费），这里手动灌满
	// notify 触发 default，验证安全降级（不阻塞、不 panic）。
	dw := NewDiskWatcher(10 * 1024 * 1024)
	dw.Acquire(context.Background(), 1*1024*1024)
	// 灌满 notify（64 slots），无 Acquire 在等 → 信号堆积
	for i := 0; i < 64; i++ {
		dw.notify <- struct{}{}
	}
	// 再 Release → select 走 default（97）而非 case（96）
	dw.Release(1 * 1024 * 1024)
	assert.Equal(t, int64(0), dw.Current())
}
