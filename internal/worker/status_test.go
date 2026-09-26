package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/audio"
	"github.com/sruckh/minmaxmusic3-web/internal/runpod"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// submittedJob is a job RunPod has accepted: the state applyStatus sees.
func submittedJob(t *testing.T, w *Worker) *store.Job {
	t.Helper()
	j := claimQueuedJob(t, w)
	if err := w.st.TransitionJob(j.ID, store.StateSubmitting, store.StateSubmitted,
		func(a map[string]any) { a["runpod_id"] = "rp-" + j.ID }); err != nil {
		t.Fatalf("submitting the job: %v", err)
	}
	return reload(t, w, j.ID)
}

func reload(t *testing.T, w *Worker, id string) *store.Job {
	t.Helper()
	j, err := w.st.Job(id, store.AdminAccess("test"))
	if err != nil || j == nil {
		t.Fatalf("Job(%s) = %v, %v", id, j, err)
	}
	return j
}

func completedStatus(t *testing.T) *runpod.StatusResponse {
	t.Helper()
	out, err := json.Marshal(map[string]any{
		"delivery":     "base64",
		"audio_base64": base64.StdEncoding.EncodeToString(audio.GenerateTestWAV(32000, 2, 3200)),
		"duration":     30,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &runpod.StatusResponse{Status: runpod.StatusCompleted, Output: out}
}

// Every RunPod status maps to exactly one local state. This is the closed
// machine applyStatus exists to enforce; a status that lands anywhere else
// leaves a job stuck or a paid generation discarded.
func TestApplyStatusMapsEachStatusToOneState(t *testing.T) {
	for _, c := range []struct {
		name string
		sr   func(*testing.T) *runpod.StatusResponse
		want string
	}{
		{"in queue", func(*testing.T) *runpod.StatusResponse { return &runpod.StatusResponse{Status: runpod.StatusInQueue} }, store.StateSubmitted},
		{"in progress", func(*testing.T) *runpod.StatusResponse {
			return &runpod.StatusResponse{Status: runpod.StatusInProgress}
		}, store.StateRunning},
		{"completed", completedStatus, store.StateSucceeded},
		{"failed", func(*testing.T) *runpod.StatusResponse { return &runpod.StatusResponse{Status: runpod.StatusFailed} }, store.StateFailed},
		{"cancelled", func(*testing.T) *runpod.StatusResponse { return &runpod.StatusResponse{Status: runpod.StatusCancelled} }, store.StateFailed},
		{"unknown", func(*testing.T) *runpod.StatusResponse { return &runpod.StatusResponse{Status: "TIMED_OUT"} }, store.StateFailed},
		{"completed, no output", func(*testing.T) *runpod.StatusResponse { return &runpod.StatusResponse{Status: runpod.StatusCompleted} }, store.StateFailed},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newClassifyWorker(t)
			w.audioDir = t.TempDir()
			j := submittedJob(t, w)
			w.applyStatus(context.Background(), j, c.sr(t))
			if got := reload(t, w, j.ID).State; got != c.want {
				t.Errorf("state = %s, want %s", got, c.want)
			}
		})
	}
}

// started_at is written by the first IN_PROGRESS and never again: the run
// budget measures from when a GPU picked the job up, not from the latest poll.
func TestApplyStatusStampsStartOnce(t *testing.T) {
	w := newClassifyWorker(t)
	j := submittedJob(t, w)
	running := &runpod.StatusResponse{Status: runpod.StatusInProgress}

	w.applyStatus(context.Background(), j, running)
	first := reload(t, w, j.ID)
	if first.StartedAt == nil {
		t.Fatal("the first IN_PROGRESS did not stamp started_at")
	}
	time.Sleep(10 * time.Millisecond)
	w.applyStatus(context.Background(), first, running)
	if again := reload(t, w, j.ID); !again.StartedAt.Equal(*first.StartedAt) {
		t.Errorf("started_at moved from %v to %v", *first.StartedAt, *again.StartedAt)
	}
}

// A COMPLETED seen twice — the CAS lost a race, or the process restarted
// between storing and marking done — stores the song once. finish's idempotence
// is what lets applyStatus store before it marks the job succeeded.
func TestCompletedTwiceStoresOneSong(t *testing.T) {
	w := newClassifyWorker(t)
	w.audioDir = t.TempDir()
	j := submittedJob(t, w)

	w.applyStatus(context.Background(), j, completedStatus(t))
	first, err := w.st.SongForJob(j.ID)
	if err != nil || first == nil {
		t.Fatalf("no song after COMPLETED: %v, %v", first, err)
	}
	if err := w.finish(context.Background(), j, &runpod.Output{Delivery: "base64"}); err != nil {
		t.Fatalf("a second finish errored instead of standing down: %v", err)
	}
	if again, _ := w.st.SongForJob(j.ID); again.ID != first.ID {
		t.Errorf("song replaced: %s then %s", first.ID, again.ID)
	}
	if first.UserID != "u1" {
		t.Errorf("song owner = %q, want the job's", first.UserID)
	}
}
