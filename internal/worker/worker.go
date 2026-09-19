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

	// The running budget is per-mode, not one number. See runBudgetFor.
	//
	// Each is set from what the mode actually costs, with headroom: a YuE2
	// create measures ~71 s on a 4090, so 20 minutes is not a tight leash but a
	// wedged-job detector. Cover is the one mode that genuinely approaches the
	// worker's own JOB_TIMEOUT_SECONDS (30 min by default), and it is set above
	// that deliberately — the worker knows better than we do why a cover is
	// slow, so when the two race it should be the worker that gives up first
	// and names the reason. Our timeout winning would replace a specific
	// failure with a generic one. Give a mode more room here and the endpoint's
	// JOB_TIMEOUT_SECONDS has to move with it.
	runBudgetMiniMax   = 8 * time.Minute
	runBudgetYue2      = 20 * time.Minute
	runBudgetYue2Cover = 45 * time.Minute

	// Backoff for a submission RunPod definitively refused. Doubles per
	// consecutive rejection; the cap keeps a job checking often enough to
	// claim capacity soon after it frees up.
	submitBackoff    = 15 * time.Second
	maxSubmitBackoff = 2 * time.Minute

	maxTransient = 3 // consecutive transient failures while polling

	// How many consecutive storage failures a completed job survives before it
	// is given up on. Separate from maxTransient because the cost of being
	// wrong differs: a failed poll can be retried by submitting again, while
	// this artifact cost GPU time that will not come back.
	maxStoreRetries = 5
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
	budget := runBudgetFor(j)
	return time.Since(*j.StartedAt) > budget,
		"timeout: generation exceeded " + budget.String()
}

// runBudgetFor is the generation leash for one job. It depends on what the job
// asks the worker to do, which is a property of the mode rather than of the
// engine alone.
//
// YuE2's cover mode is the outlier that makes a single constant untenable: it
// transcribes the source recording with SheetSage2 and then Qwen3-ASR — each in
// its own virtualenv, each releasing its VRAM before the next starts — and only
// then generates anything. The worker budgets those two stages at 900 s and
// 600 s, so a cover can legitimately spend 25 minutes before generation begins.
// A MiniMax-sized leash would kill it mid-transcription and report a timeout
// for work that was progressing normally.
func runBudgetFor(j *store.Job) time.Duration {
	if j.Engine != store.EngineYue2 {
		return runBudgetMiniMax
	}
	if j.Mode == store.ModeCover {
		return runBudgetYue2Cover
	}
	return runBudgetYue2
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
	clients     map[string]*runpod.Client
	log         *slog.Logger
	audioDir    string
	maxInFlight int
	// fetchBackoff overrides the pause between artifact-download attempts.
	// Zero means the package default; tests set it so a retry path does not
	// make the suite wait out real backoff.
	fetchBackoff time.Duration
}

// New takes one client per engine, keyed by the store's engine constants. An
// engine absent from the map is one that is not configured, and jobs naming it
// fail with a reason rather than being posted to the wrong endpoint.
func New(st *store.Store, clients map[string]*runpod.Client, log *slog.Logger, audioDir string, maxInFlight int) *Worker {
	return &Worker{st: st, clients: clients, log: log, audioDir: audioDir, maxInFlight: maxInFlight}
}

// clientFor resolves the endpoint a job runs on. Returning nil is a real
// outcome, not a defensive branch: an engine can be configured at boot and
// absent later, and posting YuE2's payload to MiniMax would be billed and
// rejected as a schema error rather than as the misconfiguration it is.
func (w *Worker) clientFor(j *store.Job) *runpod.Client {
	return w.clients[j.Engine]
}

// requestFor builds the submit payload for a job's engine. The two engines have
// disjoint field vocabularies — MiniMax takes `instructions` and a duration,
// YuE2 takes a `style` and has no duration parameter at all — so the shape is
// chosen here rather than adapted inside the client.
func requestFor(j *store.Job) any {
	if j.Engine == store.EngineYue2 {
		mode := j.Mode
		if mode == store.ModeCreate {
			// The worker's own default; sending it would only restate it.
			mode = ""
		}
		// No words means instrumental, but only for create: a cover's lyrics
		// come from the recording, so an empty field there means "transcribe
		// them", not "no vocals". The two must not be conflated, because the
		// worker rejects the contradiction of a flag and words together rather
		// than guessing which was meant.
		// The flag is stored, not inferred. An empty lyric box means "no words"
		// only because the form says so; on its own it is equally consistent
		// with "not typed yet", and the worker reads a present-but-empty lyrics
		// field as a supplied empty lyric — which it refuses outright.
		//
		// Cover is excluded regardless: a cover with no words means "transcribe
		// the recording", the opposite of instrumental.
		instrumental := j.Instrumental && j.Mode != store.ModeCover
		// An external score needs a plan to hang off, so the worker refuses
		// `abc` together with `cot="off"` — "off" writes no plan at all. The
		// pair cannot be sent: the engine that would receive it is the one that
		// rejects it, so the invalid combination is dropped here rather than
		// travelling and failing a job that has already been paid for.
		//
		// Clearing it leaves the worker's default, which is "full" — and "full"
		// is precisely what an edit means, since it keeps the supplied harmony
		// rather than discarding it.
		cot := j.Cot
		if j.ABC != "" && cot == "off" {
			cot = ""
		}
		return &runpod.Yue2Request{
			Style:  j.Caption,
			Lyrics: j.Lyrics,
			Mode:   mode,
			// Empty is left empty on purpose — the worker's own default is
			// "full", and sending our guess would override a choice it is
			// better placed to make.
			Cot:          cot,
			Seed:         j.Seed,
			ABC:          j.ABC,
			SourceAudio:  j.SourceAudio,
			Instrumental: instrumental,
		}
	}
	return &runpod.Request{
		Lyrics:       j.Lyrics,
		Instructions: j.Caption,
		AudioDur:     j.Duration,
		Seed:         j.Seed,
	}
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

		rp := w.clientFor(j)
		if rp == nil {
			// Nothing remote exists yet, so there is nothing to cancel and the
			// row holds no billable job. Failing it here is the whole fix.
			w.fail(ctx, j, "config: no endpoint configured for engine "+j.Engine)
			continue
		}
		id, err := rp.Submit(ctx, requestFor(j))
		if err != nil {
			w.classifySubmit(j, err)
			continue
		}
		if err := w.st.TransitionJob(j.ID, store.StateSubmitting, store.StateSubmitted,
			func(a map[string]any) { a["runpod_id"] = id; a["retries"] = 0 }); err != nil {
			// The remote id exists but the normal CAS failed. Cancel it and
			// durably record both the id and failure — never lose the id and
			// never make the row eligible for resubmission.
			rp.Cancel(ctx, id)
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
		rp := w.clientFor(j)
		if rp == nil {
			// The endpoint went away between submit and now. There is nothing
			// left to poll, and leaving the row active would keep it here
			// forever — polling an endpoint that is not configured.
			w.fail(ctx, j, "config: no endpoint configured for engine "+j.Engine)
			continue
		}
		// Budget before cadence: an expired job must not wait out a poll slot
		// before anyone notices it is over.
		if over, why := expired(j); over {
			rp.Cancel(ctx, j.RunPodID)
			w.fail(ctx, j, why)
			continue
		}
		if time.Since(j.UpdatedAt) < w.pollDelay(j) {
			continue
		}
		sr, err := rp.Status(ctx, j.RunPodID)
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
			w.storeFailure(ctx, j, err)
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

// storeFailure handles a completed job whose artifact could not be stored.
//
// It does not fail the job on the first error. The generation is paid for, its
// artifact survives in the bucket for days, and the failure is usually
// transient — a live run discarded a four-minute song over one TLS handshake
// timeout. So the job stays alive and the next poll re-runs finish(), which is
// idempotent, exactly as the ordering above intends.
//
// The retries are bounded so that a genuinely broken store still ends with a
// message about the store. Counting on `retries` — the same counter the poll
// loop uses for transient failures — is deliberate: both are consecutive
// transient errors, which is what that column means, and nothing else needs to
// be persisted to space the attempts out.
func (w *Worker) storeFailure(ctx context.Context, j *store.Job, err error) {
	n, berr := w.st.BumpRetries(j.ID)
	if berr != nil {
		w.log.Error("worker: store retry count", "job", j.ID, "err", berr)
		w.fail(ctx, j, "store: "+err.Error())
		return
	}
	if n >= maxStoreRetries {
		w.fail(ctx, j, "store: "+err.Error())
		return
	}
	w.log.Warn("worker: storing song failed; the next poll will retry",
		"job", j.ID, "attempt", n, "of", maxStoreRetries, "err", err)
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
	if err := audio.EncodeAudioToM4A(ctx, data, path); err != nil {
		return fmt.Errorf("transcoding audio to m4a: %w", err)
	}
	// YuE2 returns the score it planned, under a presigned URL that expires.
	// It is fetched now, beside the audio, because it is what edit mode
	// generates from later — a stored URL would be a dead link by the time
	// anyone wanted to edit the song.
	//
	// A failed fetch is deliberately not fatal. The song exists and is
	// playable; what is lost is the ability to edit it, which is worth a
	// warning and not worth discarding a completed generation.
	score := ""
	if out.ScoreABCURL != "" {
		b, err := w.fetch(ctx, out.ScoreABCURL)
		if err != nil {
			w.log.Warn("worker: score fetch failed; song stored without its score",
				"job", j.ID, "err", err)
		} else {
			score = string(b)
		}
	}
	// The song inherits the job's owner — otherwise every generated song would
	// land on the legacy owner and be invisible to the user who asked for it.
	// Engine and mode come from the job first: YuE2 reports no engine at all,
	// and the job is the record of what was actually asked for.
	return w.st.CreateSong(&store.Song{
		ID: songID, JobID: j.ID, UserID: j.UserID,
		Lyrics: j.Lyrics, Caption: j.Caption, Idea: j.Idea,
		Duration: out.Duration, Seed: j.Seed, Engine: engineOf(j, out),
		Delivery: out.Delivery, AudioPath: path, Title: titleOf(j),
		Mode: modeOf(j, out), Cot: cotOf(j, out),
		ScoreABC: score, SourceSongID: j.SourceSongID,
		Truncated: out.Truncated,
		CreatedAt: time.Now().UTC(),
	})
}

// engineOf and modeOf record what produced a song, preferring the worker's own
// report and falling back to what the job asked for. MiniMax names its engine
// in the output while YuE2 names neither engine nor a mode that can differ from
// the request — except for cover, which always resolves to melody-conditioned
// generation and says so.
func engineOf(j *store.Job, out *runpod.Output) string {
	if out.Engine != "" {
		return out.Engine
	}
	return j.Engine
}

func modeOf(j *store.Job, out *runpod.Output) string {
	if out.Mode != "" {
		return out.Mode
	}
	return j.Mode
}

// cotOf records the planning depth a song was generated with, preferring the
// worker's report. That preference matters: cover forces melody-conditioning
// and edit forces full, regardless of what the job asked for, so the job's
// value is the request and the worker's is the fact.
func cotOf(j *store.Job, out *runpod.Output) string {
	if out.Cot != "" {
		return out.Cot
	}
	return j.Cot
}

// fetchTimeout bounds one s3 audio download; a hung read must never
// freeze the worker loop (same discipline as runpod.callTimeout).
const fetchTimeout = 2 * time.Minute

// Attempts and spacing for one artifact download.
//
// The artifact is the entire point of the job, its URL stays valid for days,
// and the GPU time that produced it is already spent. A live run lost a
// four-minute song to a single TLS handshake timeout, so a momentary failure
// must not be allowed to cost one.
const (
	fetchAttempts = 4
	fetchBackoff  = 3 * time.Second
)

// fetchTransport serves artifact downloads only, deliberately not sharing
// http.DefaultTransport.
//
// The default pool is used by every other client in the process, and its
// TLSHandshakeTimeout is 10 seconds. A large transfer should not compete for
// connections with status polling, and a handshake that takes longer than ten
// seconds is unusual rather than broken — but failing one costs a song.
var fetchTransport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	MaxIdleConns:          4,
	MaxIdleConnsPerHost:   4,
	IdleConnTimeout:       60 * time.Second,
	TLSHandshakeTimeout:   30 * time.Second,
	ExpectContinueTimeout: time.Second,
}

var fetchClient = &http.Client{Timeout: fetchTimeout, Transport: fetchTransport}

// httpStatusError is a non-2xx answer, typed so a refusal can be told from a
// transport failure without parsing a string.
type httpStatusError struct{ Code int }

func (e *httpStatusError) Error() string { return fmt.Sprintf("HTTP %d", e.Code) }

// permanentFetch reports whether retrying is pointless: the server answered,
// and its answer will not change. 408 and 429 are answers too, but they invite
// a retry; every other 4xx is a decision — a 403 whose signature is wrong stays
// wrong, and a 404 does not become a file.
func permanentFetch(err error) bool {
	var se *httpStatusError
	if !errors.As(err, &se) {
		return false
	}
	if se.Code == http.StatusRequestTimeout || se.Code == http.StatusTooManyRequests {
		return false
	}
	return se.Code >= 400 && se.Code < 500
}

// fetch downloads one artifact, retrying a transient failure with a growing
// pause between attempts.
func (w *Worker) fetch(ctx context.Context, url string) ([]byte, error) {
	backoff := w.fetchBackoff
	if backoff == 0 {
		backoff = fetchBackoff
	}
	var lastErr error
	for attempt := 1; attempt <= fetchAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt-1) * backoff):
			}
		}
		data, err := fetchOnce(ctx, url)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if permanentFetch(err) {
			return nil, err
		}
		if attempt < fetchAttempts {
			w.log.Warn("worker: artifact fetch failed; retrying",
				"attempt", attempt, "of", fetchAttempts, "err", err)
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", fetchAttempts, lastErr)
}

// fetchOnce is a single download attempt.
func fetchOnce(ctx context.Context, url string) ([]byte, error) {
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
		return nil, &httpStatusError{Code: resp.StatusCode}
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
