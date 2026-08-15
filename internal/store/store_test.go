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

func TestDeleteSong(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	j := testJob("job-del")
	if err := s.CreateJob(j); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	song := &Song{
		ID: "song-del", JobID: j.ID, Lyrics: "lyrics", Caption: "caption",
		Duration: 45, Engine: "diffusers", Delivery: "base64",
		AudioPath: "/tmp/del.m4a", Title: "Delete Me", CreatedAt: now,
	}
	if err := s.CreateSong(song); err != nil {
		t.Fatal(err)
	}

	// Verify song exists
	got, err := s.Song(song.ID)
	if err != nil || got == nil {
		t.Fatalf("expected song to exist, got %v, err=%v", got, err)
	}

	// Delete song
	deleted, err := s.DeleteSong(song.ID)
	if err != nil {
		t.Fatalf("DeleteSong failed: %v", err)
	}
	if deleted == nil || deleted.ID != song.ID {
		t.Fatalf("expected deleted song %s, got %#v", song.ID, deleted)
	}

	// Verify song is removed
	gotSong, err := s.Song(song.ID)
	if err != nil {
		t.Fatalf("Song lookup after delete error: %v", err)
	}
	if gotSong != nil {
		t.Fatalf("expected song to be deleted, found: %#v", gotSong)
	}

	// Verify job is removed
	gotJob, err := s.Job(j.ID)
	if err != nil {
		t.Fatalf("Job lookup after delete error: %v", err)
	}
	if gotJob != nil {
		t.Fatalf("expected job to be deleted, found: %#v", gotJob)
	}

	// Deleting a non-existent song returns nil, nil
	missing, err := s.DeleteSong("non-existent")
	if err != nil {
		t.Fatalf("DeleteSong non-existent error: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected nil for non-existent song, got %#v", missing)
	}
}

func TestUpdateSongTitle(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now().UTC()
	song := &Song{
		ID: "song-upd", JobID: "j-upd", Lyrics: "la", Caption: "pop",
		Duration: 30, Engine: "diffusers", Delivery: "base64",
		AudioPath: "/tmp/u.m4a", Title: "Original Title", CreatedAt: now,
	}
	if err := s.CreateSong(song); err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateSongTitle(song.ID, "New Custom Title"); err != nil {
		t.Fatalf("UpdateSongTitle failed: %v", err)
	}

	got, err := s.Song(song.ID)
	if err != nil || got == nil {
		t.Fatalf("Song lookup error: %v", err)
	}
	if got.Title != "New Custom Title" {
		t.Fatalf("expected title 'New Custom Title', got %q", got.Title)
	}

	// Update non-existent song
	if err := s.UpdateSongTitle("non-existent", "Title"); err == nil {
		t.Fatalf("expected error when updating non-existent song, got nil")
	}
}


