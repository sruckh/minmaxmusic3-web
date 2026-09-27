//go:build live

package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/abc"
	"github.com/sruckh/minmaxmusic3-web/internal/runpod"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// TestLiveYuE2Transpose asks whether YuE2 sings a transposed score in the new
// key — the question that decides whether a Transpose control is worth building.
//
// It takes one stored YuE2 song and renders its score twice as edits, with
// everything else — style, lyrics, seed — held equal:
//
//   - rekeyed:    only the K: fields changed. The old reasoning said this was
//     the whole of a key change; in fact it changes which notes are sharp or
//     flat, which is a change of mode.
//   - transposed: every note, chord symbol and key field moved by
//     abc.Transpose.
//
// Both, the original audio and the three scores are written to PROBE_OUT for
// listening and pitch analysis on the host. Two GPU jobs.
//
//	PROBE_SONG_ID    a YuE2 song to use (default: the newest one with a score)
//	PROBE_SEMITONES  how far to move it (default 2)
//	PROBE_OUT        where to write the results (default /probe-out)
func TestLiveYuE2Transpose(t *testing.T) {
	endpoint, key := liveEnv(t)
	st := liveStore(t)
	song := scoredSong(t, st)

	semitones := 2
	if v := os.Getenv("PROBE_SEMITONES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("PROBE_SEMITONES=%q: %v", v, err)
		}
		semitones = n
	}

	// Checked before any GPU time is spent: results with nowhere to go are a
	// wasted run.
	out := os.Getenv("PROBE_OUT")
	if out == "" {
		out = "/probe-out"
	}
	if err := os.WriteFile(filepath.Join(out, ".writable"), nil, 0o644); err != nil {
		t.Fatalf("PROBE_OUT %s is not writable — mount a host directory there: %v", out, err)
	}
	os.Remove(filepath.Join(out, ".writable"))

	transposed, err := abc.Transpose(song.ScoreABC, semitones)
	if err != nil {
		t.Fatalf("transposing song %s: %v", song.ID, err)
	}
	rekeyed, err := rekeyOnly(song.ScoreABC, semitones)
	if err != nil {
		t.Fatalf("rekeying song %s: %v", song.ID, err)
	}
	t.Logf("song %s | %s", song.ID, scoreMetaOfForProbe(song.ScoreABC))
	t.Logf("moving %+d semitones: rekeyed %s | transposed %s",
		semitones, scoreMetaOfForProbe(rekeyed), scoreMetaOfForProbe(transposed))

	c := &runpod.Client{Endpoint: endpoint, APIKey: key}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Minute)
	defer cancel()

	// Both submitted before either is awaited, so the second queues while the
	// first renders.
	variants := []struct{ name, score string }{{"rekeyed", rekeyed}, {"transposed", transposed}}
	ids := make([]string, len(variants))
	for i, v := range variants {
		job := &store.Job{
			Engine:       store.EngineYue2,
			Mode:         store.ModeEdit,
			Caption:      song.Caption,
			Lyrics:       song.Lyrics,
			Instrumental: strings.TrimSpace(song.Lyrics) == "",
			ABC:          v.score,
			Seed:         song.Seed,
		}
		ids[i] = submitRetrying(t, ctx, c, requestFor(job), v.name)
		t.Logf("%s accepted: %s", v.name, ids[i])
	}

	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(out, name), data, 0o644); err != nil {
			t.Errorf("writing %s: %v", name, err)
		}
	}
	write("original.abc", []byte(song.ScoreABC))
	if data, err := os.ReadFile(song.AudioPath); err == nil {
		write("original"+filepath.Ext(song.AudioPath), data)
	} else {
		t.Errorf("reading the original audio: %v", err)
	}

	summary := []string{
		fmt.Sprintf("song %s, moved %+d semitones, seed %v", song.ID, semitones, derefSeed(song.Seed)),
		"original:   " + scoreMetaOfForProbe(song.ScoreABC),
	}
	for i, v := range variants {
		res := waitForJob(t, ctx, c, ids[i])
		checkEditOutput(t, res)
		data, err := (&Worker{log: testLogger(t)}).fetch(ctx, res.AudioURL)
		if err != nil {
			t.Errorf("fetching %s audio: %v", v.name, err)
			continue
		}
		write(v.name+".abc", []byte(v.score))
		write(v.name+audioExt(data), data)
		line := fmt.Sprintf("%-11s %s | %.1fs | runpod %s", v.name+":", scoreMetaOfForProbe(v.score), res.Duration, ids[i])
		summary = append(summary, line)
		t.Log(line)
	}
	write("summary.txt", []byte(strings.Join(summary, "\n")+"\n"))
	t.Logf("results in %s", out)
}

// scoredSong is PROBE_SONG_ID, or the newest YuE2 song with a score and chord
// symbols — a score with harmony, so a key change has chords to move too.
func scoredSong(t *testing.T, st *store.Store) *store.Song {
	t.Helper()
	if id := os.Getenv("PROBE_SONG_ID"); id != "" {
		s, err := st.Song(id, store.Access{Admin: true})
		if err != nil || s == nil {
			t.Fatalf("PROBE_SONG_ID %s: not found (%v)", id, err)
		}
		if strings.TrimSpace(s.ScoreABC) == "" {
			t.Fatalf("PROBE_SONG_ID %s has no score", id)
		}
		return s
	}
	songs, err := st.Songs(50, 0, store.Access{Admin: true})
	if err != nil {
		t.Fatalf("listing songs: %v", err)
	}
	for _, s := range songs {
		if s.Engine == store.EngineYue2 && hasChords(s.ScoreABC) && s.AudioPath != "" {
			return s
		}
	}
	t.Skip("no YuE2 song with a chord-bearing score among the newest 50")
	return nil
}

// hasChords reports a quoted chord symbol on a music line. The V: header lines
// quote their names too, so a bare search for `"` finds those — which is how
// the first run picked a melody-only cover score.
func hasChords(score string) bool {
	for _, line := range strings.Split(score, "\n") {
		line = strings.TrimSpace(line)
		if len(line) > 1 && line[1] == ':' || strings.HasPrefix(line, "%") {
			continue
		}
		if strings.Contains(line, `"`) {
			return true
		}
	}
	return false
}

// rekeyOnly changes the K: fields to the transposed key and nothing else —
// the edit the old reasoning thought was a key change.
func rekeyOnly(score string, semitones int) (string, error) {
	lines := strings.Split(score, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "K:") {
			continue
		}
		name, rest, _ := strings.Cut(strings.TrimSpace(trimmed[2:]), " ")
		moved, err := abc.TransposeKey(name, semitones)
		if err != nil {
			return "", err
		}
		lines[i] = strings.TrimSpace("K:" + moved + " " + rest)
	}
	return strings.Join(lines, "\n"), nil
}

func audioExt(data []byte) string {
	if len(data) >= 4 && string(data[:4]) == "fLaC" {
		return ".flac"
	}
	if len(data) >= 4 && string(data[:4]) == "RIFF" {
		return ".wav"
	}
	return ".bin"
}

func derefSeed(s *int64) any {
	if s == nil {
		return "unset"
	}
	return *s
}
