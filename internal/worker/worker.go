// Package worker owns all RunPod traffic: it drains queued jobs, submits
// them async, polls status, fetches/stores audio, and writes songs rows.
// The browser never talks to RunPod (stage 02 contract).
package worker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/audio"
	"github.com/sruckh/minmaxmusic3-web/internal/runpod"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

const (
	submitTick = 2 * time.Second
	pollFast   = 3 * time.Second // first minute
	pollSlow   = 10 * time.Second

	// Two budgets, not one. RunPod GPU availability is unreliable, so a job
	// can sit in IN_QUEUE for a long stretch through no fault of its own,
	// while generation itself is quick once a worker picks it up. A single
	// clock from created_at spent its whole budget on the wait and then killed
	// the run it had just finished paying for. Waiting is now cheap and
	// generous; running is tight.
	queueBudget = 45 * time.Minute // submitting + IN_QUEUE, from created_at
	runBudget   = 8 * time.Minute  // from the first IN_PROGRESS

	// Backoff for a submission RunPod definitively refused. Doubles per
	// consecutive rejection; the cap keeps a job checking often enough to
	// claim capacity soon after it frees up.
	submitBackoff    = 15 * time.Second
	maxSubmitBackoff = 2 * time.Minute

	maxTransient = 3 // consecutive transient failures while polling
)

// expired reports whether a job has outlived the budget that applies to it,
// and why. Which budget that is turns on whether a GPU ever picked the job
// up: before the first IN_PROGRESS it is only queueing, after it the job is
// burning GPU time and a short leash is right.
func expired(j *store.Job) (bool, string) {
	if j.StartedAt == nil {
		return time.Since(j.CreatedAt) > queueBudget,
			"timeout: no GPU capacity within " + queueBudget.String()
	}
	return time.Since(*j.StartedAt) > runBudget,
		"timeout: generation exceeded " + runBudget.String()
}

// submitDelay spaces retries of a refused submission: 15s, 30s, 60s, 2m, 2m…
// There is no attempt cap — queueBudget is the cap. Three tries across six
// seconds never outlived a GPU shortage. A job that has not been refused yet
// waits for nothing: retries == 0 is the first attempt, not a retry.
func submitDelay(retries int) time.Duration {
	if retries <= 0 {
		return 0
	}
	d := time.Duration(submitBackoff)
	for range retries - 1 {
		if d >= maxSubmitBackoff {
			break
		}
		d *= 2
	}
	return min(d, maxSubmitBackoff)
}

// Worker is the background submitter/poller pair.
type Worker struct {
	st          *store.Store
	rp          *runpod.Client
	log         *slog.Logger
	audioDir    string
	maxInFlight int
}

func New(st *store.Store, rp *runpod.Client, log *slog.Logger, audioDir string, maxInFlight int) *Worker {
	return &Worker{st: st, rp: rp, log: log, audioDir: audioDir, maxInFlight: maxInFlight}
}

// Run blocks until ctx is done. Call once at boot, after Start().
func (w *Worker) Run(ctx context.Context) {
	// Restart recovery (stage 02 §C): a crash in the non-atomic window
	// between remote submission and durable runpod_id leaves `submitting`.
	// Fail it as ambiguous — never resubmit and risk a duplicate bill.
	w.failAmbiguousSubmissions(ctx)
	// Rows with a durable runpod_id resume polling; stale active rows fail.
	w.failExpired(ctx)

	tick := time.NewTicker(submitTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			w.pollActive(ctx)
			w.submitQueued(ctx)
		}
	}
}

func (w *Worker) failAmbiguousSubmissions(ctx context.Context) {
	jobs, err := w.st.SubmittingJobs()
	if err != nil {
		w.log.Error("worker: ambiguous-submission scan", "err", err)
		return
	}
	for _, j := range jobs {
		reason := "submit-ambiguous: process stopped before the remote id was durable; not resubmitting"
		if err := w.st.OrphanSubmission(j.ID, j.RunPodID, reason); err != nil {
			w.log.Error("worker: failing ambiguous submission", "job", j.ID, "err", err)
		}
	}
	_ = ctx // reserved for best-effort cancel when a durable id exists
}

func (w *Worker) failExpired(ctx context.Context) {
	jobs, err := w.st.ActiveJobs()
	if err != nil {
		w.log.Error("worker: recovery scan", "err", err)
		return
	}
	for _, j := range jobs {
		// Same test the poll loop uses, rather than a last-progress heuristic:
		// with a generous queue budget, wall-clock age is now both accurate and
		// forgiving enough to survive an ordinary restart.
		if over, why := expired(j); over {
			w.fail(ctx, j, why)
		}
	}
}

func (w *Worker) submitQueued(ctx context.Context) {
	// The in-flight cap counts jobs already submitted or running; only the
	// remaining budget may leave the queue this tick.
	active, err := w.st.ActiveJobs()
	if err != nil {
		w.log.Error("worker: active scan", "err", err)
		return
	}
	budget := w.maxInFlight - len(active)
	if budget <= 0 {
		return
	}
	jobs, err := w.st.DequeueQueued(budget)
	if err != nil {
		w.log.Error("worker: dequeue", "err", err)
		return
	}
	for _, j := range jobs {
		if j.RunPodID != "" {
			continue // idempotent: never resubmit
		}
		// Capacity never arrived. Fail here rather than in pollActive: this row
		// has no runpod_id, so there is nothing remote to cancel.
		if over, why := expired(j); over {
			w.log.Warn("worker: queue budget exhausted", "job", j.ID,
				"waited", time.Since(j.CreatedAt).Round(time.Second),
				"attempts", j.Retries)
			w.fail(ctx, j, why)
			continue
		}
		// A refused submission waits out its backoff in `queued`.
		if time.Since(j.UpdatedAt) < submitDelay(j.Retries) {
			continue
		}
		// Claim locally BEFORE the remote call. A crash after this point
		// leaves `submitting`, which restart recovery fails as ambiguous
		// rather than resubmitting a potentially billable remote job.
		if err := w.st.TransitionJob(j.ID, store.StateQueued, store.StateSubmitting, nil); err != nil {
			continue
		}
		j.State = store.StateSubmitting

		id, err := w.rp.Submit(ctx, &runpod.Request{
			Lyrics:       j.Lyrics,
			Instructions: j.Caption,
			AudioDur:     j.Duration,
			Seed:         j.Seed,
		})
		if err != nil {
			w.classifySubmit(j, err)
			continue
		}
		if err := w.st.TransitionJob(j.ID, store.StateSubmitting, store.StateSubmitted,
			func(a map[string]any) { a["runpod_id"] = id; a["retries"] = 0 }); err != nil {
			// The remote id exists but the normal CAS failed. Cancel it and
			// durably record both the id and failure — never lose the id and
			// never make the row eligible for resubmission.
			w.rp.Cancel(ctx, id)
			reason := "submit-cas: " + err.Error()
			if oerr := w.st.OrphanSubmission(j.ID, id, reason); oerr != nil {
				w.log.Error("worker: recording orphaned submission", "job", j.ID,
					"runpod_id", id, "err", oerr)
			}
		}
	}
}

func (w *Worker) pollActive(ctx context.Context) {
	jobs, err := w.st.ActiveJobs()
	if err != nil {
		w.log.Error("worker: active scan", "err", err)
		return
	}
	for _, j := range jobs {
		if j.RunPodID == "" {
			continue
		}
		// Budget before cadence: an expired job must not wait out a poll slot
		// before anyone notices it is over.
		if over, why := expired(j); over {
			w.rp.Cancel(ctx, j.RunPodID)
			w.fail(ctx, j, why)
			continue
		}
		if time.Since(j.UpdatedAt) < w.pollDelay(j) {
			continue
		}
		sr, err := w.rp.Status(ctx, j.RunPodID)
		if err != nil {
			w.classify(ctx, j, err)
			continue
		}
		w.applyStatus(ctx, j, sr)
	}
}

// pollDelay: fast while young, slow after the first minute.
func (w *Worker) pollDelay(j *store.Job) time.Duration {
	if time.Since(j.CreatedAt) < time.Minute {
		return pollFast
	}
	return pollSlow
}

// applyStatus maps every RunPod status to exactly one transition (closed
// machine, stage 02 §C).
func (w *Worker) applyStatus(ctx context.Context, j *store.Job, sr *runpod.StatusResponse) {
	from := store.StateSubmitted
	if j.State == store.StateRunning {
		from = store.StateRunning
	}
	switch sr.Status {
	case runpod.StatusInQueue:
		// stay submitted; touch updated_at so the budget clock is honest
		// and reset the retry counter — progress of any kind clears it.
		_ = w.st.TransitionJob(j.ID, from, store.StateSubmitted,
			func(a map[string]any) { a["retries"] = 0 })
	case runpod.StatusInProgress:
		_ = w.st.TransitionJob(j.ID, from, store.StateRunning,
			func(a map[string]any) {
				a["retries"] = 0
				// Stamped once. The run budget measures from the first
				// IN_PROGRESS, not from the latest poll that saw one.
				if j.StartedAt == nil {
					a["started_at"] = time.Now().UTC()
				}
			})
	case runpod.StatusCompleted:
		out, err := runpod.OutputOf(sr)
		if err != nil {
			w.fail(ctx, j, "schema: "+err.Error())
			return
		}
		// Store the result FIRST, then CAS to succeeded. finish() is
		// idempotent (SongForJob check + unique index), so if the CAS
		// fails the next poll re-runs it harmlessly; but a CAS-first order
		// would orphan a "succeeded" job with no audio.
		if err := w.finish(ctx, j, out); err != nil {
			w.log.Error("worker: storing song", "job", j.ID, "err", err)
			w.fail(ctx, j, "store: "+err.Error())
			return
		}
		_ = w.st.TransitionJob(j.ID, from, store.StateSucceeded,
			func(a map[string]any) { a["retries"] = 0 })
	case runpod.StatusFailed:
		w.fail(ctx, j, "worker: "+runpod.ErrorText(sr.Error))
	case runpod.StatusCancelled:
		_ = w.st.FailJob(j.ID, "cancelled")
	default:
		w.fail(ctx, j, "schema: unknown status "+sr.Status)
	}
}

// classifySubmit is deliberately stricter than poll retry handling. A
// network error or 5xx after POST /run is ambiguous: RunPod may have accepted
// the job. Retrying could bill twice, so only an explicit 429 is retried; all
// other errors fail the locally-claimed submission without resubmission.
func (w *Worker) classifySubmit(j *store.Job, err error) {
	// Retryable here means RunPod definitively enqueued nothing, so returning
	// the row to `queued` cannot produce a duplicate billable generation.
	// Everything else — including any transport error, where it is unknowable
	// whether the POST landed — still fails as ambiguous and is never
	// resubmitted. queueBudget, not an attempt count, decides when to stop.
	if runpod.IsRetryableSubmit(err) {
		if _, berr := w.st.BumpRetries(j.ID); berr != nil {
			w.log.Error("worker: submit retry count", "job", j.ID, "err", berr)
			return
		}
		if terr := w.st.TransitionJob(j.ID, store.StateSubmitting, store.StateQueued, nil); terr == nil {
			return
		}
	}
	reason := "submit-rejected: " + err.Error()
	if !runpod.IsPermanent(err) {
		reason = "submit-ambiguous: " + err.Error()
	}
	if ferr := w.st.OrphanSubmission(j.ID, "", reason); ferr != nil {
		w.log.Error("worker: failing submission", "job", j.ID, "err", ferr)
	}
}

// classify handles poll errors per the taxonomy: permanent ⇒ fail,
// transient ⇒ count to 3 then fail.
func (w *Worker) classify(ctx context.Context, j *store.Job, err error) {
	if runpod.IsPermanent(err) {
		w.fail(ctx, j, err.Error())
		return
	}
	n, berr := w.st.BumpRetries(j.ID)
	if berr != nil {
		w.log.Error("worker: bump retries", "job", j.ID, "err", berr)
		return
	}
	if n >= maxTransient {
		w.fail(ctx, j, "transient: "+err.Error())
	}
}

func (w *Worker) fail(ctx context.Context, j *store.Job, reason string) {
	if err := w.st.FailJob(j.ID, reason); err != nil && !errors.Is(err, store.ErrTransition) {
		w.log.Error("worker: fail job", "job", j.ID, "err", err)
	}
	w.log.Warn("worker: job failed", "job", j.ID, "reason", reason)
}

// finish stores the audio locally and writes the songs row (stage 02 §A3).
// Idempotent: one song per job, enforced by the unique index.
func (w *Worker) finish(ctx context.Context, j *store.Job, out *runpod.Output) error {
	if existing, _ := w.st.SongForJob(j.ID); existing != nil {
		return nil
	}
	var data []byte
	switch out.Delivery {
	case "s3":
		b, err := w.fetch(ctx, out.AudioURL)
		if err != nil {
			return fmt.Errorf("fetching s3 audio: %w", err)
		}
		data = b
	case "base64":
		b, err := base64.StdEncoding.DecodeString(out.InlineB64())
		if err != nil {
			return fmt.Errorf("decoding base64 audio: %w", err)
		}
		data = b
	default:
		raw, _ := json.Marshal(out)
		w.log.Warn("worker: unknown delivery — full output recorded",
			"delivery", out.Delivery, "output", string(raw))
		return fmt.Errorf("unknown delivery %q", out.Delivery)
	}
	if err := os.MkdirAll(w.audioDir, 0o750); err != nil {
		return err
	}
	songID := newID()
	path := filepath.Join(w.audioDir, songID+".m4a")
	if err := audio.EncodeWAVToM4A(ctx, data, path); err != nil {
		return fmt.Errorf("transcoding audio to m4a: %w", err)
	}
	// The song inherits the job's owner — otherwise every generated song would
	// land on the legacy owner and be invisible to the user who asked for it.
	return w.st.CreateSong(&store.Song{
		ID: songID, JobID: j.ID, UserID: j.UserID,
		Lyrics: j.Lyrics, Caption: j.Caption,
		Duration: out.Duration, Seed: j.Seed, Engine: out.Engine,
		Delivery: out.Delivery, AudioPath: path, Title: titleOf(j),
		CreatedAt: time.Now().UTC(),
	})
}

// fetchTimeout bounds one s3 audio download; a hung read must never
// freeze the worker loop (same discipline as runpod.callTimeout).
const fetchTimeout = 2 * time.Minute

var fetchClient = &http.Client{Timeout: fetchTimeout}

func (w *Worker) fetch(ctx context.Context, url string) ([]byte, error) {
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := fetchClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// titleOf is what the song is filed under in History. The user's own title
// wins; the caption-derived fallback is only for jobs submitted without one,
// which includes every job predating the title field.
func titleOf(j *store.Job) string {
	if t := strings.TrimSpace(j.Title); t != "" {
		return t
	}
	for _, line := range splitLines(j.Caption) {
		if len(line) > 60 {
			line = line[:60]
		}
		if line != "" {
			return line
		}
	}
	return "Untitled"
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[:])
}

// NewJobID is exported for handlers.
func NewJobID() string { return newID() }
