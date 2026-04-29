/*
Copyright 2026 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxy

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"k8s.io/klog/v2"
)

// startEchoServer starts a TCP listener that echoes back any data received.
func startEchoServer(t *testing.T) (addr string, closer func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start echo server: %v", err)
	}
	addr = ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if err != nil {
						_ = c.Close()
						return
					}
					_, _ = c.Write(buf[:n])
				}
			}(conn)
		}
	}()

	return addr, func() { _ = ln.Close() }
}

func TestConnPool_GetDialsNewWhenEmpty(t *testing.T) {
	addr, closer := startEchoServer(t)
	defer closer()

	pool := newConnPool(PoolConfig{
		MaxIdleConnections: 10,
		IdleTimeout:        1 * time.Second,
		DialTimeout:        1 * time.Second,
		CleanInterval:      500 * time.Millisecond,
	})
	defer pool.Close()
	pool.SetTarget(addr)

	conn, err := pool.Get(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn == nil {
		t.Fatal("expected non-nil connection")
	}
	defer func() { _ = conn.Close() }()

	stats := pool.Stats()
	if stats.TotalDials != 1 {
		t.Errorf("expected 1 dial, got %d", stats.TotalDials)
	}
	if stats.TotalBorrows != 1 {
		t.Errorf("expected 1 borrow, got %d", stats.TotalBorrows)
	}
}

func TestConnPool_PutAndGetReuse(t *testing.T) {
	addr, closer := startEchoServer(t)
	defer closer()

	pool := newConnPool(PoolConfig{
		MaxIdleConnections: 10,
		IdleTimeout:        10 * time.Second,
		DialTimeout:        1 * time.Second,
		CleanInterval:      500 * time.Millisecond,
	})
	defer pool.Close()
	pool.SetTarget(addr)

	// First borrow (dials new)
	conn1, err := pool.Get(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Return to pool
	pool.Put(conn1)

	// Second borrow (should reuse idle connection)
	conn2, err := pool.Get(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be the same underlying connection
	if conn1 != conn2 {
		t.Errorf("expected the same connection to be reused, got different connections")
	}

	stats := pool.Stats()
	if stats.TotalDials != 1 {
		t.Errorf("expected 1 dial (reused from pool), got %d", stats.TotalDials)
	}
	if stats.TotalBorrows != 2 {
		t.Errorf("expected 2 borrows, got %d", stats.TotalBorrows)
	}
	if stats.TotalReturns != 1 {
		t.Errorf("expected 1 return, got %d", stats.TotalReturns)
	}
}

func TestConnPool_MaxIdleLimitDiscard(t *testing.T) {
	addr, closer := startEchoServer(t)
	defer closer()

	maxIdle := 2
	pool := newConnPool(PoolConfig{
		MaxIdleConnections: maxIdle,
		IdleTimeout:        10 * time.Second,
		DialTimeout:        1 * time.Second,
		CleanInterval:      500 * time.Millisecond,
	})
	defer pool.Close()
	pool.SetTarget(addr)

	// Borrow maxIdle+1 connections simultaneously so we have more than
	// maxIdle connections to return at once
	var conns []net.Conn
	for i := 0; i < maxIdle+1; i++ {
		conn, err := pool.Get(context.Background())
		if err != nil {
			t.Fatalf("unexpected error on borrow %d: %v", i, err)
		}
		conns = append(conns, conn)
	}

	// Return all of them — only maxIdle should be kept, rest discarded
	for _, conn := range conns {
		pool.Put(conn)
	}

	stats := pool.Stats()
	if stats.IdleCount != maxIdle {
		t.Errorf("expected %d idle connections (maxIdle limit), got %d", maxIdle, stats.IdleCount)
	}
	if stats.TotalDiscards == 0 {
		t.Errorf("expected some discards due to pool size limit, got 0")
	}
}

func TestConnPool_DrainOnTargetChange(t *testing.T) {
	addr1, closer1 := startEchoServer(t)
	defer closer1()
	addr2, closer2 := startEchoServer(t)
	defer closer2()

	pool := newConnPool(PoolConfig{
		MaxIdleConnections: 10,
		IdleTimeout:        10 * time.Second,
		DialTimeout:        1 * time.Second,
		CleanInterval:      500 * time.Millisecond,
	})
	defer pool.Close()
	pool.SetTarget(addr1)

	// Borrow and return a connection to addr1
	conn, err := pool.Get(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pool.Put(conn)

	// Change target — should drain idle connections
	old := pool.SetTarget(addr2)
	if old != addr1 {
		t.Errorf("expected old target %q, got %q", addr1, old)
	}

	stats := pool.Stats()
	if stats.IdleCount != 0 {
		t.Errorf("expected 0 idle connections after target change, got %d", stats.IdleCount)
	}
}

func TestConnPool_GetReturnsNilWhenNoTarget(t *testing.T) {
	pool := newConnPool(PoolConfig{
		MaxIdleConnections: 10,
		IdleTimeout:        10 * time.Second,
		DialTimeout:        1 * time.Second,
		CleanInterval:      500 * time.Millisecond,
	})
	defer pool.Close()

	conn, err := pool.Get(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn != nil {
		t.Error("expected nil connection when no target is set")
	}
}

func TestConnPool_GetFailsAfterClose(t *testing.T) {
	pool := newConnPool(PoolConfig{
		MaxIdleConnections: 10,
		IdleTimeout:        10 * time.Second,
		DialTimeout:        1 * time.Second,
		CleanInterval:      500 * time.Millisecond,
	})
	pool.SetTarget("127.0.0.1:9999")
	pool.Close()

	conn, err := pool.Get(context.Background())
	if err != context.Canceled {
		t.Errorf("expected context.Canceled error after close, got %v", err)
	}
	if conn != nil {
		t.Error("expected nil connection after close")
	}
}

func TestConnPool_ClosedIdleConnectionsDrained(t *testing.T) {
	addr, closer := startEchoServer(t)
	defer closer()

	pool := newConnPool(PoolConfig{
		MaxIdleConnections: 10,
		IdleTimeout:        10 * time.Second,
		DialTimeout:        1 * time.Second,
		CleanInterval:      500 * time.Millisecond,
	})
	pool.SetTarget(addr)

	conn, err := pool.Get(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pool.Put(conn)

	pool.Close()

	stats := pool.Stats()
	if stats.IdleCount != 0 {
		t.Errorf("expected 0 idle connections after close, got %d", stats.IdleCount)
	}
}

func TestConnPool_ConcurrentBorrowAndReturn(t *testing.T) {
	addr, closer := startEchoServer(t)
	defer closer()

	pool := newConnPool(PoolConfig{
		MaxIdleConnections: 128,
		IdleTimeout:        10 * time.Second,
		DialTimeout:        1 * time.Second,
		CleanInterval:      500 * time.Millisecond,
	})
	defer pool.Close()
	pool.SetTarget(addr)

	// Run concurrent borrows and returns
	var wg sync.WaitGroup
	const goroutines = 50
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := pool.Get(context.Background())
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			// Brief "work"
			time.Sleep(1 * time.Millisecond)
			pool.Put(conn)
		}()
	}
	wg.Wait()

	stats := pool.Stats()
	if stats.TotalBorrows != int64(goroutines) {
		t.Errorf("expected %d borrows, got %d", goroutines, stats.TotalBorrows)
	}
	if stats.TotalReturns != int64(goroutines) {
		t.Errorf("expected %d returns, got %d", goroutines, stats.TotalReturns)
	}
}

func TestConnPool_CleanerRemovesOldConnections(t *testing.T) {
	addr, closer := startEchoServer(t)
	defer closer()

	idleTTL := 100 * time.Millisecond
	pool := newConnPool(PoolConfig{
		MaxIdleConnections: 10,
		IdleTimeout:        idleTTL,
		DialTimeout:        1 * time.Second,
		CleanInterval:      idleTTL / 2, // cleaner runs every 50ms
	})
	defer pool.Close()
	pool.SetTarget(addr)

	ctx, cancel := context.WithCancel(context.Background())
	pool.StartCleaner(ctx, klog.FromContext(ctx).WithName("test-pool"))

	conn, err := pool.Get(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pool.Put(conn)

	// Wait for connection to become stale + for cleaner to remove it
	time.Sleep(idleTTL * 3)

	stats := pool.Stats()
	if stats.IdleCount != 0 {
		t.Errorf("expected 0 idle connections after cleaner ran, got %d", stats.IdleCount)
	}

	cancel()
}
