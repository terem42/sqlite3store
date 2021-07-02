# sqlite3store
Thread-safe high-performant SQLite3 session store backend for [gorilla/sessions](http://www.gorillatoolkit.org/pkg/sessions) - [src](https://github.com/gorilla/sessions).
Unlike other Sqlite session stores implementation, usable in real app scenarios, because it uses connection pool and statement caching, SQLite db runs in shared cache and WAL log, ensuring threads do not block each other and statements are processed very quickly


### Example

Minimal http server with session store usage example, detailed info can be found at [gorilla/sessions](http://www.gorillatoolkit.org/pkg/sessions) repository

```go
package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/terem42/sqlite3store"
	"github.com/gorilla/sessions"
)

var store *sqlite3store.SqliteStore


func handler(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "session-name")
	// Set some session values.
	if session.Values["foo"] == nil {
		session.Values["foo"] = "=>"
	}
	session.Values["foo"] = "=" + session.Values["foo"].(string)
	// Save it before we write to the response/return from the handler.
	err := session.Save(r, w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, session.Values["foo"].(string))
}

func main() {
	var err error
        session_options := sessions.Options{
	  Path:     "/",
	  Domain:   "localhost",
	  MaxAge:   600,
	  Secure:   false,
	  HttpOnly: false,
	  SameSite: http.SameSiteDefaultMode,
        }

	store, err = sqlite3store.NewSqliteStore("test.db", 10, "sessions", &session_options, []byte("my-session-secret"))
	if err != nil {
		log.Fatalf(err.Error())
	}
	http.HandleFunc("/", handler)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
```
