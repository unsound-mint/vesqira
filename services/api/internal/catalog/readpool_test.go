package catalog

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The read pool sees what the writer committed and cannot write.
func TestReadPoolReadsAndRefusesWrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	st, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ro, err := OpenReadPool(p, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := st.Upsert(Dataset{Path: "/d/a.fits", Name: "a.fits", Header: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := ro.QueryRow(`SELECT COUNT(*) FROM datasets`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("read pool: %d %v", n, err)
	}
	if _, err := ro.Exec(`DELETE FROM datasets`); err == nil {
		t.Fatal("the read pool wrote")
	}
}

// Waiting for the writer connection is counted and reported.
func TestWriterWaitsAreReported(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var got int64
	go WriterWaits(ctx, st.DB(), 20*time.Millisecond, func(n int64, _ time.Duration) { mu.Lock(); got += n; mu.Unlock() })
	time.Sleep(10 * time.Millisecond) // let it take its first reading
	tx, err := st.DB().Begin()        // holds the one connection
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _, _ = st.DB().Exec(`SELECT 1`); close(done) }()
	time.Sleep(30 * time.Millisecond)
	_ = tx.Rollback()
	<-done
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		g := got
		mu.Unlock()
		if g > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a wait for the writer was not reported")
}
