package s3cli

import (
	"context"
	"sync"
)

// pool runs transfers with a bounded number of goroutines. It exists because
// a recursive transfer of a thousand small objects is almost entirely round-trip
// latency: serially it takes a thousand round trips end to end, and with even
// four workers a quarter of that. s5cmd's headline speed is this and little
// else.
//
// The first failure cancels the pool's context so the rest stop pushing bytes at
// a store that is already refusing, and is what wait returns. Later failures are
// dropped: they are usually the cancellation, and a page of identical "context
// canceled" lines buries the one error that mattered.
type pool struct {
	slots  chan struct{}
	wg     sync.WaitGroup
	cancel context.CancelFunc

	mu  sync.Mutex
	err error
}

// newPool returns a pool of n workers and the context its work must use.
func newPool(ctx context.Context, n int) (*pool, context.Context) {
	if n < 1 {
		n = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	return &pool{slots: make(chan struct{}, n), cancel: cancel}, ctx
}

// run starts fn in the background once a worker slot frees up. It reports false
// when the pool has already failed or the context is done, which is the signal
// for the caller's producer loop to stop.
func (p *pool) run(ctx context.Context, fn func() error) bool {
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		p.fail(ctx.Err())
		return false
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() { <-p.slots }()
		if err := fn(); err != nil {
			p.fail(err)
		}
	}()
	return true
}

// fail records the first error and stops the others.
func (p *pool) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err == nil {
		p.err = err
		p.cancel()
	}
}

// wait blocks until every started worker has finished and returns the first
// failure. It must be called exactly once, and the pool's cancel is released
// with it.
func (p *pool) wait() error {
	p.wg.Wait()
	p.cancel()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// syncWriter serialises the per-object progress lines a pool's workers emit, so
// two transfers finishing at once cannot interleave half a line each.
//
// Lines therefore appear in completion order, which with more than one worker is
// not the listing order — a caller that needs a stable order must sort, and the
// tests do.
type syncWriter struct {
	mu sync.Mutex
	w  interface{ Write([]byte) (int, error) }
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
