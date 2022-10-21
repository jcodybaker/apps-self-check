package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"io/ioutil"
	"strconv"
	"strings"
	"sync"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/go-sql-driver/mysql"
	_ "github.com/go-sql-driver/mysql"
	"github.com/rs/zerolog/log"
	"github.com/xo/dburl"
)

const (
	// dyingBreathExpiration defines a grace period after the ctx has been canceled, when async
	// saves will be attempted before returning.
	dyingBreathExpiration = 1 * time.Second
)

// mysqlConfigID ensures any certificates registered against the driver are given a unique name.
var mysqlConfigID = 1

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

// Storer instances store stuff.
type Storer interface {
	// SaveCheckResults saves check results.
	SaveCheckResults(ctx context.Context, result CheckResults) (err error)

	// AsyncSaveCheckResults saves check results asynchronously with retries on failure.
	AsyncSaveCheckResults(ctx context.Context, result CheckResults, attemptSchedule []time.Duration)

	// Close triggers any asynchronous saves to immediately make a final attempt, waits briefly
	// for their completion, and closes database connections..
	Close() error
}

type mysqlStorer struct {
	db        *sql.DB
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// NewMySQLStorer creates a new storer driver for a MySQL backend.
func NewMySQLStorer(ctx context.Context, uri, cert string) (Storer, error) {
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
	db, err := sql.Open("mysql", connStr)
	if err != nil {
		return nil, err
	}
	// Open() only inits the config & pool, do a Ping() to establish/validate a connection.
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	m := &mysqlStorer{
		db:   db,
		done: make(chan struct{}),
	}
	if err := m.init(ctx); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

// SaveCheckResults saves check results.
func (m *mysqlStorer) SaveCheckResults(ctx context.Context, result CheckResults) (err error) {
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelDefault,
	})
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			err = tx.Commit()
		}
	}()

	r, err := sq.Insert("checks").Columns("app_id", "hostname", "ts").Values(result.AppID, result.Hostname, result.TS).RunWith(tx).ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("storing check result: %w", err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		return err
	}

	if len(result.Labels) > 0 {
		q := sq.Insert("check_labels").Columns("check_id", "k", "v")
		for k, v := range result.Labels {
			q = q.Values(id, k, v)
		}
		if _, err = q.RunWith(tx).ExecContext(ctx); err != nil {
			return fmt.Errorf("storing labels: %w", err)
		}
	}
	if len(result.Errors) > 0 {
		q := sq.Insert("check_errors").Columns("check_id", "check_name", "error")
		for _, e := range result.Errors {
			errStr := e.Error
			if len(errStr) > 512 { // Truncate to fit column.
				errStr = errStr[:512]
			}
			q = q.Values(id, e.Check, errStr)
		}
		if _, err = q.RunWith(tx).ExecContext(ctx); err != nil {
			return fmt.Errorf("storing errors: %w", err)
		}
	}
	if len(result.Measurements) > 0 {
		q := sq.Insert("check_measurements").Columns("check_id", "measurement", "value")
		for _, e := range result.Measurements {
			q = q.Values(id, e.Check, e.Value)
		}
		if _, err = q.RunWith(tx).ExecContext(ctx); err != nil {
			return fmt.Errorf("storing measurements: %w", err)
		}
	}

	return nil
}

// AsyncSaveCheckResults saves check results asynchronously with retries on failure.
func (m *mysqlStorer) AsyncSaveCheckResults(ctx context.Context, result CheckResults, attemptSchedule []time.Duration) {
	m.wg.Add(1)
	ll := log.Ctx(ctx)
	go func() {
		defer m.wg.Done()
		var err error
		for i, delay := range attemptSchedule {
			var dyingBreath bool
			if delay > 0 {
				t := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					dyingBreath = true
				case <-t.C:
				case <-m.done:
					dyingBreath = true
				}
				if dyingBreath {
					// We're shutting down, but haven't successfully saved yet. Make a hail mary
					// attempt with a fresh (but short-lived) ctx.
					var cancel func()
					ctx, cancel = context.WithTimeout(context.Background(), dyingBreathExpiration)
					defer cancel()
				}
			}
			err = m.SaveCheckResults(ctx, result)
			if err == nil {
				return
			}
			if i+1 < len(attemptSchedule) {
				ll.Warn().Err(err).Msg("saving results to database asynchronously")
			}
			result.Errors = append(result.Errors, CheckError{
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

// Close shuts down the database handle and any async savers.
func (m *mysqlStorer) Close() error {
	m.closeOnce.Do(func() {
		close(m.done)
	})
	m.wg.Wait()
	return m.db.Close()
}

func registerMySQLCertificate(cert string) (string, error) {
	rootCertPool := x509.NewCertPool()
	pem := []byte(cert)
	if strings.HasPrefix(cert, "/") {
		var err error
		pem, err = ioutil.ReadFile("/path/ca-cert.pem")
		if err != nil {
			return "", err
		}
	}
	if ok := rootCertPool.AppendCertsFromPEM(pem); !ok {
		return "", errors.New("appending certificate to pool")
	}

	mysqlConfigName := fmt.Sprintf("custom%d", mysqlConfigID)
	mysqlConfigID++
	mysql.RegisterTLSConfig(mysqlConfigName, &tls.Config{
		RootCAs: rootCertPool,
	})
	return mysqlConfigName, nil
}

func (m *mysqlStorer) init(ctx context.Context) error {
	_, err := m.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS checks (
			id INT NOT NULL AUTO_INCREMENT,
			hostname VARCHAR(64) NOT NULL,
			app_id CHAR(36) NOT NULL,
			ts TIMESTAMP(6) NOT NULL,
			KEY app_id_ts (app_id, ts),
			PRIMARY KEY(id)
		)
	`)
	if err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS check_errors (
			check_id INT NOT NULL,
			check_name VARCHAR(64) NOT NULL,
			error VARCHAR(512) NOT NULL,
			KEY check_name (check_name),
			PRIMARY KEY(check_id, check_name)
		)
	`)
	if err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS check_measurements (
			check_id INT NOT NULL,
			measurement VARCHAR(64) NOT NULL,
			value DOUBLE NOT NULL,
			KEY measurement (measurement),
			PRIMARY KEY(check_id, measurement)
		)
`)
	if err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS check_labels (
			check_id INT NOT NULL,
			k VARCHAR(64) NOT NULL,
			v VARCHAR(64) NOT NULL,
			KEY kv (k, v),
			PRIMARY KEY(check_id, k)
		)
	`)
	if err != nil {
		return err
	}
	return nil
}
