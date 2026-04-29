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
	"sync/atomic"
	"time"

	"k8s.io/klog/v2"
)

// connPool manages a pool of idle TCP connections to a target server.
// It provides Get/Put semantics with health checking and idle timeout cleanup.
type connPool struct {
	// target address to dial, updated atomically on reconfig
	target atomic.Value // string

	// idle connections protected by mu
	mu   sync.Mutex
	idle []*idleConn

	// configuration
	maxIdle  int
	idleTTL  time.Duration
	dialer   *net.Dialer
	onDial   func(ctx context.Context, network, addr string) (net.Conn, error)

	// metrics
	totalDials   atomic.Int64
	totalBorrows  atomic.Int64
	totalReturns  atomic.Int64
	totalDiscards atomic.Int64

	// lifecycle
	closed atomic.Bool
	stopCh chan struct{}
}

// idleConn wraps a connection with its idle timestamp
type idleConn struct {
	conn      net.Conn
	idleSince time.Time
}

// PoolConfig holds configuration for the connection pool
type PoolConfig struct {
	// MaxIdleConnections is the maximum number of idle connections to keep
	MaxIdleConnections int
	// IdleTimeout is how long a connection can sit idle before being closed
	IdleTimeout time.Duration
	// DialTimeout is the timeout for creating new connections
	DialTimeout time.Duration
	// CleanInterval is how often the background cleaner runs
	CleanInterval time.Duration
}

// DefaultPoolConfig provides sensible defaults
var DefaultPoolConfig = PoolConfig{
	MaxIdleConnections: 128,
	IdleTimeout:        3 * time.Second,  // below uvicorn's default 5s keep-alive timeout
	DialTimeout:        10 * time.Second,
	CleanInterval:      1 * time.Second,  // cleaner runs frequently enough to beat uvicorn's 5s timeout
}

// newConnPool creates a new connection pool
func newConnPool(cfg PoolConfig) *connPool {
	p := &connPool{
		maxIdle: cfg.MaxIdleConnections,
		idleTTL: cfg.IdleTimeout,
		dialer: &net.Dialer{
			Timeout: cfg.DialTimeout,
		},
		stopCh: make(chan struct{}),
	}
	p.target.Store("")
	return p
}

// SetTarget updates the target address and drains existing connections.
// Returns the old target for logging purposes.
func (p *connPool) SetTarget(target string) string {
	old := p.target.Swap(target).(string)
	p.drain()
	return old
}

// Target returns the current target address
func (p *connPool) Target() string {
	return p.target.Load().(string)
}

// Get retrieves a connection from the pool or dials a new one.
// Returns nil if the pool is closed or has no target set.
func (p *connPool) Get(ctx context.Context) (net.Conn, error) {
	if p.closed.Load() {
		return nil, context.Canceled
	}

	target := p.Target()
	if target == "" {
		return nil, nil
	}

	// Try to get an idle connection
	if conn := p.getIdle(); conn != nil {
		p.totalBorrows.Add(1)
		return conn, nil
	}

	// Dial a new connection
	var newConn net.Conn
	var err error
	if p.onDial != nil {
		newConn, err = p.onDial(ctx, "tcp", target)
	} else {
		newConn, err = p.dialer.DialContext(ctx, "tcp", target)
	}
	if err != nil {
		return nil, err
	}

	p.totalDials.Add(1)
	p.totalBorrows.Add(1)
	return newConn, nil
}

// Put returns a connection to the pool for reuse.
// If the pool is full, closed, or the connection is unhealthy, it is closed.
func (p *connPool) Put(conn net.Conn) {
	p.totalReturns.Add(1)

	if p.closed.Load() || conn == nil {
		if conn != nil {
			_ = conn.Close()
			p.totalDiscards.Add(1)
		}
		return
	}

	// Check if connection is still alive
	if !p.isAlive(conn) {
		_ = conn.Close()
		p.totalDiscards.Add(1)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.idle) >= p.maxIdle {
		_ = conn.Close()
		p.totalDiscards.Add(1)
		return
	}

	p.idle = append(p.idle, &idleConn{conn: conn, idleSince: time.Now()})
}

// drain closes all idle connections and resets the pool.
// Must be called with the target lock held (which SetTarget does).
func (p *connPool) drain() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, ic := range p.idle {
		_ = ic.conn.Close()
		p.totalDiscards.Add(1)
	}
	p.idle = p.idle[:0]
}

// Close shuts down the pool and closes all idle connections.
func (p *connPool) Close() {
	if !p.closed.CompareAndSwap(false, true) {
		return
	}
	close(p.stopCh)
	p.drain()
}

// StartCleaner starts a background goroutine that periodically removes
// idle connections that have exceeded the idle timeout.
func (p *connPool) StartCleaner(ctx context.Context, logger klog.Logger) {
	go func() {
		ticker := time.NewTicker(p.idleTTL / 2)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				p.cleanIdle()
			case <-p.stopCh:
				logger.V(4).Info("connection pool cleaner stopped")
				return
			case <-ctx.Done():
				logger.V(4).Info("connection pool cleaner stopped by context")
				return
			}
		}
	}()
	logger.Info("connection pool cleaner started", "interval", p.idleTTL/2, "idleTTL", p.idleTTL)
}

// cleanIdle removes connections that have been idle longer than idleTTL.
func (p *connPool) cleanIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-p.idleTTL)

	// Filter in place: keep only connections newer than cutoff
	valid := p.idle[:0]
	discarded := 0
	for _, ic := range p.idle {
		if ic.idleSince.After(cutoff) {
			valid = append(valid, ic)
		} else {
			_ = ic.conn.Close()
			discarded++
			p.totalDiscards.Add(1)
		}
	}
	p.idle = valid

	if discarded > 0 {
		klog.V(4).Infof("connection pool cleaned %d idle connections (kept %d)", discarded, len(valid))
	}
}

// getIdle pops a connection from the idle list with health checking.
func (p *connPool) getIdle() net.Conn {
	p.mu.Lock()
	defer p.mu.Unlock()

	for len(p.idle) > 0 {
		ic := p.idle[len(p.idle)-1]
		p.idle = p.idle[:len(p.idle)-1]

		if !p.isAlive(ic.conn) {
			_ = ic.conn.Close()
			p.totalDiscards.Add(1)
			continue
		}
		return ic.conn
	}
	return nil
}

// isAlive checks if a TCP connection is still usable.
// It uses a short read deadline to avoid blocking on dead connections.
func (p *connPool) isAlive(conn net.Conn) bool {
	// Check if the connection has been closed by the remote side
	// by attempting a non-blocking read with a very short timeout.
	// If there's an error (other than timeout/EAGAIN), the connection is dead.
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		// For non-TCP connections, assume alive
		return true
	}

	// Use SetReadDeadline to detect dead connections quickly
	deadline := time.Now().Add(100 * time.Millisecond)
	if err := tcpConn.SetReadDeadline(deadline); err != nil {
		return false
	}

	var buf [1]byte
	_, err := tcpConn.Read(buf[:])
	// Reset deadline
	_ = tcpConn.SetReadDeadline(time.Time{})

	if err == nil {
		// Got data — connection is alive, but we consumed a byte.
		// This is unlikely during idle health check but handle gracefully.
		return true
	}

	// Timeout or EAGAIN means connection is alive but no data
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return true
	}

	// Other errors (EOF, connection reset) mean connection is dead
	return false
}

// Stats returns current pool statistics for monitoring
func (p *connPool) Stats() PoolStats {
	p.mu.Lock()
	idleCount := len(p.idle)
	p.mu.Unlock()

	return PoolStats{
		Target:        p.Target(),
		IdleCount:     idleCount,
		MaxIdle:       p.maxIdle,
		TotalDials:    p.totalDials.Load(),
		TotalBorrows:  p.totalBorrows.Load(),
		TotalReturns:  p.totalReturns.Load(),
		TotalDiscards: p.totalDiscards.Load(),
		Closed:        p.closed.Load(),
	}
}

// PoolStats holds pool statistics
type PoolStats struct {
	Target        string
	IdleCount     int
	MaxIdle       int
	TotalDials    int64
	TotalBorrows  int64
	TotalReturns  int64
	TotalDiscards int64
	Closed        bool
}
