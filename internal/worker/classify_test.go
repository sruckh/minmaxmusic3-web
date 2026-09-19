package worker

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/runpod"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// claimQueuedJob puts one job into the local `submitting` state — the state a
// job is in while a POST /run is in flight, and the only state classifySubmit
// ever sees.
func claimQueuedJob(t *testing.T, w *Worker) *store.Job {
	t.Helper()
	j := &store.Job{
		ID: newID(), State: store.StateQueued, UserID: "u1",
		Lyrics: "[Verse]\nx", Caption: "pop", Duration: 30,
		CreatedAt: time.Now().UTC(),
	}
	if err := w.st.CreateJob(j); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := w.st.TransitionJob(j.ID, store.StateQueued, store.StateSubmitting, nil); err != nil {
		t.Fatalf("claiming the job: %v", err)
	}
	j.State = store.StateSubmitting
	return j
}

// queuedIDs lists the ids the worker would pick up on its next tick.
func queuedIDs(t *testing.T, w *Worker) []string {
	t.Helper()
	jobs, err := w.st.DequeueQueued(10)
	if err != nil {
		t.Fatalf("DequeueQueued: %v", err)
	}
	ids := make([]string, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	return ids
}

func newClassifyWorker(t *testing.T) *Worker {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Worker{st: st, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// A refused submission that RunPod says was never enqueued goes back to the
// queue and is tried again, rather than being failed.
//
// This is the behaviour a live 409 exposed: an endpoint scaled to zero answers
// `ENDPOINT_PAUSED`, and treating that as an ambiguous failure rejected the job
// outright instead of letting it wait for capacity — which is what the queue
// budget is for. Raising max_workers then lets the queued work through.
func TestARefusedButUnenqueuedSubmissionReturnsToTheQueue(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantQueued bool
	}{
		{
			// Observed live, verbatim from RunPod.
			name: "endpoint paused",
			err: &runpod.Error{StatusCode: http.StatusConflict,
				Body: `{"status":409,"title":"Conflict","detail":"Endpoint is paused (max_workers=0). Set max_workers > 0 to accept work.","code":"ENDPOINT_PAUSED"}`},
			wantQueued: true,
		},
		{
			name:       "rate limited",
			err:        &runpod.Error{StatusCode: http.StatusTooManyRequests},
			wantQueued: true,
		},
		{
			name:       "endpoint refusing work",
			err:        &runpod.Error{StatusCode: http.StatusServiceUnavailable},
			wantQueued: true,
		},
		{
			name:       "accepted but no job id",
			err:        runpod.ErrNoJobID,
			wantQueued: true,
		},
		{
			// The dangerous case, and the reason this predicate is not simply
			// "not permanent": the request may have landed, so a retry could
			// bill a second generation. It must not return to the queue.
			name:       "server error, outcome unknown",
			err:        &runpod.Error{StatusCode: http.StatusInternalServerError},
			wantQueued: false,
		},
		{
			name:       "transport failure, outcome unknown",
			err:        errors.New("connection reset by peer"),
			wantQueued: false,
		},
		{
			// Definitive rejection, and retrying changes nothing.
			name:       "bad request",
			err:        &runpod.Error{StatusCode: http.StatusBadRequest},
			wantQueued: false,
		},
		{
			name:       "unauthorized",
			err:        &runpod.Error{StatusCode: http.StatusForbidden},
			wantQueued: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newClassifyWorker(t)
			j := claimQueuedJob(t, w)

			w.classifySubmit(j, tc.err)

			queued := false
			for _, id := range queuedIDs(t, w) {
				if id == j.ID {
					queued = true
				}
			}
			if queued != tc.wantQueued {
				t.Errorf("back in the queue = %v, want %v (err: %v)", queued, tc.wantQueued, tc.err)
			}

			// A failed submission must still be legible: the reason has to say
			// which kind of failure it was, because "ambiguous" and "rejected"
			// call for different responses from whoever reads the log.
			if !tc.wantQueued {
				got, err := w.st.Job(j.ID, store.Access{Admin: true})
				if err != nil {
					t.Fatalf("reading the job back: %v", err)
				}
				if got.State != store.StateFailed {
					t.Errorf("state = %q, want %q", got.State, store.StateFailed)
				}
				if got.Error == "" {
					t.Error("a failed submission must record why")
				}
			}
		})
	}
}

// Retrying a refusal must be counted, because the count is what spaces the
// retries — an uncounted retry would resubmit on every tick.
func TestARefusedSubmissionIsCountedSoTheBackoffAdvances(t *testing.T) {
	w := newClassifyWorker(t)
	j := claimQueuedJob(t, w)

	if d := submitDelay(j.Retries); d != 0 {
		t.Fatalf("first attempt should not wait, got %s", d)
	}
	w.classifySubmit(j, &runpod.Error{StatusCode: http.StatusConflict})

	after, err := w.st.Job(j.ID, store.Access{Admin: true})
	if err != nil {
		t.Fatalf("reading the job back: %v", err)
	}
	if after.Retries <= j.Retries {
		t.Errorf("Retries = %d, want more than %d — the next attempt would not back off",
			after.Retries, j.Retries)
	}
	if d := submitDelay(after.Retries); d == 0 {
		t.Error("the retry would fire immediately")
	}
}
