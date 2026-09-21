package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// fastFetchWorker is a worker whose retry backoff is short enough to test.
func fastFetchWorker(t *testing.T) *Worker {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Worker{
		st: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		fetchBackoff: time.Millisecond,
	}
}

// A single transient failure must not cost the artifact. This is the exact
// shape of the live incident: one TLS handshake timeout discarded a
// four-minute generation whose bytes were sitting in the bucket, valid for
// days, entirely undamaged.
func TestFetchRetriesATransientFailure(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// A transport-level failure, as the live one was: the connection
			// dies before any response exists.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("test server cannot hijack")
			}
			conn, _, err := hj.Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		_, _ = w.Write([]byte("audio-bytes"))
	}))
	defer srv.Close()

	w := fastFetchWorker(t)
	got, err := w.fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(got) != "audio-bytes" {
		t.Errorf("body = %q, want audio-bytes", got)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("server saw %d calls, want 2 — one failure then one success", n)
	}
}

// A server error is worth another try; the next answer may differ.
func TestFetchRetriesA5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	w := fastFetchWorker(t)
	got, err := w.fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(got) != "ok" {
		t.Errorf("body = %q, want ok", got)
	}
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Errorf("server saw %d calls, want 3", n)
	}
}

// A refusal is an answer, and it will not change. Retrying a 403 or a 404 just
// burns the job's budget before reporting the same thing.
func TestFetchDoesNotRetryARefusal(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusNotFound,
		http.StatusUnauthorized, http.StatusBadRequest} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var calls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				w.WriteHeader(code)
			}))
			defer srv.Close()

			w := fastFetchWorker(t)
			_, err := w.fetch(context.Background(), srv.URL)
			if err == nil {
				t.Fatal("a refusal must be an error")
			}
			if n := atomic.LoadInt32(&calls); n != 1 {
				t.Errorf("server saw %d calls, want 1 — a %d will not change", n, code)
			}
			if !permanentFetch(err) {
				t.Errorf("%d should classify as permanent", code)
			}
		})
	}
}

// The two 4xx answers that do invite a retry.
func TestFetchRetriesTheInviting4xx(t *testing.T) {
	for _, code := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var calls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if atomic.AddInt32(&calls, 1) == 1 {
					w.WriteHeader(code)
					return
				}
				_, _ = w.Write([]byte("ok"))
			}))
			defer srv.Close()

			w := fastFetchWorker(t)
			if _, err := w.fetch(context.Background(), srv.URL); err != nil {
				t.Fatalf("fetch: %v", err)
			}
			if n := atomic.LoadInt32(&calls); n != 2 {
				t.Errorf("server saw %d calls, want 2", n)
			}
		})
	}
}

// Retrying is bounded, and the error says how hard it tried — otherwise a
// permanent outage reads like a single unlucky request.
func TestFetchGivesUpAfterTheAttemptBudget(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	w := fastFetchWorker(t)
	_, err := w.fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("an unreachable artifact must be an error")
	}
	if !strings.Contains(err.Error(), fmt.Sprint(fetchAttempts)) {
		t.Errorf("error does not say how many attempts were made: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != fetchAttempts {
		t.Errorf("server saw %d calls, want %d", n, fetchAttempts)
	}
}

// A cancelled context stops the retries rather than grinding through the whole
// budget after the process has been told to shut down.
func TestFetchStopsRetryingWhenTheContextIsDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	w := &Worker{st: nil, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		fetchBackoff: time.Hour} // long enough that only cancellation can end it
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := w.fetch(ctx, srv.URL); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// completeJob writes a job in the running state, as it would be when RunPod
// first reports COMPLETED.
func completeJob(t *testing.T, w *Worker) *store.Job {
	t.Helper()
	j := &store.Job{
		ID: newID(), State: store.StateQueued, UserID: "u1",
		Lyrics: "[Verse]\nx", Caption: "pop", Duration: 30,
		CreatedAt: time.Now().UTC(),
	}
	if err := w.st.CreateJob(j); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	started := time.Now().UTC()
	if err := w.st.TransitionJob(j.ID, store.StateQueued, store.StateRunning,
		func(a map[string]any) { a["started_at"] = started }); err != nil {
		t.Fatalf("starting the job: %v", err)
	}
	j.State = store.StateRunning
	return j
}

func jobState(t *testing.T, w *Worker, id string) (*store.Job, string) {
	t.Helper()
	got, err := w.st.Job(id, store.Access{Admin: true})
	if err != nil {
		t.Fatalf("reading the job back: %v", err)
	}
	return got, got.State
}

// A finished generation whose artifact cannot be stored is kept alive for
// another attempt, not thrown away. The artifact is the whole point of the job
// and the GPU time is already spent.
func TestAStorageFailureKeepsTheJobAliveUntilTheRetryBudgetIsSpent(t *testing.T) {
	w := fastFetchWorker(t)
	j := completeJob(t, w)

	// The first failures keep it running.
	for i := 1; i < maxStoreRetries; i++ {
		w.storeFailure(context.Background(), j, errors.New("boom"))
		got, state := jobState(t, w, j.ID)
		if state == store.StateFailed {
			t.Fatalf("job failed after %d storage failures, want it retried until %d",
				i, maxStoreRetries)
		}
		if got.Retries != i {
			t.Errorf("Retries = %d after %d failures, want %d", got.Retries, i, i)
		}
	}

	// The last one gives up, and says why.
	w.storeFailure(context.Background(), j, errors.New("boom"))
	got, state := jobState(t, w, j.ID)
	if state != store.StateFailed {
		t.Fatalf("state = %q after %d failures, want failed", state, maxStoreRetries)
	}
	if !strings.Contains(got.Error, "store:") {
		t.Errorf("the failure must name storage as the cause, got %q", got.Error)
	}
}

// The count survives a restart because it lives in the row, so a crash loop
// cannot retry forever.
func TestStorageRetriesAreCountedDurably(t *testing.T) {
	w := fastFetchWorker(t)
	j := completeJob(t, w)

	for i := 0; i < 3; i++ {
		w.storeFailure(context.Background(), j, errors.New("boom"))
	}
	// A fresh Worker over the same store — the restart case.
	fresh := &Worker{st: w.st, log: w.log, fetchBackoff: time.Millisecond}
	got, _ := jobState(t, fresh, j.ID)
	if got.Retries != 3 {
		t.Errorf("Retries = %d after a restart, want 3", got.Retries)
	}
}
