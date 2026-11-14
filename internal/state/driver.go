package state

import (
	"database/sql"
	"database/sql/driver"
	"errors"

	"github.com/mattn/go-sqlite3"
)

const driverName = "agentpack-state"

func init() { sql.Register(driverName, stateDriver{}) }

type stateDriver struct{}

func (stateDriver) Open(dsn string) (driver.Conn, error) {
	conn, err := new(sqlite3.SQLiteDriver).Open(dsn)
	if err != nil {
		return nil, err
	}
	return &stateConn{conn.(*sqlite3.SQLiteConn)}, nil
}

// sql.DB can retire pooled connections at any time, not only on Store.Close.
type stateConn struct{ *sqlite3.SQLiteConn }

func (conn *stateConn) Close() error {
	_, err := conn.SQLiteConn.Exec("SELECT crsql_finalize()", nil)
	return errors.Join(err, conn.SQLiteConn.Close())
}
