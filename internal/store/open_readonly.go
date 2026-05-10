package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func OpenReadOnly(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("baseline archive not found: %s: %w", path, err)
		}
		return nil, err
	}

	// Build the SQLite URI via net/url so paths containing URI metacharacters
	// (`?`, `#`, spaces) are escaped instead of being parsed as part of the
	// URI grammar.
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	var sentinel int
	err = db.QueryRow(`select 1 from project_info where id = 'default'`).Scan(&sentinel)
	if err != nil {
		db.Close()
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("path is not a supacrawl archive: %s", path)
		}
		return nil, fmt.Errorf("path is not a supacrawl archive: %s: %w", path, err)
	}

	return &Store{db: db, path: path}, nil
}
