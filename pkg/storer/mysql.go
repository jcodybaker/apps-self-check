package storer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"io/ioutil"
	"strings"
	"sync"
	"time"

	"github.com/digitalocean/apps-self-check/pkg/types/check"

	sq "github.com/Masterminds/squirrel"
	"github.com/go-sql-driver/mysql"
	_ "github.com/go-sql-driver/mysql"
	"github.com/xo/dburl"
)

const (
	defaultConnectTimeout = "10s"
	defaultReadTimeout    = "10s"
	defaultWriteTimeout   = "10s"
)

// mysqlConfigID ensures any certificates registered against the driver are given a unique name.
var mysqlConfigID = 1

type mysqlStorer struct {
	db *sql.DB
	commonStorer
	instanceLock sync.Mutex
}

// NewMySQLStorer creates a new storer driver for a MySQL backend.
func NewMySQLStorer(ctx context.Context, uri, cert string, createTables bool) (Storer, error) {
	u, err := dburl.Parse(uri)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	tlsMode := "true"
	if cert != "" {
		tlsMode, err = RegisterMySQLCertificate(cert)
		if err != nil {
			return nil, fmt.Errorf("loading TLS cert: %w", err)
		}
	}
	if cert != "" || strings.EqualFold(q.Get("ssl-mode"), "required") {
		q.Del("ssl-mode")
		q.Add("tls", tlsMode)
	}
	q.Add("parseTime", "true")
	if !q.Has("timeout") {
		q.Add("timeout", defaultConnectTimeout)
	}
	if !q.Has("writeTimeout") {
		q.Add("writeTimeout", defaultWriteTimeout)
	}
	if !q.Has("readTimeout") {
		q.Add("readTimeout", defaultReadTimeout)
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
		db: db,
	}
	if err := m.init(ctx, createTables); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

// SaveCheckResults saves check results.
func (m *mysqlStorer) SaveCheckResults(ctx context.Context, result check.CheckResults) (err error) {
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

	r, err := sq.Insert("checks").Columns("instance_id", "ts").
		Values(result.Instance.DatabaseID, result.TS).RunWith(tx).ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("storing check result: %w", err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		return err
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

// Close shuts down the database handle and any async savers.
func (m *mysqlStorer) Close() error {
	m.closeOnce.Do(func() {
		close(m.done)
	})
	m.wg.Wait()
	return m.db.Close()
}

func RegisterMySQLCertificate(cert string) (string, error) {
	rootCertPool := x509.NewCertPool()
	pem := []byte(cert)
	if strings.HasPrefix(cert, "/") {
		var err error
		pem, err = ioutil.ReadFile(cert)
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

func (m *mysqlStorer) init(ctx context.Context, createTables bool) error {
	m.commonStorer.init()
	if !createTables {
		return nil
	}

	exists, err := m.tableExists(ctx, "instance")
	if err != nil {
		return err
	}
	if !exists {
		// We keep the "IF NOT EXISTS" because there may be other instances creating these tables.
		_, err := m.db.ExecContext(ctx, `
			CREATE TABLE IF NOT EXISTS instances (
				id INT NOT NULL AUTO_INCREMENT,
				uuid CHAR(36) NOT NULL,
				hostname VARCHAR(64) NOT NULL,
				app_id CHAR(36) NULL,
				public_ipv4 CHAR(15) NULL,
				started_at TIMESTAMP(6) NOT NULL,
				stopped_at TIMESTAMP(6) NOT NULL,
				KEY app_id (app_id),
				KEY public_ipv4 (public_ipv4),
				KEY hostname (hostname),
				KEY started_at (started_at),
				KEY stopped_at (stopped_at),
				UNIQUE KEY uuid (uuid)
				PRIMARY KEY(id)
			)
		`)
		if err != nil {
			return err
		}
	}

	exists, err = m.tableExists(ctx, "checks")
	if err != nil {
		return err
	}
	if !exists {
		// We keep the "IF NOT EXISTS" because there may be other instances creating these tables.
		_, err := m.db.ExecContext(ctx, `
			CREATE TABLE IF NOT EXISTS checks (
				id INT NOT NULL AUTO_INCREMENT,
				instance_id INT NOT NULL,
				ts TIMESTAMP(6) NOT NULL,
				KEY app_id_ts (instance_id, ts),
				PRIMARY KEY(id)
			)
		`)
		if err != nil {
			return err
		}
	}

	exists, err = m.tableExists(ctx, "check_errors")
	if err != nil {
		return err
	}
	if !exists {
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
	}

	exists, err = m.tableExists(ctx, "check_measurements")
	if err != nil {
		return err
	}
	if !exists {
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
	}

	exists, err = m.tableExists(ctx, "check_labels")
	if err != nil {
		return err
	}
	if !exists {
		_, err = m.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS instance_labels (
			instance_id INT NOT NULL,
			k VARCHAR(64) NOT NULL,
			v VARCHAR(64) NOT NULL,
			KEY kv (k, v),
			PRIMARY KEY(check_id, k)
		)
	`)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *mysqlStorer) AnalyzeLongestGapPerApp(
	ctx context.Context,
	start time.Time,
	end time.Time,
	apps []string,
	output func(appID string, interval time.Duration, maxIntervalTS time.Time),
) error {
	cond := sq.And{sq.GtOrEq{"ts": start}, sq.LtOrEq{"ts": end}}
	if len(apps) > 0 {
		cond = append(cond, sq.Eq{"app_id": apps})
	}
	q := sq.Select("app_id", "ts").
		From("checks").
		Where(cond).
		OrderBy("app_id", "ts ASC")
	rows, err := q.RunWith(m.db).QueryContext(ctx)
	if err != nil {
		return err
	}
	defer rows.Close()
	var maxInterval time.Duration = -1
	var maxIntervalTS, lastTS time.Time
	var lastAppID string
	for rows.Next() {
		var appID string
		var ts time.Time
		if err := rows.Scan(&appID, &ts); err != nil {
			return err
		}
		if appID != lastAppID {
			if lastAppID != "" && maxInterval > 0 {
				output(lastAppID, maxInterval, maxIntervalTS)
			}
			lastAppID = appID
			lastTS = ts
			maxIntervalTS = ts
			maxInterval = -1
			continue
		}
		interval := ts.Sub(lastTS)
		if interval > maxInterval {
			maxInterval = interval
			maxIntervalTS = ts
		}
		lastTS = ts
	}
	if lastAppID != "" && maxInterval > 0 {
		output(lastAppID, maxInterval, maxIntervalTS)
	}
	return rows.Err()
}

func (m *mysqlStorer) tableExists(ctx context.Context, table string) (bool, error) {
	err := m.db.QueryRowContext(ctx, `SELECT 1 FROM `+table).Err()
	if mErr, ok := err.(*mysql.MySQLError); ok {
		if mErr.Number == 1146 {
			return false, nil
		}
	}
	return true, nil
}

func (m *mysqlStorer) ensureInstance(ctx context.Context, instance *check.Instance) error {
	m.instanceLock.Lock()
	defer m.instanceLock.Unlock()
	if instance.DatabaseID != 0 {
		// If the database ID exists, we don't need to insert the instance. Updates to the instance
		// are handled via UpdateInstance.
		return nil
	}
	return m.updateInstance(ctx, instance)
}

func (m *mysqlStorer) updateInstance(ctx context.Context, instance *check.Instance) error {
	r, err := sq.Insert("instances").Columns(
		"uuid",
		"hostname",
		"app_id",
		"public_ipv4",
		"started_at",
		"stopped_at",
	).Values(
		instance.UUID,
		sql.NullString{Valid: instance.AppID != "", String: instance.AppID},
		instance.Hostname,
		sql.NullString{Valid: instance.PublicIPv4 != "", String: instance.PublicIPv4},
		instance.StartedAt,
		sql.NullTime{Valid: !instance.StoppedAt.IsZero(), Time: instance.StoppedAt},
	).Suffix(`
	ON DUPLICATE KEY UPDATE
	id = LAST_INSERT_ID(id),
	uuid = VALUES(uuid),
	hostname = VALUES(hostname),
	app_id = VALUES(app_id),
	public_ipv4 = VALUES(public_ipv4),
	started_at = VALUES(started_at),
	stopped_at = VALUES(stopped_at)
	`).RunWith(m.db).ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("updating instance record: %w", err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		return fmt.Errorf("reading instance id: %w", err)
	}
	instance.DatabaseID = id
	if len(instance.Labels) > 0 {
		// We won't cleanup removed labels.
		q := sq.Insert("instance_labels").Columns("instance_id", "k", "v")
		for k, v := range instance.Labels {
			q = q.Values(id, k, v)
		}
		q = q.Suffix(`ON DUPLICATE KEY UPDATE v = VALUES(v)`)
		if _, err = q.RunWith(m.db).ExecContext(ctx); err != nil {
			return fmt.Errorf("storing instance labels: %w", err)
		}
	}
	return nil
}

func (m *mysqlStorer) UpdateInstance(ctx context.Context, instance *check.Instance) error {
	m.instanceLock.Lock()
	defer m.instanceLock.Unlock()
	return m.updateInstance(ctx, instance)
}
