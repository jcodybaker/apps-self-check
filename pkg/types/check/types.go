package check

import (
	"context"
	"time"
)

type CheckError struct {
	Check string
	Error string
}

type CheckResults struct {
	TS           time.Time
	AppID        string
	Hostname     string
	Labels       map[string]string
	Errors       []CheckError
	Measurements []CheckMeasurement
}

type CheckMeasurement struct {
	Check string
	Value float64
}

// Check describes a function which validates this app.
type Check func(context.Context) ([]CheckMeasurement, error)
