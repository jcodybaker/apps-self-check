package main

import (
	"context"
	"database/sql"

	sq "github.com/Masterminds/squirrel"
	_ "github.com/go-sql-driver/mysql"
)

// Storer instances store stuff.
type Storer interface {
	// SaveCheckResults saves check results.
	SaveCheckResults(ctx context.Context, result CheckResults) (err error)
}

type mysqlStorer struct {
	db *sql.DB
}

// NewMySQLStorer creates a new storer driver for a MySQL backend.
func NewMySQLStorer(ctx context.Context, uri string) (Storer, error) {
	db, err := sql.Open("mysql", uri)
	if err != nil {
		return nil, err
	}
	m := &mysqlStorer{db: db}
	if err := m.init(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

// SaveCheckResults saves check results.
func (m *mysqlStorer) SaveCheckResults(ctx context.Context, result CheckResults) (err error) {
	m.db.Begin()
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelDefault,
	})
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			err = tx.Commit()
		}
	}()

	r, err := sq.Insert("checks").Columns("app_id", "ts").Values(result.AppID, result.TS).RunWith(tx).ExecContext(ctx)
	if err != nil {
		return err
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
		_, err = q.RunWith(tx).ExecContext(ctx)
	}
	if len(result.Errors) > 0 {
		q := sq.Insert("check_errors").Columns("check_id", "check_name", "error")
		for _, e := range result.Errors {
			q = q.Values(id, e.Check, e.Error.Error())
		}
		_, err = q.RunWith(tx).ExecContext(ctx)
	}
	if len(result.Measurements) > 0 {
		q := sq.Insert("check_measurements").Columns("check_id", "measurement", "value")
		for _, e := range result.Measurements {
			q = q.Values(id, e.Check, e.Value)
		}
		_, err = q.RunWith(tx).ExecContext(ctx)
	}

	return nil
}

func (m *mysqlStorer) init(ctx context.Context) error {
	_, err := m.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS checks (
			id INT NOT NULL AUTO_INCREMENT,
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
			error VARCHAR(128) NOT NULL,
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
