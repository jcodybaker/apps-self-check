package storer

import (
	"context"
	"time"

	"github.com/digitalocean/apps-self-check/pkg/types/check"
)

// Storer instances store stuff.
type Storer interface {
	// SaveCheckResults saves check results.
	SaveCheckResults(ctx context.Context, result check.CheckResults) (err error)

	// AsyncQueryRetry saves check results asynchronously with retries on failure.
	AsyncQueryRetry(
		ctx context.Context,
		attemptSchedule []time.Duration,
		f func(ctx context.Context, attempt int) error,
	)
	UpdateInstance(ctx context.Context, instance *check.Instance) error

	// Close triggers any asynchronous saves to immediately make a final attempt, waits briefly
	// for their completion, and closes database connections..
	Close() error

	AnalyzeLongestGapPerApp(ctx context.Context, start, end time.Time, apps []string, output func(appID string, interval time.Duration, maxIntervalTS time.Time)) error
}
