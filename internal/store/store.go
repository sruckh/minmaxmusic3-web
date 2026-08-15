// Package store persists jobs and songs in SQLite (pure-Go driver, no CGO).
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Job states — the closed machine from stage 02's contract.
const (
	StateQueued     = "queued"
	StateSubmitting = "submitting" // local claim made; remote id not durable yet
	StateSubmitted  = "submitted"
	StateRunning    = "running"
	StateSucceeded  = "succeeded"
	StateFailed     = "failed"
	StateCancelled  = "cancelled"
)

var ErrTransition = errors.New("store: state transition rejected")

type Job struct {
	ID        string
	State     string
	RunPodID  string
	Lyrics    string
	Caption   string
	Duration  float64
	Seed      *int64
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Song struct {
	ID        string
	JobID     string
	Lyrics    string
	Caption   string
	Duration  float64
	Seed      *int64
	Engine    string
	Delivery  string
	AudioPath string
	Title     string
	CreatedAt time.Time
}

// Open creates the database and schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // sqlite: single writer
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

type Store struct{ db *sql.DB }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS jobs (
  id         TEXT PRIMARY KEY,
  state      TEXT NOT NULL,
  runpod_id  TEXT NOT NULL DEFAULT '',
  lyrics     TEXT NOT NULL,
  caption    TEXT NOT NULL,
  duration_s REAL NOT NULL,
  seed       INTEGER,
  error      TEXT NOT NULL DEFAULT '',
  retries    INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS songs (
  id         TEXT PRIMARY KEY,
  job_id     TEXT NOT NULL,
  lyrics     TEXT NOT NULL,
  caption    TEXT NOT NULL,
  duration_s REAL NOT NULL,
  seed       INTEGER,
  engine     TEXT NOT NULL,
  delivery   TEXT NOT NULL,
  audio_path TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state);
CREATE INDEX IF NOT EXISTS idx_songs_created ON songs(created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS uq_songs_job ON songs(job_id);
`)
	return err
}

// CreateJob inserts a queued job.
func (s *Store) CreateJob(j *Job) error {
	_, err := s.db.Exec(
		`INSERT INTO jobs (id, state, lyrics, caption, duration_s, seed, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, StateQueued, j.Lyrics, j.Caption, j.Duration, j.Seed, j.CreatedAt, j.CreatedAt)
	return err
}

// TransitionJob moves a job from one state to another (compare-and-set).
// Terminal states never transition again.
func (s *Store) TransitionJob(id, from, to string, set func(assignments map[string]any)) error {
	sets := map[string]any{"state": to, "updated_at": time.Now().UTC()}
	if set != nil {
		set(sets)
	}
	cols, args := make([]string, 0, len(sets)), make([]any, 0, len(sets)+2)
	for k, v := range sets {
		cols = append(cols, k+" = ?")
		args = append(args, v)
	}
	args = append(args, id, from)
	res, err := s.db.Exec(
		"UPDATE jobs SET "+join(cols)+" WHERE id = ? AND state = ?", args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: job %s %s -> %s", ErrTransition, id, from, to)
	}
	return nil
}

// FailJob marks a job failed from any non-terminal state.
func (s *Store) FailJob(id, reason string) error {
	res, err := s.db.Exec(
		`UPDATE jobs SET state = ?, error = ?, updated_at = ?
		 WHERE id = ? AND state NOT IN ('succeeded','failed','cancelled')`,
		StateFailed, reason, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: job %s already terminal", ErrTransition, id)
	}
	return nil
}

func (s *Store) Job(id string) (*Job, error) {
	row := s.db.QueryRow(`SELECT id, state, runpod_id, lyrics, caption,
		duration_s, seed, error, created_at, updated_at FROM jobs WHERE id = ?`, id)
	var j Job
	err := row.Scan(&j.ID, &j.State, &j.RunPodID, &j.Lyrics, &j.Caption,
		&j.Duration, &j.Seed, &j.Error, &j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// DequeueQueued returns queued jobs (worker submits them).
func (s *Store) DequeueQueued(limit int) ([]*Job, error) {
	return s.jobsByState(StateQueued, limit)
}

// SubmittingJobs returns jobs claimed locally before the remote id became
// durable. On restart these are failed as ambiguous — never resubmitted.
func (s *Store) SubmittingJobs() ([]*Job, error) {
	return s.jobsByState(StateSubmitting, 100)
}

// ActiveJobs returns submitted/running jobs (worker polls them).
func (s *Store) ActiveJobs() ([]*Job, error) {
	js, err := s.jobsByState(StateSubmitted, 100)
	if err != nil {
		return nil, err
	}
	rs, err := s.jobsByState(StateRunning, 100)
	return append(js, rs...), err
}

func (s *Store) jobsByState(state string, limit int) ([]*Job, error) {
	rows, err := s.db.Query(`SELECT id, state, runpod_id, lyrics, caption,
		duration_s, seed, error, created_at, updated_at
		FROM jobs WHERE state = ? ORDER BY created_at LIMIT ?`, state, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.State, &j.RunPodID, &j.Lyrics, &j.Caption,
			&j.Duration, &j.Seed, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &j)
	}
	return out, rows.Err()
}

// OrphanSubmission atomically records a remote id (when known) and fails a
// locally-claimed submission. This prevents a CAS error from losing the id
// and prevents restart from resubmitting a billable remote generation.
func (s *Store) OrphanSubmission(id, runpodID, reason string) error {
	res, err := s.db.Exec(`UPDATE jobs SET state = ?, runpod_id = ?, error = ?, updated_at = ?
		WHERE id = ? AND state = ?`, StateFailed, runpodID, reason,
		time.Now().UTC(), id, StateSubmitting)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: job %s is not submitting", ErrTransition, id)
	}
	return nil
}

// BumpRetries increments the transient-retry counter, returning the new value.
func (s *Store) BumpRetries(id string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`UPDATE jobs SET retries = retries + 1, updated_at = ?
		 WHERE id = ? RETURNING retries`, time.Now().UTC(), id).Scan(&n)
	return n, err
}

// CreateSong persists a finished song. OR IGNORE + the unique job_id index
// make it idempotent — the worker may retry after a crash mid-finish.
func (s *Store) CreateSong(g *Song) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO songs (id, job_id, lyrics, caption, duration_s, seed,
		  engine, delivery, audio_path, title, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.JobID, g.Lyrics, g.Caption, g.Duration, g.Seed,
		g.Engine, g.Delivery, g.AudioPath, g.Title, g.CreatedAt)
	return err
}

func (s *Store) Song(id string) (*Song, error) {
	row := s.db.QueryRow(`SELECT id, job_id, lyrics, caption, duration_s, seed,
		engine, delivery, audio_path, title, created_at FROM songs WHERE id = ?`, id)
	var g Song
	err := row.Scan(&g.ID, &g.JobID, &g.Lyrics, &g.Caption, &g.Duration, &g.Seed,
		&g.Engine, &g.Delivery, &g.AudioPath, &g.Title, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// SongForJob returns the (unique) song produced by a job, or nil.
func (s *Store) SongForJob(jobID string) (*Song, error) {
	row := s.db.QueryRow(`SELECT id, job_id, lyrics, caption, duration_s, seed,
		engine, delivery, audio_path, title, created_at FROM songs WHERE job_id = ?`, jobID)
	var g Song
	err := row.Scan(&g.ID, &g.JobID, &g.Lyrics, &g.Caption, &g.Duration, &g.Seed,
		&g.Engine, &g.Delivery, &g.AudioPath, &g.Title, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// Songs pages the library newest-first.
func (s *Store) Songs(limit, offset int) ([]*Song, error) {
	rows, err := s.db.Query(`SELECT id, job_id, lyrics, caption, duration_s, seed,
		engine, delivery, audio_path, title, created_at
		FROM songs ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Song
	for rows.Next() {
		var g Song
		if err := rows.Scan(&g.ID, &g.JobID, &g.Lyrics, &g.Caption, &g.Duration,
			&g.Seed, &g.Engine, &g.Delivery, &g.AudioPath, &g.Title, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &g)
	}
	return out, rows.Err()
}

// DeleteSong removes a song and its associated job in a single transaction.
// It returns the deleted song metadata (or nil if not found) so the caller can
// clean up the audio file from disk.
func (s *Store) DeleteSong(id string) (*Song, error) {
	g, err := s.Song(id)
	if err != nil || g == nil {
		return g, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM songs WHERE id = ?`, id); err != nil {
		return nil, err
	}
	if g.JobID != "" {
		if _, err := tx.Exec(`DELETE FROM jobs WHERE id = ?`, g.JobID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return g, nil
}

// UpdateSongTitle updates the title of a song.
func (s *Store) UpdateSongTitle(id, title string) error {
	res, err := s.db.Exec(`UPDATE songs SET title = ? WHERE id = ?`, title, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func join(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
