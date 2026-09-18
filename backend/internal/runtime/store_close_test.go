package runtime

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreCloseWaitsForConnectionRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "close.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db}
	defer store.Close()
	connection, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), `CREATE TABLE fixture (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- store.Close() }()
	select {
	case err := <-done:
		t.Fatalf("Close returned before its connection was released: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not finish after releasing the connection")
	}
	if db.Stats().OpenConnections != 0 {
		t.Fatal("Close left SQLite connections open")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestStoreCloseTimeoutIsExplicitAndCanBeDrained(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "timeout.db"))
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db}
	defer store.Close()
	connection, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := store.closeContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("leased connection did not cause a bounded close error: %v", err)
	}
	if err := db.Ping(); err == nil {
		t.Fatal("closing store accepted another query")
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil || db.Stats().OpenConnections != 0 {
		t.Fatalf("connection was not drained on the next close: %v", err)
	}
}

func TestSQLiteCancelledQueriesReleaseDatabaseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cancelled-queries.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db}
	defer store.Close()
	if _, err := db.Exec(`CREATE TABLE fixture (id INTEGER); INSERT INTO fixture VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(time.Duration(i%20)*time.Microsecond, cancel)
		rows, queryErr := db.QueryContext(ctx, `SELECT id, id, id, id, id, id, id, id FROM fixture`)
		if rows != nil {
			rows.Close()
		}
		timer.Stop()
		cancel()
		if queryErr != nil && !errors.Is(queryErr, context.Canceled) && !strings.Contains(queryErr.Error(), "interrupted") {
			t.Fatal(queryErr)
		}
	}
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("database invalid after cancellation: %q, %v", integrity, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("cancelled query retained a database handle: %v", err)
	}
}
