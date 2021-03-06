package db

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/GeertJohan/go.rice"
	"github.com/atrox/go-migrate-rice"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	log "github.com/sirupsen/logrus"
)

func Open(path string, options string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("Opening database: path must not be empty")
	}

	if options == "" {
		options = "cache=shared&journal_mode=WAL"
	}

	db, err := sql.Open("sqlite3", path+"?"+options)
	if err != nil {
		// if the file exists try removing it and opening it again
		// this could be because of change in database file formats
		// or a corrupted database
		if _, rmErr := os.Stat(path); !os.IsNotExist(rmErr) {
			os.Remove(path)
		} else {
			return nil, rmErr
		}

		db, err = sql.Open("sqlite3", path+"?"+options)
		if err != nil {
			return nil, err
		}
	}
	// disabling this made a lot of things work better
	// but it might bring in some side effects we need
	// to watch out for
	//db.SetMaxOpenConns(1)

	return db, nil
}

func OpenAndMigrate(path string, options string, box *rice.Box) (*sql.DB, error) {

	db, err := Open(path, options)
	if err != nil {
		return nil, err
	}

	migrationDriver, err := migraterice.WithInstance(box)
	if err != nil {
		log.Errorf("Could not get migration instances: %s", err)
		return nil, err
	}

	dbDriver, err := sqlite3.WithInstance(db, &sqlite3.Config{})
	if err != nil {
		log.Errorf("Could not open db: %s", err)
		return nil, err
	}

	m, err := migrate.NewWithInstance("rice", migrationDriver, "sqlite3", dbDriver)
	if err != nil {
		log.Errorf("Could not migrate: %s", err)
		return nil, err
	}

	// migrate to newest version of database in the given box
	err = m.Up()
	// only return error if it is not a "no change" error (those are fine)
	if err != nil && err != migrate.ErrNoChange {
		return nil, err
	}

	return db, nil
}
