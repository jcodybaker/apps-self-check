package storer

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/digitalocean/apps-self-check/pkg/types/check"
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

func (c *commonStorer) wgAdd(i int) {
	c.wg.Add(i)
}

func (c *commonStorer) wgDone() {
	c.wg.Done()
}

func (c *commonStorer) wgWait() {
	c.wg.Wait()
}

func (c *commonStorer) doneChan() <-chan struct{} {
	return c.done
}

// asyncSaveCheckResults saves check results asynchronously with retries on failure.
func asyncSaveCheckResults(ctx context.Context, s storer, result check.CheckResults, attemptSchedule []time.Duration) {
	s.wgAdd(1)
	ll := log.Ctx(ctx)
	go func() {
		defer s.wgDone()
		var err error
		for i, delay := range attemptSchedule {
			var dyingBreath bool
			if delay > 0 {
				t := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					dyingBreath = true
				case <-t.C:
				case <-s.doneChan():
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
			err = s.SaveCheckResults(ctx, result)
			if err == nil {
				return
			}
			if i+1 < len(attemptSchedule) {
				ll.Warn().Err(err).Msg("saving results to database asynchronously")
			}
			result.Errors = append(result.Errors, check.CheckError{
				Check: "result_save_attempt_" + strconv.Itoa(i+1),
				Error: err.Error(),
			})
			if dyingBreath || ctx.Err() != nil {
				return
			}
		}
		ll.Error().Err(err).Msg("final attempt: saving results to database asynchronously")
	}()
}
