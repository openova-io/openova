package db

import (
	"database/sql"
	"time"

	_ "github.com/lib/pq"
)

// ConnectPostgres opens a PostgreSQL connection, configures the pool, and pings.
func ConnectPostgres(dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

// MustConnect calls ConnectPostgres and panics on error.
func MustConnect(dsn string) *sql.DB {
	db, err := ConnectPostgres(dsn)
	if err != nil {
		panic("db: " + err.Error())
	}
	return db
}
