package sqlite3store

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"

	"encoding/base64"
	"encoding/gob"
	"net/http"
	"net/http/httptest"

	"testing"
	"time"

	"github.com/gorilla/sessions"
)

type headerOnlyResponseWriter http.Header

func (ho headerOnlyResponseWriter) Header() http.Header {
	return http.Header(ho)
}

func (ho headerOnlyResponseWriter) Write([]byte) (int, error) {
	panic("Not Implemented")
}

func (ho headerOnlyResponseWriter) WriteHeader(int) {
	panic("Not Implemented")
}

// ----------------------------------------------------------------------------
// ResponseRecorder
// ----------------------------------------------------------------------------
// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// ResponseRecorder is an implementation of http.ResponseWriter that
// records its mutations for later inspection in tests.
type ResponseRecorder struct {
	Code      int           // the HTTP response code from WriteHeader
	HeaderMap http.Header   // the HTTP response headers
	Body      *bytes.Buffer // if non-nil, the bytes.Buffer to append written data to
	Flushed   bool
}

// NewRecorder returns an initialized ResponseRecorder.
func NewRecorder() *ResponseRecorder {
	return &ResponseRecorder{
		HeaderMap: make(http.Header),
		Body:      new(bytes.Buffer),
	}
}

// DefaultRemoteAddr is the default remote address to return in RemoteAddr if
// an explicit DefaultRemoteAddr isn't set on ResponseRecorder.
const DefaultRemoteAddr = "1.2.3.4"

// Header returns the response headers.
func (rw *ResponseRecorder) Header() http.Header {
	return rw.HeaderMap
}

// Write always succeeds and writes to rw.Body, if not nil.
func (rw *ResponseRecorder) Write(buf []byte) (int, error) {
	if rw.Body != nil {
		rw.Body.Write(buf)
	}
	if rw.Code == 0 {
		rw.Code = http.StatusOK
	}
	return len(buf), nil
}

// WriteHeader sets rw.Code.
func (rw *ResponseRecorder) WriteHeader(code int) {
	rw.Code = code
}

// Flush sets rw.Flushed to true.
func (rw *ResponseRecorder) Flush() {
	rw.Flushed = true
}

// ----------------------------------------------------------------------------

type FlashMessage struct {
	Type    int
	Message string
}

var session_options = sessions.Options{
	Path:     "/",
	Domain:   "localhost",
	MaxAge:   600,
	Secure:   false,
	HttpOnly: false,
	SameSite: http.SameSiteDefaultMode,
}

func TestSqliteStore(t *testing.T) {
	var (
		//req     *http.Request
		rsp     *ResponseRecorder
		hdr     http.Header
		ok      bool
		cookies []string
		session *sessions.Session
		flashes []interface{}
	)

	defer func() {
		os.Remove("test.db")
		os.Remove("test.db-shm")
		os.Remove("test.db-wal")
	}()

	// Copyright 2021 Andrey Prokopenko. All rights reserved.
	// Use of this source code is governed by a BSD-style
	// license that can be found in the LICENSE file.

	// Round 1 ----------------------------------------------------------------
	{
		// SqliteStore
		log.Println("Round 1")
		store, err := NewSqliteStore("test.db", 10, "sessions", &session_options, []byte("secret-key"))
		if err != nil {
			t.Fatal(err.Error())
		}
		defer func() {
			store.CloseDatabasePool()
		}()

		req, _ := http.NewRequest("GET", "http://localhost:8080/", nil)
		rsp = NewRecorder()
		// Get a session.
		if session, err = store.Get(req, "session-key"); err != nil {
			t.Fatalf("Error getting session: %v", err)
		}
		// Get a flash.
		flashes = session.Flashes()
		if len(flashes) != 0 {
			t.Errorf("Expected empty flashes; Got %v", flashes)
		}
		// Add some flashes.
		session.AddFlash("foo")
		session.AddFlash("bar")
		// Custom key.
		session.AddFlash("baz", "custom_key")
		// Save.
		if err = sessions.Save(req, rsp); err != nil {
			t.Fatalf("Error saving session: %v", err)
		}
		hdr = rsp.Header()
		cookies, ok = hdr["Set-Cookie"]
		if !ok || len(cookies) != 1 {
			t.Fatalf("No cookies. Header: %s", hdr)
		}

	}

	// Round 2 ----------------------------------------------------------------
	{
		log.Println("Round 2")
		store, err := NewSqliteStore("test.db", 10, "sessions", &session_options, []byte("secret-key"))
		if err != nil {
			t.Fatal(err.Error())
		}
		defer func() {
			store.CloseDatabasePool()
		}()

		req, _ := http.NewRequest("GET", "http://localhost:8080/", nil)
		req.Header.Add("Cookie", cookies[0])
		rsp = NewRecorder()
		// Get a session.
		if session, err = store.Get(req, "session-key"); err != nil {
			t.Fatalf("Error getting session: %v", err)
		}
		// Check all saved values.
		flashes = session.Flashes()
		if len(flashes) != 2 {
			t.Fatalf("Expected flashes; Got %v", flashes)
		}
		if flashes[0] != "foo" || flashes[1] != "bar" {
			t.Errorf("Expected foo,bar; Got %v", flashes)
		}
		flashes = session.Flashes()
		if len(flashes) != 0 {
			t.Errorf("Expected dumped flashes; Got %v", flashes)
		}
		// Custom key.
		flashes = session.Flashes("custom_key")
		if len(flashes) != 1 {
			t.Errorf("Expected flashes; Got %v", flashes)
		} else if flashes[0] != "baz" {
			t.Errorf("Expected baz; Got %v", flashes)
		}
		flashes = session.Flashes("custom_key")
		if len(flashes) != 0 {
			t.Errorf("Expected dumped flashes; Got %v", flashes)
		}

		// RediStore specific
		// Set MaxAge to -1 to mark for deletion.
		session.Options.MaxAge = -1
		// Save.
		if err = sessions.Save(req, rsp); err != nil {
			t.Fatalf("Error saving session: %v", err)
		}
	}

	// Round 3 ----------------------------------------------------------------
	// Custom type
	{
		log.Println("Round 3")
		store, err := NewSqliteStore("test.db", 10, "sessions", &session_options, []byte("secret-key"))
		if err != nil {
			t.Fatal(err.Error())
		}
		defer func() {
			store.CloseDatabasePool()
		}()

		req, _ := http.NewRequest("GET", "http://localhost:8080/", nil)
		rsp = NewRecorder()
		// Get a session.
		if session, err = store.Get(req, "session-key"); err != nil {
			t.Fatalf("Error getting session: %v", err)
		}
		// Get a flash.
		flashes = session.Flashes()
		if len(flashes) != 0 {
			t.Errorf("Expected empty flashes; Got %v", flashes)
		}
		// Add some flashes.
		session.AddFlash(&FlashMessage{42, "foo"})
		// Save.
		if err = sessions.Save(req, rsp); err != nil {
			t.Fatalf("Error saving session: %v", err)
		}
		hdr = rsp.Header()
		cookies, ok = hdr["Set-Cookie"]
		if !ok || len(cookies) != 1 {
			t.Fatalf("No cookies. Header: %s", hdr)
		}
	}

	// Round 4 ----------------------------------------------------------------
	// Custom type
	{
		log.Println("Round 4")
		store, err := NewSqliteStore("test.db", 10, "sessions", &session_options, []byte("secret-key"))
		if err != nil {
			t.Fatal(err.Error())
		}
		defer func() {
			store.CloseDatabasePool()
		}()

		req, _ := http.NewRequest("GET", "http://localhost:8080/", nil)
		req.Header.Add("Cookie", cookies[0])
		rsp = NewRecorder()
		// Get a session.
		if session, err = store.Get(req, "session-key"); err != nil {
			t.Fatalf("Error getting session: %v", err)
		}
		// Check all saved values.
		flashes = session.Flashes()
		if len(flashes) != 1 {
			t.Fatalf("Expected flashes; Got %v", flashes)
		}
		custom := flashes[0].(FlashMessage)
		if custom.Type != 42 || custom.Message != "foo" {
			t.Errorf("Expected %#v, got %#v", FlashMessage{42, "foo"}, custom)
		}

		// RediStore specific
		// Set MaxAge to -1 to mark for deletion.
		session.Options.MaxAge = -1
		// Save.
		if err = sessions.Save(req, rsp); err != nil {
			t.Fatalf("Error saving session: %v", err)
		}
	}
	// Round 5 ----------------------------------------------------------------
	// change MaxLength of session

	{
		log.Println("Round 5")
		store, err := NewSqliteStore("test.db", 10, "sessions", &session_options, []byte("secret-key"))
		if err != nil {
			t.Fatal(err.Error())
		}
		defer func() {
			store.CloseDatabasePool()
		}()

		req, err := http.NewRequest("GET", "http://www.example.com", nil)
		if err != nil {
			t.Fatal("failed to create request", err)
		}
		w := httptest.NewRecorder()

		session, _ = store.New(req, "my session")
		session.Values["big"] = make([]byte, base64.StdEncoding.DecodedLen(4096*2))
		err = session.Save(req, w)
		if err == nil {
			t.Fatal("expected an error, got nil")
		}

		store.SetMaxLength(4096 * 3) // A bit more than the value size to account for encoding overhead.
		err = session.Save(req, w)
		if err != nil {
			t.Fatal("failed to Save:", err)
		}
	}

	// Round 6 ----------------------------------------------------------------

	{
		log.Println("Round 6")
		store, err := NewSqliteStore("test.db", 10, "sessions", &session_options, []byte("secret-key"))
		if err != nil {
			t.Fatal(err.Error())
		}
		defer func() {
			store.CloseDatabasePool()
		}()

		req, _ := http.NewRequest("GET", "http://localhost:8080/", nil)
		rsp = NewRecorder()
		// Get a session. Using the same key as previously, but on different DB
		if session, err = store.Get(req, "session-key"); err != nil {
			t.Fatalf("Error getting session: %v", err)
		}
		// Get a flash.
		flashes = session.Flashes()
		if len(flashes) != 0 {
			t.Errorf("Expected empty flashes; Got %v", flashes)
		}
		// Add some flashes.
		session.AddFlash("foo")
		// Save.
		if err = sessions.Save(req, rsp); err != nil {
			t.Fatalf("Error saving session: %v", err)
		}
		hdr = rsp.Header()
		cookies, ok = hdr["Set-Cookie"]
		if !ok || len(cookies) != 1 {
			t.Fatalf("No cookies. Header: %s", hdr)
		}

		// Get a session.
		req.Header.Add("Cookie", cookies[0])
		if session, err = store.Get(req, "session-key"); err != nil {
			t.Fatalf("Error getting session: %v", err)
		}
		// Check all saved values.
		flashes = session.Flashes()
		if len(flashes) != 1 {
			t.Fatalf("Expected flashes; Got %v", flashes)
		}
		if flashes[0] != "foo" {
			t.Errorf("Expected foo,bar; Got %v", flashes)
		}
	}

	// Round 7 ----------------------------------------------------------------
	// JSONSerializer
	{
		log.Println("Round 7")
		store, err := NewSqliteStore("test.db", 10, "sessions", &session_options, []byte("secret-key"))
		if err != nil {
			t.Fatal(err.Error())
		}
		defer func() {
			store.CloseDatabasePool()
		}()

		req, _ := http.NewRequest("GET", "http://localhost:8080/", nil)
		rsp = NewRecorder()
		// Get a session.
		if session, err = store.Get(req, "session-key"); err != nil {
			t.Fatalf("Error getting session: %v", err)
		}
		// Get a flash.
		flashes = session.Flashes()
		if len(flashes) != 0 {
			t.Errorf("Expected empty flashes; Got %v", flashes)
		}
		// Add some flashes.
		session.AddFlash("foo")
		// Save.
		if err = sessions.Save(req, rsp); err != nil {
			t.Fatalf("Error saving session: %v", err)
		}
		hdr = rsp.Header()
		cookies, ok = hdr["Set-Cookie"]
		if !ok || len(cookies) != 1 {
			t.Fatalf("No cookies. Header: %s", hdr)
		}

		// Get a session.
		req.Header.Add("Cookie", cookies[0])
		if session, err = store.Get(req, "session-key"); err != nil {
			t.Fatalf("Error getting session: %v", err)
		}
		// Check all saved values.
		flashes = session.Flashes()
		if len(flashes) != 1 {
			t.Fatalf("Expected flashes; Got %v", flashes)
		}
		if flashes[0] != "foo" {
			t.Errorf("Expected foo,bar; Got %v", flashes)
		}
	}

}

func init() {
	gob.Register(FlashMessage{})
}

func TestCleanup(t *testing.T) {
	store, err := NewSqliteStore("test.db", 10, "sessions", &session_options, []byte("secret-key"))
	if err != nil {
		t.Fatal(err.Error())
	}
	defer func() {
		store.CloseDatabasePool()
		os.Remove("test.db")
		os.Remove("test.db-shm")
		os.Remove("test.db-wal")

	}()

	// Start the cleanup goroutine.
	defer store.StopCleanup(store.StartCleanup(time.Millisecond * 500))

	req, err := http.NewRequest("GET", "http://www.example.com", nil)
	if err != nil {
		t.Fatal("Failed to create request", err)
	}

	session, err := store.Get(req, "newsess")
	if err != nil {
		t.Fatal("Failed to create session", err)
	}

	// Expire the session.
	session.Options.MaxAge = 1

	m := make(http.Header)
	if err = store.Save(req, headerOnlyResponseWriter(m), session); err != nil {
		t.Fatal("failed to save session:", err.Error())
	}

	// Give the ticker a moment to run.
	time.Sleep(time.Second * 5)

	// SELECT expired sessions. We should get a count of zero back.
	var count int
	c := store.DbPool.Get(context.TODO())
	stmt := c.Prep(fmt.Sprintf("SELECT count(*) as cnt FROM %s WHERE expires_on < ?", store.tableName))
	stmt.BindInt64(1, time.Now().Unix())
	defer store.DbPool.Put(c)
	_, err = stmt.Step()
	if err != nil {
		t.Fatalf("failed to select expired sessions from store: %v", err)
	}
	count = int(stmt.GetInt64("cnt"))
	stmt.Reset()
	if count > 0 {
		t.Fatalf("ticker did not delete expired sessions: want 0 got %v", count)
	}
}
