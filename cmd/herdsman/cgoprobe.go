//go:build cgoprobe

// The release-build check (.github/workflows/release-build.yml) builds with
// -tags cgoprobe to prove that cgo and the SQLite driver link for every
// release target. This file goes when the daemon imports the driver itself.

package main

import (
	"database/sql"
	"fmt"
	"io"

	_ "github.com/mattn/go-sqlite3"
)

func init() {
	cgoProbe = func(stdout io.Writer) error {
		db, err := sql.Open("sqlite3", ":memory:")
		if err != nil {
			return err
		}
		defer db.Close()
		var v string
		if err := db.QueryRow("SELECT sqlite_version()").Scan(&v); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "cgo probe: sqlite %s in memory\n", v)
		return nil
	}
}
