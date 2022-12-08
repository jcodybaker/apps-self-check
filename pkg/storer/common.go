package storer

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// dyingBreathExpiration defines a grace period after the ctx has been canceled, when async
	// saves will be attempted before returning.
	dyingBreathExpiration = 1 * time.Second
)

var (
	SaveBackOffSchedule = []time.Duration{
		0,
		10 * time.Millisecond,
		100 * time.Millisecond,
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
	}
)

// commonStorer provides foundational bits used by all storer implementations.
type commonStorer struct {
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

func (c *commonStorer) init() {
	c.done = make(chan struct{})
}

// AsyncQueryRetry saves check results asynchronously with retries on failure.
func (c *commonStorer) AsyncQueryRetry(
	ctx context.Context,
	attemptSchedule []time.Duration,
	f func(ctx context.Context, attempt int) error,
) {
	c.wg.Add(1)
	ll := log.Ctx(ctx)
	go func() {
		defer c.wg.Done()
		var err error
		for i, delay := range attemptSchedule {
			var dyingBreath bool
			attempt := i + 1
			if delay > 0 {
				t := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					dyingBreath = true
				case <-t.C:
				case <-c.done:
					dyingBreath = true
				}
			}
			if dyingBreath || ctx.Err() != nil {
				// We're shutting down, but haven't successfully saved yet. Make a hail mary
				// attempt with a fresh (but short-lived) ctx.
				var cancel func()
				ctx, cancel = context.WithTimeout(context.Background(), dyingBreathExpiration)
				defer cancel()
			}
			if err = f(ctx, attempt); err == nil {
				return
			}
			if attempt < len(attemptSchedule) {
				ll.Warn().Err(err).Int("attempt", attempt).Msg("performing async database action")
			}
			if dyingBreath || ctx.Err() != nil {
				return
			}
		}
		ll.Error().Err(err).
			Int("attempt", len(attemptSchedule)).
			Msg("final attempt: performing async database action")
	}()
}
