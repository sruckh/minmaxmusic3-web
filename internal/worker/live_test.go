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

func ptr[T any](v T) *T { return &v }
