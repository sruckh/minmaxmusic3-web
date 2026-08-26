package worker

import (
	"testing"
	"time"

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
			CreatedAt: ago(2 * time.Hour), StartedAt: at(runBudget + time.Second)}, true},
		{"running under run budget", &store.Job{
			CreatedAt: ago(2 * time.Hour), StartedAt: at(runBudget - time.Second)}, false},
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
