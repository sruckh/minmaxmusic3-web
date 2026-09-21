//go:build live

package worker

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/runpod"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// TestLiveCoverFetch is the throughput question the reachability probe left
// open.
//
// That probe proved RunPod can reach MM3_PUBLIC_URL, but it fetched /healthz —
// two bytes. A real recording is megabytes, and the path being reachable says
// nothing about whether a sustained transfer completes inside the worker's
// budget. This fetches an actual file from the same origin.
//
// It needs a live app serving a real song, so it reads the app's own database
// and mints a link the way the cover handler would. Run it inside the app
// container, where the data volume and the secrets both are.
func TestLiveCoverFetch(t *testing.T) {
	endpoint, key := liveEnv(t)
	dbPath := os.Getenv("MM3_DB_PATH")
	if dbPath == "" {
		dbPath = "/data/mm3.db"
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Skipf("no database at %s — run this inside the app container", dbPath)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening %s: %v", dbPath, err)
	}
	defer st.Close()

	// Any song with audio on disk will do: the worker only needs bytes it can
	// decode, and the point is the size, not the content.
	admin := store.Access{Admin: true}
	songs, err := st.Songs(50, 0, admin)
	if err != nil {
		t.Fatalf("listing songs: %v", err)
	}
	var song *store.Song
	for _, s := range songs {
		if s.AudioPath == "" {
			continue
		}
		if fi, err := os.Stat(s.AudioPath); err == nil && fi.Size() > 1<<20 {
			song = s
			break
		}
	}
	if song == nil {
		t.Skip("no song with a multi-megabyte audio file to serve")
	}
	fi, _ := os.Stat(song.AudioPath)
	t.Logf("serving %s (%d bytes)", song.ID, fi.Size())

	// Mint the link exactly as the cover handler does.
	token, err := st.CreateCoverLink(store.CoverLinkAudio, song.ID, 2*time.Hour)
	if err != nil {
		t.Fatalf("minting a cover link: %v", err)
	}
	public := strings.TrimRight(os.Getenv("MM3_PUBLIC_URL"), "/")
	if public == "" {
		t.Skip("MM3_PUBLIC_URL is unset, so no absolute link can be built")
	}
	sourceURL := public + "/signed/" + token
	t.Logf("source: %s/signed/<token>", public)

	// --- does the app serve it, at size? ---------------------------------
	w := &Worker{log: testLogger(t), fetchBackoff: time.Second}

	// Two contexts, deliberately. The fetch is bounded tightly because it
	// should be quick and a hang there is its own bug; the job needs the
	// worker's full budget, because a cover transcribes with two model families
	// before it generates anything.
	//
	// An earlier version used one five-minute context for both, so the job was
	// abandoned mid-transcription with the probe blaming a timeout that was its
	// own. The job it left behind went on to run — the work was fine, the
	// measurement was not.
	fctx, fcancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer fcancel()

	// Fetched the way the worker fetches it: from inside the container, over
	// the public origin, with no session.
	body, err := w.fetch(fctx, sourceURL)
	if err != nil {
		t.Fatalf("the app did not serve its own signed link: %v", err)
	}
	if int64(len(body)) != fi.Size() {
		t.Errorf("served %d bytes, file is %d", len(body), fi.Size())
	}
	t.Logf("app served %d bytes over the public origin", len(body))

	// --- and can RunPod fetch the same thing? ----------------------------
	// A cover job pointed at the link. Success here is the worker getting far
	// enough to decode the audio: a fetch failure and a decode failure are
	// different errors, and only the first means it could not reach us.
	c := &runpod.Client{Endpoint: endpoint, APIKey: key}
	req := requestFor(&store.Job{
		Engine: store.EngineYue2, Mode: store.ModeCover,
		Caption: "sparse piano, slow", Lyrics: "",
		SourceAudio: sourceURL,
	})
	pretty, _ := json.MarshalIndent(req, "", "  ")
	t.Logf("cover request:\n%s", pretty)

	// The job's own budget, separate from the fetch's.
	jctx, jcancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer jcancel()

	id := submitRetrying(t, jctx, c, req, "cover")
	t.Logf("cover accepted: %s", id)

	deadline := time.Now().Add(30 * time.Minute)
	var sr *runpod.StatusResponse
	for time.Now().Before(deadline) {
		select {
		case <-jctx.Done():
			t.Fatalf("the job's own budget expired while it was %s", statusOf(sr))
		case <-time.After(10 * time.Second):
		}
		sr, err = c.Status(jctx, id)
		if err != nil {
			continue
		}
		if sr.Status != runpod.StatusInQueue && sr.Status != runpod.StatusInProgress {
			break
		}
	}
	if sr == nil {
		t.Fatal("no status ever came back")
	}
	text := runpod.ErrorText(sr.Error)
	t.Logf("terminal status: %s", sr.Status)
	if sr.Status != runpod.StatusCompleted {
		t.Logf("error: %s", maskURLs(text))
	}

	// The verdict, and it is about which failure this is rather than pass/fail:
	switch {
	case sr.Status == runpod.StatusCompleted:
		out, err := runpod.OutputOf(sr)
		if err != nil {
			t.Fatalf("OutputOf rejected a real cover: %v", err)
		}
		t.Logf("COVER SUCCEEDED: %.1fs of audio, mode=%q cot=%q",
			out.Duration, out.Mode, out.Cot)
		if out.Mode != "cover" {
			t.Errorf("Mode = %q, want cover", out.Mode)
		}
		// A cover is melody-conditioned by definition; the worker forces this
		// itself rather than trusting the caller.
		if out.Cot != "melody" {
			t.Errorf("Cot = %q, want melody — a cover is melody-conditioned", out.Cot)
		}
	case strings.Contains(text, "could not fetch source_audio"):
		t.Fatalf("the worker could NOT fetch from the public origin at size: %s", maskURLs(text))
	default:
		// It fetched and got past the download — which is the answer this test
		// was asking for. A decode or transcription failure on a MiniMax WAV is
		// expected and not a defect in the link.
		t.Logf("the worker FETCHED the recording (this is not a fetch failure): %s", maskURLs(text))
	}
}

// statusOf names the state a probe was in when it gave up, so an abandoned
// run says whether the work was still moving or had wedged.
func statusOf(sr *runpod.StatusResponse) string {
	if sr == nil {
		return "never observed"
	}
	return sr.Status
}
