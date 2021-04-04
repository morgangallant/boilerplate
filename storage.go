package main

import (
	"context"
	"fmt"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
	"github.com/pkg/errors"
)

type migration []string

func (mig migration) exec(conn *sqlite.Conn) (err error) {
	defer sqlitex.Save(conn)(&err)
	var stmt *sqlite.Stmt
	for _, m := range mig {
		stmt, _, err = conn.PrepareTransient(m)
		if err != nil {
			err = errors.Wrap(err, "failed to prepare migration step")
			return
		}
		if _, err = stmt.Step(); err != nil {
			err = errors.Wrap(err, "failed to execute migration step")
			return
		}
		if err = stmt.Finalize(); err != nil {
			err = errors.Wrap(err, "failed to finalize migration step")
			return
		}
	}
	return
}

// Append-only.
var migrations = []migration{}

// LoadDBPool loads or creates a database on disk, and returns a connection pool.
func LoadDBPool(cfg *Config, logger Logger) (*sqlitex.Pool, error) {
	pool, err := sqlitex.Open(cfg.DBPath, 0, 10)
	if err != nil {
		return nil, err
	}
	conn := pool.Get(context.TODO())
	if conn == nil {
		return nil, errors.New("nil connection returned from db pool")
	}
	defer pool.Put(conn)
	if err := migrate(conn, logger, migrations); err != nil {
		return nil, errors.Wrap(err, "failed to migrate database")
	}
	return pool, nil
}

func getUserVersion(conn *sqlite.Conn) (int, error) {
	var version int
	stmt := conn.Prep(`PRAGMA user_version;`)
	for {
		if hasRow, err := stmt.Step(); err != nil {
			return 0, errors.Wrap(err, "failed to query user_version")
		} else if !hasRow {
			break
		}
		version = int(stmt.GetInt64("user_version"))
	}
	return version, nil
}

func setUserVersion(conn *sqlite.Conn, version int) (err error) {
	defer sqlitex.Save(conn)(&err)
	var stmt *sqlite.Stmt
	stmt, _, err = conn.PrepareTransient(fmt.Sprintf("PRAGMA user_version=%d;", version))
	if err != nil {
		err = errors.Wrap(err, "failed to prepare statement")
		return
	}
	defer stmt.Finalize()
	if _, err = stmt.Step(); err != nil {
		err = errors.Wrap(err, "failed to update user version")
	}
	return
}

func migrate(conn *sqlite.Conn, logger Logger, migs []migration) (err error) {
	defer sqlitex.Save(conn)(&err)
	var ver int
	ver, err = getUserVersion(conn)
	if err != nil {
		err = errors.Wrap(err, "failed to get current user version of sqlite")
		return
	}
	l := len(migrations)
	if ver == l {
		// No migrations required.
		return
	}
	for i := ver; i < l; i++ {
		if err = migrations[i].exec(conn); err != nil {
			err = errors.Wrapf(err, "failed to execute migration %d, rolling back to %d", i+1, ver)
			return
		}
	}
	if err = setUserVersion(conn, l); err != nil {
		err = errors.Wrap(err, "failed to update current user version")
		return
	}
	logger.Logf("Migrated DB To %d.", l)
	return
}
