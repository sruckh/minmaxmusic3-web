//go:build live

// Live probes. These talk to the real RunPod endpoints and cost real GPU time,
// so they are behind the `live` build tag and never compiled into the ordinary
// suite — `go test ./...` cannot run them even by accident.
//
//	go test -tags live -c -o /tmp/probe.test ./internal/worker
//	docker compose run --rm --no-deps app /tmp/probe.test -test.run TestLive -test.v
//
// The second line is the point: it runs inside the app container, so Infisical
// injects the real secrets exactly as it does for the server, and the code path
// exercised is the one the worker actually uses.

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/audio"
	"github.com/sruckh/minmaxmusic3-web/internal/runpod"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// testLogger keeps the probe's output readable — the worker logs a warning for
// every retry, and a probe has nothing to do with those.
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// maskURLs redacts presigned links: they embed a key id, a bucket name and a
// signature, and a probe's output ends up in logs and transcripts.
func maskURLs(s string) string {
	out := s
	for {
		i := strings.Index(out, "http")
		if i < 0 {
			return out
		}
		j := i
		for j < len(out) && !strings.ContainsRune("\" \n", rune(out[j])) {
			j++
		}
		out = out[:i] + "<URL>" + out[j:]
	}
}

func liveEnv(t *testing.T) (endpoint, key string) {
	t.Helper()
	endpoint = os.Getenv("YUE2_RUNPOD_ENDPOINT")
	// PROBE_RUNPOD_API_KEY wins when set, so a throwaway credential can be
	// supplied for one run without touching Infisical — and without relying on
	// -e, which `infisical run` would overwrite with its own value.
	key = os.Getenv("PROBE_RUNPOD_API_KEY")
	if key == "" {
		key = os.Getenv("RUNPOD_API_KEY")
	}
	if endpoint == "" || key == "" {
		t.Skip("live probe needs YUE2_RUNPOD_ENDPOINT and a RunPod key in the environment")
	}
	return endpoint, key
}

// TestLiveYuE2Create is the end-to-end proof this app has never had: it puts
// the request *this code* builds in front of the live worker and follows the
// job to a playable file.
//
// Everything before this point was built from the worker's schema.py and
// README, and its response handling was checked against a captured payload.
// The request side had never been accepted by the live endpoint at all.
func TestLiveYuE2Create(t *testing.T) {
	endpoint, key := liveEnv(t)
	c := &runpod.Client{Endpoint: endpoint, APIKey: key}

	// The job the handler would queue for a YuE2 create. Deliberately small:
	// a short lyric block keeps the generation brief.
	j := &store.Job{
		Engine:  store.EngineYue2,
		Mode:    store.ModeCreate,
		Caption: "solo acoustic guitar, warm, slow, instrumental-forward, 70 BPM",
		Lyrics:  "[Verse]\nMorning light across the window pane\n[Chorus]\nStay a while, the day can wait",
		Seed:    ptr(int64(12300)),
	}

	req := requestFor(j)
	if _, ok := req.(*runpod.Yue2Request); !ok {
		t.Fatalf("a YuE2 job built %T, want *runpod.Yue2Request", req)
	}
	pretty, _ := json.MarshalIndent(req, "", "  ")
	t.Logf("submitting:\n%s", pretty)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	// --- the step that has never succeeded: getting a job accepted ---------
	id, err := c.Submit(ctx, req)
	if err != nil {
		t.Fatalf("submit refused: %v", err)
	}
	t.Logf("accepted, runpod id %s", id)

	// --- poll to a terminal state -----------------------------------------
	var sr *runpod.StatusResponse
	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		sr, err = c.Status(ctx, id)
		if err != nil {
			t.Logf("poll error (continuing): %v", err)
			continue
		}
		if sr.Status != runpod.StatusInQueue && sr.Status != runpod.StatusInProgress {
			break
		}
	}
	if sr == nil {
		t.Fatal("no status ever came back")
	}
	t.Logf("terminal status: %s", sr.Status)
	if sr.Status != runpod.StatusCompleted {
		t.Fatalf("job did not complete: %s — %s", sr.Status, maskURLs(runpod.ErrorText(sr.Error)))
	}

	// --- the app's own decode path ----------------------------------------
	out, err := runpod.OutputOf(sr)
	if err != nil {
		t.Fatalf("OutputOf rejected a real completion: %v", err)
	}
	t.Logf("decoded: delivery=%s duration=%.1f sample_rate=%d seed=%d mode=%q cot=%q",
		out.Delivery, out.Duration, out.SamplingRate, out.Seed, out.Mode, out.Cot)

	if out.Delivery != runpod.DeliveryS3 {
		t.Errorf("Delivery = %q, want %q", out.Delivery, runpod.DeliveryS3)
	}
	if out.AudioURL == "" {
		t.Fatal("no audio_url — nothing to fetch")
	}
	if out.Duration <= 0 {
		t.Errorf("Duration = %v, want positive", out.Duration)
	}
	// The field the two engines spell differently. A zero here is the exact
	// silent failure the fold exists to prevent.
	if out.SamplingRate != 48000 {
		t.Errorf("SamplingRate = %d, want 48000 (folded from sample_rate)", out.SamplingRate)
	}
	// YuE2 reports no engine; the worker stamps it from the job.
	if out.Engine != "" {
		t.Logf("note: worker now reports engine=%q", out.Engine)
	}
	if out.ScoreABCURL == "" {
		t.Error("no score_abc_url — an edit of this song would have nothing to work from")
	}

	// --- the rest of finish(): fetch, transcode, and read the score --------
	w := &Worker{audioDir: t.TempDir(), log: testLogger(t)}
	data, err := w.fetch(ctx, out.AudioURL)
	if err != nil {
		t.Fatalf("fetching the audio: %v", err)
	}
	t.Logf("fetched %d bytes of audio", len(data))

	// A container sniff, not a filename: YuE2 sends FLAC where MiniMax sends
	// WAV, and the encoder has to cope with both.
	if len(data) > 4 {
		t.Logf("container magic: %q", string(data[:4]))
	}

	outPath := filepath.Join(t.TempDir(), "song.m4a")
	if err := audio.EncodeAudioToM4A(ctx, data, outPath); err != nil {
		t.Fatalf("transcoding to m4a: %v", err)
	}
	fi, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat transcoded file: %v", err)
	}
	t.Logf("transcoded to %d bytes of m4a", fi.Size())
	if fi.Size() == 0 {
		t.Error("transcoded file is empty")
	}

	if out.ScoreABCURL != "" {
		score, err := w.fetch(ctx, out.ScoreABCURL)
		if err != nil {
			t.Errorf("fetching the score: %v", err)
		} else {
			head := string(score)
			if len(head) > 120 {
				head = head[:120]
			}
			t.Logf("score fetched, %d bytes, begins: %q", len(score), head)
			if !strings.HasPrefix(strings.TrimSpace(string(score)), "X:") {
				t.Errorf("score does not begin with the ABC header X: — got %q", head)
			}
		}
	}
}

// TestLiveYuE2Edit is the same proof for edit mode: create a song, take the
// score the worker planned, change its tempo, and re-render from it.
//
// Edit is the mode the app can support most cheaply — it needs no object
// storage at all, since a score is a few kilobytes of text in a local column.
// But its request path has never been in front of the live worker, and the
// whole point of this probe is to find that out before a UI is built on it.
func TestLiveYuE2Edit(t *testing.T) {
	endpoint, key := liveEnv(t)
	c := &runpod.Client{Endpoint: endpoint, APIKey: key}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()

	// --- step 1: a create, to obtain a real score -------------------------
	base := &store.Job{
		Engine:  store.EngineYue2,
		Mode:    store.ModeCreate,
		Caption: "sparse piano ballad, close-mic vocal, 80 BPM",
		Lyrics:  "[Verse]\nMorning light across the window pane\n[Chorus]\nStay a while",
		Seed:    ptr(int64(24680)),
	}
	id := submitRetrying(t, ctx, c, requestFor(base), "create")
	t.Logf("create accepted: %s", id)
	out := waitForJob(t, ctx, c, id)
	if out.AudioURL == "" {
		t.Fatal("create produced no audio")
	}
	score, err := (&Worker{log: testLogger(t)}).fetch(ctx, out.ScoreABCURL)
	if err != nil {
		t.Fatalf("fetching the score to edit: %v", err)
	}
	before := scoreMetaOfForProbe(string(score))
	t.Logf("create done: %s | score %d bytes | %s", before, len(score), headerOf(string(score)))

	// --- step 2: edit it --------------------------------------------------
	// The one edit that is genuinely a performance change. Key and meter are
	// deliberately left alone: ABC note tokens are relative, so changing K:
	// respells rather than transposes, and changing M: makes the bars the wrong
	// length — which nothing validates, because the worker defers per-measure
	// arithmetic to a tokenizer it cannot run without a GPU.
	edited := retempo(string(score), 132)
	if edited == string(score) {
		t.Fatalf("the score had no Q: header to rewrite; headers were %s", headerOf(string(score)))
	}

	job := &store.Job{
		Engine:  store.EngineYue2,
		Mode:    store.ModeEdit,
		Caption: "stripped-back piano, slower and closer",
		Lyrics:  base.Lyrics,
		ABC:     edited,
		Seed:    ptr(int64(24680)),
	}
	req, _ := requestFor(job).(*runpod.Yue2Request)
	pretty, _ := json.MarshalIndent(req, "", "  ")
	t.Logf("editing with:\n%s", pretty)

	id2 := submitRetrying(t, ctx, c, req, "edit")
	t.Logf("edit accepted: %s", id2)
	out2 := waitForJob(t, ctx, c, id2)

	if out2.AudioURL == "" {
		t.Fatal("edit produced no audio")
	}
	if out2.Mode != "edit" {
		t.Errorf("worker reports mode %q, want edit", out2.Mode)
	}
	if out2.Duration <= 0 {
		t.Errorf("Duration = %v, want positive", out2.Duration)
	}
	// The worker keeps the supplied harmony by forcing cot=full for an edit.
	// If it came back as anything else, the score was not the one re-rendered.
	if out2.Cot != "full" {
		t.Errorf("Cot = %q, want full — an edit keeps the supplied harmony", out2.Cot)
	}

	data, err := (&Worker{log: testLogger(t)}).fetch(ctx, out2.AudioURL)
	if err != nil {
		t.Fatalf("fetching the edited audio: %v", err)
	}
	t.Logf("edited audio: %d bytes, container %q, %.1fs", len(data), string(data[:4]), out2.Duration)

	// --- step 3: did the tempo actually change? ---------------------------
	if out2.ScoreABCURL != "" {
		b, err := (&Worker{log: testLogger(t)}).fetch(ctx, out2.ScoreABCURL)
		if err != nil {
			t.Errorf("fetching the edited score: %v", err)
		} else {
			after := scoreMetaOfForProbe(string(b))
			t.Logf("edit done:  %s | %s", after, headerOf(string(b)))
			t.Logf("tempo asked for 132; the edited score reports %s", after)
			// Not asserted: the model re-plans, so this is information rather
			// than a contract. Worth seeing whether Q: is honoured at all.
		}
	}
}

// submitRetrying submits, waiting out a refusal the worker would also wait out.
//
// RunPod answers 409 ENDPOINT_PAUSED whenever the endpoint has scaled to zero,
// which it does between runs. The worker treats that as a definitive refusal and
// requeues; a probe that called Submit once would simply fail, which says
// nothing about the request it was trying to test. So this retries exactly what
// IsRetryableSubmit allows — and nothing else, because a transport error might
// have landed and submitting again could bill twice.
func submitRetrying(t *testing.T, ctx context.Context, c *runpod.Client, req any, what string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Minute)
	for attempt := 1; ; attempt++ {
		id, err := c.Submit(ctx, req)
		if err == nil {
			return id
		}
		if !runpod.IsRetryableSubmit(err) {
			t.Fatalf("%s submit refused outright: %v", what, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s still refused after 20 minutes: %v", what, err)
		}
		t.Logf("%s refused (attempt %d), waiting for capacity: %v", what, attempt, err)
		select {
		case <-ctx.Done():
			t.Fatalf("context expired waiting for capacity on %s", what)
		case <-time.After(30 * time.Second):
		}
	}
}

// waitForJob polls to a terminal state and decodes the output.
func waitForJob(t *testing.T, ctx context.Context, c *runpod.Client, id string) *runpod.Output {
	t.Helper()
	deadline := time.Now().Add(35 * time.Minute)
	var sr *runpod.StatusResponse
	var err error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			t.Fatalf("context expired waiting for %s", id)
		case <-time.After(5 * time.Second):
		}
		sr, err = c.Status(ctx, id)
		if err != nil {
			continue
		}
		if sr.Status != runpod.StatusInQueue && sr.Status != runpod.StatusInProgress {
			break
		}
	}
	if sr == nil {
		t.Fatalf("no status ever came back for %s", id)
	}
	if sr.Status != runpod.StatusCompleted {
		t.Fatalf("%s did not complete: %s — %s", id, sr.Status, maskURLs(runpod.ErrorText(sr.Error)))
	}
	out, err := runpod.OutputOf(sr)
	if err != nil {
		t.Fatalf("OutputOf rejected a real completion: %v", err)
	}
	return out
}

// headerOf shows just the header block, so a probe's log stays readable.
func headerOf(abc string) string {
	var b strings.Builder
	for _, line := range strings.Split(abc, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "V:") || strings.HasPrefix(line, "%") {
			break
		}
		if line != "" {
			b.WriteString(line)
			b.WriteString(" ")
		}
	}
	return strings.TrimSpace(b.String())
}

// scoreMetaOfForProbe mirrors server.scoreMetaOf, which is in another package.
func scoreMetaOfForProbe(abc string) string {
	var key, meter, tempo string
	for _, line := range strings.Split(abc, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "K:") && key == "":
			key = strings.TrimSpace(line[2:])
		case strings.HasPrefix(line, "M:") && meter == "":
			meter = strings.TrimSpace(line[2:])
		case strings.HasPrefix(line, "Q:") && tempo == "":
			if i := strings.IndexByte(line, '='); i >= 0 {
				tempo = strings.TrimSpace(line[i+1:])
			}
		}
	}
	return "K=" + key + " M=" + meter + " Q=" + tempo
}

// retempo rewrites the Q: header, leaving everything else byte-identical.
//
// Tempo is the one edit that is unambiguously a performance change: nothing in
// the score depends on it. Key and meter do not have that property, which is
// why this probe does not touch them.
func retempo(abc string, bpm int) string {
	lines := strings.Split(abc, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "Q:") {
			lines[i] = fmt.Sprintf("Q:1/4=%d", bpm)
			return strings.Join(lines, "\n")
		}
	}
	return abc
}

func ptr[T any](v T) *T { return &v }
