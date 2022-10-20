package main

import "time"

type CheckError struct {
	Check string
	Error error
}

type CheckResults struct {
	TS           time.Time
	AppID        string
	Labels       map[string]string
	Errors       []CheckError
	Measurements []CheckMeasurement
}

type CheckMeasurement struct {
	Check string
	Value float64
}
