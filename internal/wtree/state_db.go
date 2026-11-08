package wtree

import "database/sql"

// DB is the state database, for the ledger's table of resource records.
func (s *State) DB() *sql.DB {
	return s.db
}
