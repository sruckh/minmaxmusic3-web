package worker

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/runpod"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

func ago(d time.Duration) time.Time { return time.Now().UTC().Add(-d) }

func at(d time.Duration) *time.Time { t := ago(d); return &t }

// The point of the split: queue time is not generation time. A job that spent
// half an hour waiting for a GPU must still get its full run budget once one
// picks it up, and a job that never gets picked up must eventually stop.
func TestExpiredSwitchesClockWhenAGPUPicksTheJobUp(t *testing.T) {
	for _, tc := range []struct {
		name string
		job  *store.Job
		want bool
	}{
		{"queued briefly", &store.Job{CreatedAt: ago(2 * time.Minute)}, false},
		{"queued a long time, still under budget", &store.Job{CreatedAt: ago(queueBudget - time.Minute)}, false},
		{"queued past budget", &store.Job{CreatedAt: ago(queueBudget + time.Minute)}, true},

		// Created long ago but only just started: the old single clock would
		// have killed this one instantly, having already paid for the wait.
		{"long wait then a fresh start", &store.Job{
			CreatedAt: ago(queueBudget - time.Minute), StartedAt: at(time.Minute)}, false},
		{"running past run budget", &store.Job{
			Engine:    store.EngineMiniMax,
			CreatedAt: ago(2 * time.Hour), StartedAt: at(runBudgetMiniMax + time.Second)}, true},
		{"running under run budget", &store.Job{
			Engine:    store.EngineMiniMax,
			CreatedAt: ago(2 * time.Hour), StartedAt: at(runBudgetMiniMax - time.Second)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, why := expired(tc.job)
			if got != tc.want {
				t.Fatalf("expired = %v (%q), want %v", got, why, tc.want)
			}
			if got && why == "" {
				t.Error("an expired job must carry a reason the user can read")
			}
		})
	}
}

// The run budget turns on the mode, not on the engine alone, because the modes
// do wildly different amounts of work before they generate anything.
func TestRunBudgetDependsOnModeNotJustEngine(t *testing.T) {
	for _, tc := range []struct {
		name string
		job  *store.Job
		want time.Duration
	}{
		{"minimax create", &store.Job{Engine: store.EngineMiniMax, Mode: store.ModeCreate}, runBudgetMiniMax},
		// An unset engine is every job written before the column existed, and
		// those were all MiniMax creates. They must keep the budget they had.
		{"unset engine", &store.Job{}, runBudgetMiniMax},
		{"yue2 create", &store.Job{Engine: store.EngineYue2, Mode: store.ModeCreate}, runBudgetYue2},
		{"yue2 edit", &store.Job{Engine: store.EngineYue2, Mode: store.ModeEdit}, runBudgetYue2},
		{"yue2 cover", &store.Job{Engine: store.EngineYue2, Mode: store.ModeCover}, runBudgetYue2Cover},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runBudgetFor(tc.job); got != tc.want {
				t.Errorf("runBudgetFor = %s, want %s", got, tc.want)
			}
		})
	}
}

// A cover transcribes with two models in sequence before it generates, so it
// spends minutes looking idle by MiniMax's standards. On the MiniMax budget it
// would be killed mid-transcription and reported as a timeout for work that was
// progressing normally — which is the bug this budget exists to prevent.
func TestCoverSurvivesPastTheMiniMaxBudget(t *testing.T) {
	elapsed := runBudgetMiniMax + time.Minute

	cover := &store.Job{
		Engine: store.EngineYue2, Mode: store.ModeCover,
		CreatedAt: ago(2 * time.Hour), StartedAt: at(elapsed),
	}
	if over, why := expired(cover); over {
		t.Fatalf("a cover %s into generation is still working, but was expired: %s", elapsed, why)
	}

	minimax := &store.Job{
		Engine:    store.EngineMiniMax,
		CreatedAt: ago(2 * time.Hour), StartedAt: at(elapsed),
	}
	if over, _ := expired(minimax); !over {
		t.Error("the same elapsed time on MiniMax must still expire — the budgets are not interchangeable")
	}
}

// requestFor chooses the payload shape, and the two engines' vocabularies must
// not cross. Marshalling the built request is the only way to see what actually
// goes on the wire.
func TestRequestForNeverCrossesTheEngineVocabularies(t *testing.T) {
	yue2, ok := requestFor(&store.Job{
		Engine: store.EngineYue2, Mode: store.ModeCreate,
		Caption: "city pop", Lyrics: "[Verse]\nhi", Duration: 30,
	}).(*runpod.Yue2Request)
	if !ok {
		t.Fatal("a YuE2 job must build a Yue2Request")
	}
	b, _ := json.Marshal(yue2)
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	for _, banned := range []string{"input", "instructions", "audio_duration"} {
		if _, present := got[banned]; present {
			t.Errorf("%q is MiniMax's vocabulary and must not reach YuE2: %s", banned, b)
		}
	}
	// Duration is the one that would have been sent silently: MiniMax needs it
	// and YuE2 has no such parameter, so a shared builder would leak it.
	if v, _ := got["style"].(string); v != "city pop" {
		t.Errorf("style = %q, want the caption", v)
	}

	if _, ok := requestFor(&store.Job{Engine: store.EngineMiniMax}).(*runpod.Request); !ok {
		t.Error("a MiniMax job must build a Request")
	}
}

// An external score needs a plan to hang off, so the worker refuses `abc`
// together with `cot="off"` outright. The pair must never reach it: the engine
// that would receive it is the one that rejects it, and by then the job has
// been queued and paid for.
func TestAbcAndCotOffAreNeverSentTogether(t *testing.T) {
	j := &store.Job{
		Engine: store.EngineYue2, Mode: store.ModeEdit,
		Caption: "stripped-back piano", Lyrics: "words",
		ABC: "X:1\nK:Bb\n", Cot: "off",
	}
	req, _ := requestFor(j).(*runpod.Yue2Request)
	if req.Cot == "off" {
		t.Error("cot=off was sent alongside abc, which the worker rejects")
	}
	if req.ABC == "" {
		t.Error("the score was dropped instead of the invalid cot")
	}
	// Empty leaves the worker's default, and "full" is precisely what an edit
	// means — it keeps the supplied harmony rather than discarding it.
	if req.Cot != "" {
		t.Errorf("Cot = %q, want empty so the worker's own \"full\" applies", req.Cot)
	}

	// The guard is about the pair, not about "off" itself: a job asking for no
	// score is free to skip planning.
	j.ABC = ""
	if req, _ := requestFor(j).(*runpod.Yue2Request); req.Cot != "off" {
		t.Errorf("cot=off was dropped with no score to justify it: %q", req.Cot)
	}
}

// `mode` defaults to create, so sending it would only restate the default —
// but the other two must travel, or the worker runs the wrong mode entirely.
func TestRequestForSendsEveryModeExceptTheDefault(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{store.ModeCreate, ""},
		{store.ModeCover, "cover"},
		{store.ModeEdit, "edit"},
	} {
		j := &store.Job{Engine: store.EngineYue2, Mode: tc.mode, Caption: "pop", Lyrics: "words"}
		req, _ := requestFor(j).(*runpod.Yue2Request)
		if req.Mode != tc.want {
			t.Errorf("mode %q produced %q, want %q", tc.mode, req.Mode, tc.want)
		}
	}
}

// The instrumental flag is stored, not inferred from an empty lyric box.
//
// An empty box is equally consistent with "this song has no words" and "the
// words are not typed yet", and those want opposite requests — the worker reads
// a present-but-empty lyrics field as a supplied empty lyric and refuses it. So
// inferring would turn every half-written form into a rejected job.
func TestInstrumentalComesFromTheJobNotFromEmptyLyrics(t *testing.T) {
	j := &store.Job{Engine: store.EngineYue2, Mode: store.ModeCreate, Caption: "pop"}
	if req, _ := requestFor(j).(*runpod.Yue2Request); req.Instrumental {
		t.Error("empty lyrics alone must not set the instrumental flag")
	}

	j.Instrumental = true
	if req, _ := requestFor(j).(*runpod.Yue2Request); !req.Instrumental {
		t.Error("the stored flag was not sent")
	}

	// Cover is excluded whatever the flag says: a cover's words come from the
	// recording, so calling it instrumental contradicts the mode about to run —
	// and the worker refuses the contradiction outright.
	j.Mode = store.ModeCover
	if req, _ := requestFor(j).(*runpod.Yue2Request); req.Instrumental {
		t.Error("a cover must never be sent as instrumental")
	}
}

// A job that has not been refused yet must not be delayed at all — the whole
// suite once hung because a first attempt waited out a retry backoff.
func TestSubmitDelayBacksOffOnlyAfterARefusal(t *testing.T) {
	if d := submitDelay(0); d != 0 {
		t.Fatalf("first attempt delayed by %s; it is not a retry", d)
	}
	want := []time.Duration{
		submitBackoff,     // 1st retry
		2 * submitBackoff, // 2nd
		4 * submitBackoff, // 3rd
	}
	for i, w := range want {
		if got := submitDelay(i + 1); got != w {
			t.Errorf("submitDelay(%d) = %s, want %s", i+1, got, w)
		}
	}
	for _, n := range []int{8, 20, 500} {
		if got := submitDelay(n); got != maxSubmitBackoff {
			t.Errorf("submitDelay(%d) = %s, want the %s cap", n, got, maxSubmitBackoff)
		}
	}
}

// Retrying must stay frequent enough to be worth doing: a job has to get many
// chances at capacity inside its queue budget, or the backoff has quietly
// become the timeout.
func TestBackoffLeavesRoomForManyAttemptsInsideTheQueueBudget(t *testing.T) {
	var elapsed time.Duration
	attempts := 0
	for elapsed < queueBudget {
		attempts++
		elapsed += submitDelay(attempts)
	}
	if attempts < 20 {
		t.Errorf("only %d submit attempts fit in %s; capacity would rarely be caught",
			attempts, queueBudget)
	}
}

// TestTitleOfPrefersTheUserTitle: naming a song on the generate form is the
// whole point of the field, so a title on the job wins over the caption. The
// derived fallback still covers jobs submitted without one — including every
// job that predates the field.
func TestTitleOfPrefersTheUserTitle(t *testing.T) {
	for _, tc := range []struct {
		name  string
		job   store.Job
		title string
	}{
		{"user title wins", store.Job{Title: "Midnight Drive", Caption: "acoustic pop"}, "Midnight Drive"},
		{"blank falls back to the caption", store.Job{Caption: "acoustic pop\nsoft vocals"}, "acoustic pop"},
		{"whitespace is not a title", store.Job{Title: "   ", Caption: "acoustic pop"}, "acoustic pop"},
		{"title is trimmed", store.Job{Title: "  Midnight Drive  "}, "Midnight Drive"},
		{"nothing at all", store.Job{}, "Untitled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := titleOf(&tc.job); got != tc.title {
				t.Errorf("titleOf = %q, want %q", got, tc.title)
			}
		})
	}
}
