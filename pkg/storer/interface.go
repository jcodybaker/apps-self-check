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

	// AsyncSaveCheckResults saves check results asynchronously with retries on failure.
	AsyncSaveCheckResults(ctx context.Context, result check.CheckResults, attemptSchedule []time.Duration)

	// Close triggers any asynchronous saves to immediately make a final attempt, waits briefly
	// for their completion, and closes database connections..
	Close() error

	AnalyzeLongestGapPerApp(ctx context.Context, start, end time.Time, output func(appID string, interval time.Duration, maxIntervalTS time.Time)) error
}
