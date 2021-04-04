package main

import "crawshaw.io/sqlite/sqlitex"

// LoadDBPool loads or creates a database on disk, and returns a connection pool.
func LoadDBPool(cfg *Config) (*sqlitex.Pool, error) {
	pool, err := sqlitex.Open(cfg.DBPath, 0, 10)
	if err != nil {
		return nil, err
	}
	// TODO(morgangallant): migrations
	return pool, nil
}
