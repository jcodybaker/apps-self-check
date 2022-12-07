package storer

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/digitalocean/apps-self-check/pkg/types/check"
)

// LogStorer writes to logs instead of storer.
type LogStorer struct {
	w io.Writer
	commonStorer
}

// NewLogStorer returns a thin storer which logs JSON output to the provided writer.
func NewLogStorer(w io.Writer) Storer {
	l := &LogStorer{
		w: w,
	}
	l.commonStorer.init()
	return l
}

// SaveCheckResults saves check results.
func (l *LogStorer) SaveCheckResults(ctx context.Context, result check.CheckResults) (err error) {
	j := json.NewEncoder(l.w)
	j.SetIndent("  ", "  ")
	return j.Encode(result)
}

// AsyncSaveCheckResults saves check results asynchronously with retries on failure.
func (l *LogStorer) AsyncSaveCheckResults(ctx context.Context, result check.CheckResults, attemptSchedule []time.Duration) {
	asyncSaveCheckResults(ctx, l, result, attemptSchedule)
}

// Close triggers any asynchronous saves to immediately make a final attempt, waits briefly
// for their completion, and closes database connections..
func (l *LogStorer) Close() error {
	return nil
}

// AnalyzeLongestGapPerApp looks
func (l *LogStorer) AnalyzeLongestGapPerApp(
	ctx context.Context,
	start time.Time,
	end time.Time,
	apps []string,
	output func(appID string, interval time.Duration, maxIntervalTS time.Time),
) error {
	return nil
}
