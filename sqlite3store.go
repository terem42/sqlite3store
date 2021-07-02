package sqlite3store

// Copyright 2021 Andrey Prokopenko. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
import (
	"context"
	"encoding/base32"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/pkg/errors"

	"regexp"

	"github.com/gorilla/securecookie"
	"github.com/gorilla/sessions"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
)

// SqliteStore represents the currently configured session store for SQLite
type SqliteStore struct {
	Codecs    []securecookie.Codec
	Options   *sessions.Options
	tableName string
	DbPool    *sqlitex.Pool
}

type sqliteSession struct {
	id           string
	session_data string
	created_on   time.Time
	modified_on  time.Time
	expires_on   time.Time
}

var defaultInterval = time.Minute * 5

// creates a new store instance and a new pool.
func NewSqliteStore(file_location string, poolSize int, tableName string, sessions_options *sessions.Options, keyPairs ...[]byte) (*SqliteStore, error) {
	flags := sqlite.SQLITE_OPEN_READWRITE | sqlite.SQLITE_OPEN_CREATE |
		sqlite.SQLITE_OPEN_WAL | sqlite.SQLITE_OPEN_URI | sqlite.SQLITE_OPEN_NOMUTEX
	dbpool, err := sqlitex.Open(file_location, flags, poolSize)
	if err != nil {
		return nil, err
	}

	return NewSqliteStoreFromExistingPool(dbpool, tableName, sessions_options, keyPairs...)
}

// NewSqliteFromPool creates a new SqliteStore instance from an existing
// Sqlite pool, initialized elsewhere
func NewSqliteStoreFromExistingPool(dbpool *sqlitex.Pool, tableName string, sessions_options *sessions.Options, keyPairs ...[]byte) (*SqliteStore, error) {
	reg, err := regexp.Compile("[^a-zA-Z0-9]+")
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create regex")
	}
	tablename_cleaned := reg.ReplaceAllString(tableName, "")
	if len(tablename_cleaned) == 0 {
		return nil, errors.Wrapf(err, "incorrect sessions table name")
	}
	// Create table if it doesn't exist
	stmt := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s 
		(id CHAR PRIMARY KEY,
	session_data TEXT,
	created_on INTEGER,
	modified_on INTEGER,
	expires_on INTEGER default 0);`, tablename_cleaned)

	c := dbpool.Get(context.TODO())
	defer dbpool.Put(c)
	err = sqlitex.ExecScript(c, stmt)
	if err != nil {
		return nil, errors.Wrapf(err, "Unable to create %s table in the database", tablename_cleaned)
	}
	store := &SqliteStore{
		Codecs:    securecookie.CodecsFromPairs(keyPairs...),
		tableName: tablename_cleaned,
		Options:   sessions_options,
		DbPool:    dbpool,
	}

	return store, nil
}

// Close closes all database connections in the pool.
func (db *SqliteStore) CloseDatabasePool() {
	db.DbPool.Close()
}

// Get Fetches a session for a given name after it has been added to the
// registry.
func (store *SqliteStore) Get(r *http.Request, name string) (*sessions.Session, error) {
	return sessions.GetRegistry(r).Get(store, name)
}

// New returns a new session for the given name without adding it to the registry.
func (store *SqliteStore) New(r *http.Request, name string) (*sessions.Session, error) {
	session := sessions.NewSession(store, name)
	if session == nil {
		return nil, nil
	}

	opts := *store.Options
	session.Options = &(opts)
	session.IsNew = true

	var err error
	if c, errCookie := r.Cookie(name); errCookie == nil {
		err = securecookie.DecodeMulti(name, c.Value, &session.ID, store.Codecs...)
		if err == nil {
			session_found, load_err := store.load(session)
			if session_found {
				session.IsNew = false
			} else if load_err != nil {
				return session, load_err
			}
		}
	}

	store.MaxAge(store.Options.MaxAge)

	return session, err
}

// Save saves the given session into the database and deletes cookies if needed
func (store *SqliteStore) Save(r *http.Request, w http.ResponseWriter, session *sessions.Session) error {
	// Set delete if max-age is < 0
	if session.Options.MaxAge < 0 {
		if err := store.destroy(session); err != nil {
			return err
		}
		http.SetCookie(w, sessions.NewCookie(session.Name(), "", session.Options))
		return nil
	}

	if session.ID == "" {
		// Generate a random session ID key suitable for storage in the DB
		session.ID = strings.TrimRight(
			base32.StdEncoding.EncodeToString(
				securecookie.GenerateRandomKey(32),
			), "=")
	}

	if err := store.save(session); err != nil {
		return err
	}

	// Keep the session ID key in a cookie so it can be looked up in DB later.
	encoded, err := securecookie.EncodeMulti(session.Name(), session.ID, store.Codecs...)
	if err != nil {
		return err
	}

	http.SetCookie(w, sessions.NewCookie(session.Name(), encoded, session.Options))
	return nil
}

// MaxLength restricts the maximum length of new sessions to l.
// If session_len is 0 there is no limit to the size of a session, use with caution.
func (store *SqliteStore) SetMaxLength(session_len int) {
	for _, c := range store.Codecs {
		if codec, ok := c.(*securecookie.SecureCookie); ok {
			codec.MaxLength(session_len)
		}
	}
}

// MaxAge sets the maximum age for the store and the underlying cookie
// implementation. Individual sessions can be deleted by setting Options.MaxAge
// = -1 for that session.
func (store *SqliteStore) MaxAge(age int) {
	store.Options.MaxAge = age

	// Set the maxAge for each securecookie instance.
	for _, codec := range store.Codecs {
		if sc, ok := codec.(*securecookie.SecureCookie); ok {
			sc.MaxAge(age)
		}
	}
}

func (store *SqliteStore) StartCleanup(interval time.Duration) (chan<- struct{}, <-chan struct{}) {
	if interval <= 0 {
		interval = defaultInterval
	}

	quit, done := make(chan struct{}), make(chan struct{})
	go store.cleanup(interval, quit, done)
	return quit, done
}

// StopCleanup stops the background cleanup from running.
func (store *SqliteStore) StopCleanup(quit chan<- struct{}, done <-chan struct{}) {
	quit <- struct{}{}
	<-done
}

// load fetches a session by ID from the database and decodes its content
// into session.Values.
func (store *SqliteStore) load(session *sessions.Session) (bool, error) {
	c := store.DbPool.Get(context.TODO())
	stmt := c.Prep(fmt.Sprintf("SELECT session_data from %s WHERE id = ?", store.tableName))
	stmt.BindText(1, session.ID)
	defer store.DbPool.Put(c)
	hasRow, err := stmt.Step()
	if err != nil {
		return false, err
	}
	session_data := stmt.GetText("session_data")
	stmt.Reset()
	if !hasRow {
		return false, nil
	}
	return true, securecookie.DecodeMulti(session.Name(), session_data, &session.Values, store.Codecs...)
}

// save writes encoded session.Values to a database record.
// writes to http_sessions table by default.
func (store *SqliteStore) save(session *sessions.Session) error {
	encoded, err := securecookie.EncodeMulti(session.Name(), session.Values, store.Codecs...)
	if err != nil {
		return err
	}

	crOn := session.Values["created_on"]
	exOn := session.Values["expires_on"]

	var expiresOn time.Time

	createdOn, ok := crOn.(time.Time)
	if !ok {
		createdOn = time.Now()
	}

	if exOn == nil {
		expiresOn = time.Now().Add(time.Second * time.Duration(session.Options.MaxAge))
	} else {
		expiresOn = exOn.(time.Time)
		if expiresOn.Sub(time.Now().Add(time.Second*time.Duration(session.Options.MaxAge))) < 0 {
			expiresOn = time.Now().Add(time.Second * time.Duration(session.Options.MaxAge))
		}
	}

	s := sqliteSession{
		id:           session.ID,
		session_data: encoded,
		created_on:   createdOn,
		modified_on:  time.Now(),
		expires_on:   expiresOn,
	}

	if session.IsNew {
		return store.insert(&s)
	}

	return store.update(&s)
}

// Delete session from store
func (store *SqliteStore) destroy(session *sessions.Session) error {
	c := store.DbPool.Get(context.TODO())
	stmt := c.Prep(fmt.Sprintf("delete from %s where id = ?", store.tableName))
	stmt.BindText(1, session.ID)
	defer store.DbPool.Put(c)
	_, err := stmt.Step()
	if err == nil {
		stmt.Reset()
	}
	return err
}

func (store *SqliteStore) insert(s *sqliteSession) error {
	c := store.DbPool.Get(context.TODO())
	stmt := c.Prep(fmt.Sprintf("INSERT INTO %s (id, session_data, created_on, modified_on, expires_on) VALUES (?, ?, ?, ?, ?)", store.tableName))
	stmt.BindText(1, s.id)
	stmt.BindText(2, s.session_data)
	stmt.BindInt64(3, s.created_on.Unix())
	stmt.BindInt64(4, s.modified_on.Unix())
	stmt.BindInt64(5, s.expires_on.Unix())
	defer store.DbPool.Put(c)
	_, err := stmt.Step()
	if err == nil {
		stmt.Reset()
	}
	return err
}

func (store *SqliteStore) update(s *sqliteSession) error {
	c := store.DbPool.Get(context.TODO())
	stmt := c.Prep(fmt.Sprintf("UPDATE %s SET session_data = ?, created_on = ?, modified_on = ?, expires_on = ? WHERE id = ?", store.tableName))
	stmt.BindText(1, s.session_data)
	stmt.BindInt64(2, s.created_on.Unix())
	stmt.BindInt64(3, s.modified_on.Unix())
	stmt.BindInt64(4, s.expires_on.Unix())
	stmt.BindText(5, s.id)
	defer store.DbPool.Put(c)
	_, err := stmt.Step()
	if err == nil {
		stmt.Reset()
	}
	return err
}

// cleanup deletes expired sessions at set intervals.
func (store *SqliteStore) cleanup(interval time.Duration, quit <-chan struct{}, done chan<- struct{}) {
	ticker := time.NewTicker(interval)

	defer func() {
		ticker.Stop()
	}()

	for {
		select {
		case <-quit:
			// Handle the quit signal.
			done <- struct{}{}
			return
		case <-ticker.C:
			// Delete expired sessions on each tick.
			err := store.deleteExpired()
			if err != nil {
				log.Printf("sqlitestore: error while deletings expired sessions: %v", err)
			}
		}
	}
}

// deleteExpired deletes expired sessions from the database.
func (store *SqliteStore) deleteExpired() error {
	c := store.DbPool.Get(context.TODO())
	stmt := c.Prep(fmt.Sprintf("delete from %s where expires_on < ?", store.tableName))
	stmt.BindInt64(1, time.Now().Unix())
	defer store.DbPool.Put(c)
	_, err := stmt.Step()
	if err == nil {
		stmt.Reset()
	}
	return err
}
