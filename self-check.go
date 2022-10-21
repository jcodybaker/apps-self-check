package main

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/iancoleman/strcase"
	"github.com/rs/zerolog/log"
	"github.com/xo/dburl"
)

// Check describes a function which validates this app.
type Check func(context.Context) ([]CheckMeasurement, error)

// NewChecker creates a checker.
func NewChecker(opts ...CheckerOption) *checker {
	c := &checker{
		now: time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// CheckerOption describe optional arguments for the checker.
type CheckerOption func(*checker)

// WithAppID sets the current app ID for the checker.
func WithAppID(appID string) CheckerOption {
	return func(c *checker) {
		c.appID = appID
	}
}

// WithLabels sets labels to be recorded with each check.
func WithLabels(l map[string]string) CheckerOption {
	return func(c *checker) {
		c.labels = l
	}
}

// WithCheck adds a Check function. If name is empty, we will attempt to determine one from the func.
func WithCheck(name string, f Check) CheckerOption {
	if name == "" {
		name = strings.TrimPrefix(runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name(), "github.com/digitalocean/apps-self-check")
	}
	return func(c *checker) {
		c.checks = append(c.checks, check{
			f:    f,
			name: strcase.ToSnake(name),
		})
	}
}

// WithStorer adds a storer to the checker.
func WithStorer(s Storer) CheckerOption {
	return func(c *checker) {
		c.storer = s
	}
}

type check struct {
	f    Check
	name string
}

type checker struct {
	now      func() time.Time
	appID    string
	hostname string
	storer   Storer
	labels   map[string]string
	checks   []check
}

// Run executes periodically until the ctx is cancelled.
func (c *checker) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	ll := log.Ctx(ctx)
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r := c.doChecks(ctx)
			if ctx.Err() != nil {
				return // abandon results if the ctx was canceled mid-check.
			}
			if err := c.storer.SaveCheckResults(ctx, r); err != nil {
				ll.Err(err).Msg("saving check results")
			}
		}
	}
}

func (c *checker) HTTPHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	results := c.doChecks(ctx)
	if err := c.storer.SaveCheckResults(ctx, results); err != nil {
		log.Ctx(ctx).Err(err).Msg("saving check results")
	}
	j := json.NewEncoder(w)
	j.SetIndent("  ", "  ")
	w.Header().Add("content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := j.Encode(results); err != nil {
		log.Ctx(ctx).Warn().Err(err).Msg("encoding spot check")
	}
}

func (c *checker) doChecks(ctx context.Context) CheckResults {
	r := CheckResults{
		TS:       c.now(),
		AppID:    c.appID,
		Labels:   c.labels,
		Hostname: c.hostname,
	}
	start := r.TS
	for _, ch := range c.checks {
		measurements, err := ch.f(ctx)
		if err != nil {
			r.Errors = append(r.Errors, CheckError{
				Check: ch.name,
				Error: err,
			})
			start = c.now()
			continue
		}
		r.Measurements = append(r.Measurements, measurements...)
		finish := c.now()
		r.Measurements = append(r.Measurements, CheckMeasurement{
			Check: ch.name + "_duration",
			Value: finish.Sub(start).Seconds(),
		})
		start = finish
	}
	return r
}

// NewDNSCheck adds a check which probes the specified hostname
func NewDNSCheck(hostname string) (Check, error) {
	if strings.Contains(hostname, "://") {
		// If it looks like a URL, we'll try to isolate the hostname.
		u, err := url.Parse(hostname)
		if err != nil {
			return nil, fmt.Errorf("parsing hostname: %w", err)
		}
		if u.Host != "" {
			hostname = u.Host
		}
	}
	return func(ctx context.Context) ([]CheckMeasurement, error) {
		ips, err := net.LookupHost(hostname)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("no addresses found")
		}
		return nil, nil
	}, nil
}

// NewHTTPCheck creates a new Check which verifies HTTP connectivity to the specified URL. DNS will
// be resolved exactly once.
func NewHTTPCheck(url string) (Check, error) {
	if url == "" {
		return nil, errors.New("http check requires url")
	}
	return func(ctx context.Context) ([]CheckMeasurement, error) {
		c := http.Client{}
		defer c.CloseIdleConnections()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("building request: %w", err)
		}
		resp, err := c.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			var additionalInfo string
			if originCode := resp.Header.Get("x-do-orig-status"); originCode != "" {
				additionalInfo += " origin-code=" + originCode
			}
			if msg := resp.Header.Get("x-do-failure-msg"); msg != "" {
				additionalInfo += " " + msg
			}
			if code := resp.Header.Get("x-do-failure-code"); code != "" {
				additionalInfo += " " + code
			}
			return nil, fmt.Errorf("unexpected status code: %d%s", resp.StatusCode, additionalInfo)
		}
		return nil, nil
	}, nil
}

// NewMySQLCheck will connect to the provided mysql server, execute a ping and disconnect.
func NewMySQLCheck(uri, cert string) (Check, error) {
	// All of this only gets done once.
	u, err := dburl.Parse(uri)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	tlsMode := "true"
	if cert != "" {
		tlsMode, err = registerMySQLCertificate(cert)
		if err != nil {
			return nil, fmt.Errorf("loading TLS cert: %w", err)
		}
	}
	if cert != "" || strings.EqualFold(q.Get("ssl-mode"), "required") {
		q.Del("ssl-mode")
		q.Add("tls", tlsMode)
	}
	u.RawQuery = q.Encode()
	connStr, err := dburl.GenMysql(u)
	if err != nil {
		return nil, err
	}
	config, err := mysql.ParseDSN(connStr)
	if err != nil {
		return nil, fmt.Errorf("parsing dsn")
	}
	// We make a connector directly because we don't want the built-in "database/sql" pooling.
	connector, err := mysql.NewConnector(config)
	if err != nil {
		return nil, fmt.Errorf("building mysql connector")
	}
	// Ok we are FINALLY done with all of the setup.  This is the real check.
	return func(ctx context.Context) ([]CheckMeasurement, error) {
		dbConn, err := connector.Connect(ctx)
		if err != nil {
			return nil, err
		}
		defer dbConn.Close()
		pinger, ok := dbConn.(driver.Pinger)
		if !ok {
			return nil, errors.New("mysql driver missing Ping(ctx)")
		}
		return nil, pinger.Ping(ctx)
	}, nil
}
