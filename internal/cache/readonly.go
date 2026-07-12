package cache

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// OpenReadOnly opens an existing cache database without creating the cache
// directory or main database, initializing its schema, or changing business
// state. SQLite may create or coordinate transient WAL/SHM sidecars while
// reading a live WAL database.
func OpenReadOnly(configured string) (*Cache, error) {
	dir := ResolveDir(configured)
	dbPath := filepath.Join(dir, "scriba.sqlite")
	info, err := os.Stat(dbPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("cache database is not a regular file: %s", dbPath)
	}

	u := &url.URL{Scheme: "file", Path: dbPath}
	query := u.Query()
	query.Set("mode", "ro")
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	var schemaVersion int
	if err := db.QueryRow(`select value from meta where key = 'schema_version'`).Scan(&schemaVersion); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("read cache schema version: %w", err)
	}
	if schemaVersion != SchemaVersion {
		_ = db.Close()
		return nil, fmt.Errorf("unsupported cache schema version %d (expected %d)", schemaVersion, SchemaVersion)
	}
	return &Cache{dir: dir, db: db}, nil
}
