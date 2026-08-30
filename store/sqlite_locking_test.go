package store

import (
	"runtime"
	"testing"
	"time"
)

// A queued writer makes sync.RWMutex block subsequent RLock calls. Get and
// GetCoreSession already hold a read lock while restoring their sequence
// counters, so the counter helpers must not recursively acquire the same lock.
func TestSQLiteSequenceHelpersDoNotDeadlockBehindQueuedWriter(t *testing.T) {
	st, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	st.mu.RLock()
	writerStarted := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		close(writerStarted)
		st.mu.Lock()
		st.mu.Unlock()
		close(writerDone)
	}()
	<-writerStarted
	// Give the writer a chance to queue. Repeated scheduling keeps this test
	// deterministic under both ordinary and race-enabled test runs.
	for range 100 {
		runtime.Gosched()
	}

	readDone := make(chan struct{})
	go func() {
		_ = st.getMaxSeqIDForSession("missing")
		_ = st.getMaxToolSeqForSession("missing")
		close(readDone)
	}()

	select {
	case <-readDone:
		st.mu.RUnlock()
	case <-time.After(time.Second):
		st.mu.RUnlock()
		t.Fatal("sequence helpers attempted a recursive RLock behind a queued writer")
	}

	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("queued writer did not acquire the store lock")
	}
}
