package store

import (
	"path/filepath"
	"testing"
	"time"
)

func testJob(id string) *Job {
	now := time.Now().UTC()
	return &Job{ID: id, State: StateQueued, Lyrics: "la", Caption: "pop",
		Duration: 30, CreatedAt: now, UpdatedAt: now}
}

func TestAmbiguousSubmittingJobNeverRequeues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	j := testJob("j1")
	if err := s.CreateJob(j); err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionJob(j.ID, StateQueued, StateSubmitting, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate process restart in the remote/local dual-write window.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ambiguous, err := s.SubmittingJobs()
	if err != nil || len(ambiguous) != 1 {
		t.Fatalf("submitting jobs = %d, err=%v", len(ambiguous), err)
	}
	if err := s.OrphanSubmission(j.ID, "", "submit-ambiguous"); err != nil {
		t.Fatal(err)
	}
	queued, err := s.DequeueQueued(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Fatalf("ambiguous job became eligible for resubmit: %d queued", len(queued))
	}
	got, err := s.Job(j.ID)
	if err != nil || got.State != StateFailed || got.Error != "submit-ambiguous" {
		t.Fatalf("recovered job = %#v, err=%v", got, err)
	}
}

func TestOrphanSubmissionDurablyRecordsRemoteID(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	j := testJob("j2")
	if err := s.CreateJob(j); err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionJob(j.ID, StateQueued, StateSubmitting, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.OrphanSubmission(j.ID, "rp-123", "submit-cas"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Job(j.ID)
	if err != nil || got.RunPodID != "rp-123" || got.State != StateFailed {
		t.Fatalf("orphan = %#v, err=%v", got, err)
	}
}

func TestOneSongPerJob(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	for _, id := range []string{"song-a", "song-b"} {
		if err := s.CreateSong(&Song{ID: id, JobID: "job-one", Lyrics: "la",
			Caption: "pop", Duration: 30, Engine: "stub", Delivery: "base64",
			AudioPath: "/tmp/x.wav", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	songs, err := s.Songs(10, 0)
	if err != nil || len(songs) != 1 {
		t.Fatalf("songs = %d, err=%v", len(songs), err)
	}
}
