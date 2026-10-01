package lyrics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const recording1 = "12345678-1234-1234-1234-123456789abc"
const recording2 = "23456789-1234-1234-1234-123456789abc"
const recording3 = "34567890-1234-1234-1234-123456789abc"
const recording4 = "45678901-1234-1234-1234-123456789abc"

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := New("private-key")
	c.ffmpeg, c.ffprobe, c.fpcalc = "fake", "fake", "fake"
	c.acoustid, c.musicbrainz, c.lrclib = server.URL+"/acoustid", server.URL+"/recording/", server.URL+"/lyrics/"
	c.acousticRate.interval, c.brainzRate.interval = 0, 0
	c.fingerprint = func(context.Context, string) (float64, string, error) { return 245, "fingerprint", nil }
	return c
}
func TestLookupExact(t *testing.T) {
	var musicCalls, searchCalls int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != userAgent {
			t.Error("missing identifying user agent")
		}
		switch {
		case r.URL.Path == "/acoustid":
			if r.Method != "POST" {
				t.Error("fingerprint must be POSTed")
			}
			_ = r.ParseForm()
			if r.Form.Get("client") != "private-key" || r.Form.Get("duration") != "245" || r.Form.Get("fingerprint") != "fingerprint" || r.Form.Get("meta") != "recordings recordingids" {
				t.Errorf("wrong lookup fields")
			}
			io.WriteString(w, `{"status":"ok","results":[{"score":0.98,"recordings":[{"id":"`+recording1+`"},{"id":"`+recording1+`"},{"id":"https://evil.invalid"}]}]}`)
		case strings.HasPrefix(r.URL.Path, "/recording/"):
			musicCalls++
			if r.URL.Query().Get("inc") != "artist-credits+releases" {
				t.Error("wrong MusicBrainz inclusion")
			}
			io.WriteString(w, `{"title":"Song","length":245000,"artist-credit":[{"name":"Artist"}],"releases":[{"title":"Album"}]}`)
		case r.URL.Path == "/lyrics/get":
			if r.URL.Query().Get("duration") != "245" || r.URL.Query().Get("track_name") != "Song" || r.URL.Query().Get("artist_name") != "Artist" {
				t.Error("wrong lyric metadata")
			}
			io.WriteString(w, `{"id":10,"trackName":"Song","artistName":"Artist","duration":245,"plainLyrics":"[Verse]\nReal words"}`)
		case r.URL.Path == "/lyrics/search":
			searchCalls++
			t.Error("unexpected fallback")
		default:
			t.Error("unexpected endpoint")
		}
	})
	got, err := c.Lookup(context.Background(), "local-file")
	if err != nil || got.AutoSelect != 0 || len(got.Candidates) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got.Candidates[0].Lyrics != "[Verse]\nReal words" || got.Candidates[0].Source != "LRCLIB" || got.Candidates[0].SourceURL != "https://lrclib.net/api/get/10" || musicCalls != 1 || searchCalls != 0 {
		t.Fatalf("wrong result %+v", got)
	}
}
func TestLookupBoundedAmbiguous(t *testing.T) {
	musicCalls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/acoustid":
			io.WriteString(w, `{"status":"ok","results":[{"score":0.95,"recordings":[{"id":"`+recording1+`"},{"id":"`+recording2+`"},{"id":"`+recording3+`"},{"id":"`+recording4+`"}]}]}`)
		case strings.HasPrefix(r.URL.Path, "/recording/"):
			musicCalls++
			io.WriteString(w, `{"title":"Song (Live)","length":245000,"disambiguation":"live","artist-credit":[{"artist":{"name":"Artist"}}]}`)
		case r.URL.Path == "/lyrics/get":
			w.WriteHeader(404)
		case r.URL.Path == "/lyrics/search":
			io.WriteString(w, `[{"id":1,"trackName":"Wrong song","artistName":"Artist","duration":245,"plainLyrics":"Wrong"},{"id":2,"trackName":"Song (Live)","artistName":"Other","duration":245,"plainLyrics":"Wrong"},{"id":3,"trackName":"Song (Live)","artistName":"Artist","duration":245,"syncedLyrics":"[ar:Artist]\n[00:01.25]Words\n[00:02.00][Chorus]\n[00:03.000]More"},{"id":4,"trackName":"Song (Live)","artistName":"Artist","duration":246,"plainLyrics":"Other variant"}]`)
		}
	})
	got, err := c.Lookup(context.Background(), "local-file")
	if err != nil || len(got.Candidates) != 3 || musicCalls != 3 || got.AutoSelect != -1 {
		t.Fatalf("got %+v, %v calls=%d", got, err, musicCalls)
	}
	for _, candidate := range got.Candidates {
		if !candidate.Ambiguous || candidate.Lyrics != "Words\n[Chorus]\nMore" {
			t.Errorf("wrong candidate %+v", candidate)
		}
	}
}
func TestProviderFailureBoundsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"malformed", "{", 200, ErrProvider}, {"oversized", strings.Repeat("x", maxBody+1), 200, ErrProvider}, {"rate limited", "", 429, ErrRateLimited}, {"failure", "private-key local-path", 500, ErrProvider}, {"status error", `{"status":"error"}`, 200, ErrProvider},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) })
			_, err := c.Lookup(context.Background(), "local-path")
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "private-key") || strings.Contains(err.Error(), "local-path") {
				t.Fatalf("wrong controlled error %v", err)
			}
		})
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("cancelled lookup contacted provider") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Lookup(ctx, "file")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	c.key = ""
	if c.Available() {
		t.Fatal("available without key")
	}
	_, err = c.Lookup(context.Background(), "file")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
func TestNoMatchAndPartialFailure(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"status":"ok","results":[]}`) })
	got, err := c.Lookup(context.Background(), "file")
	if err != nil || len(got.Candidates) != 0 || got.AutoSelect != -1 {
		t.Fatalf("got %+v %v", got, err)
	}
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/acoustid" {
			io.WriteString(w, `{"status":"ok","results":[{"score":0.99,"recordings":[{"id":"`+recording1+`"}]}]}`)
		} else {
			w.WriteHeader(503)
		}
	})
	got, err = c.Lookup(context.Background(), "file")
	if err != nil || got.AutoSelect != -1 || len(got.Warnings) == 0 || !got.Candidates[0].Ambiguous {
		t.Fatalf("wrong partial failure %+v %v", got, err)
	}
}
func TestLyricsMatchingAndLimits(t *testing.T) {
	candidate := Candidate{Title: "Song", Artist: "Artist", Duration: 245}
	for _, r := range []lyricRecord{{ID: 1, Track: "Other", Artist: "Artist", Duration: 245}, {ID: 1, Track: "Song", Artist: "Other", Duration: 245}, {ID: 1, Track: "Song", Artist: "Artist", Duration: 240}} {
		if matches(r, &candidate) {
			t.Error("incorrect title/artist/duration accepted")
		}
	}
	if !matches(lyricRecord{ID: 1, Track: " SONG ", Artist: "artist", Duration: 247}, &candidate) {
		t.Error("tolerance boundary rejected")
	}
	if err := applyLyrics(&candidate, lyricRecord{ID: 1, Instrumental: true}); err != nil || !candidate.Instrumental || candidate.Lyrics != "" {
		t.Fatal("instrumental not preserved")
	}
	if err := applyLyrics(&candidate, lyricRecord{ID: 2, Plain: strings.Repeat("a", maxLyrics+1)}); err == nil {
		t.Error("oversized lyrics silently accepted")
	}
	if got := stripLRC("[ti:Song]\n[00:01.00][00:02.00]Hello\n[Verse]\nWorld"); got != "Hello\n[Verse]\nWorld" {
		t.Fatalf("got %q", got)
	}
}
func TestLimiterCancellationAndSpacing(t *testing.T) {
	limiter := limiter{interval: 20 * time.Millisecond}
	first := time.Now()
	if err := limiter.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := limiter.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(first) < 20*time.Millisecond {
		t.Error("requests not spaced")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(limiter.wait(ctx), context.Canceled) {
		t.Error("limiter ignores cancellation")
	}
}
func TestDecodeConcurrency(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"status":"ok","results":[]}`) })
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	c.fingerprint = func(ctx context.Context, path string) (float64, string, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return 245, "fp", nil
		case <-ctx.Done():
			return 0, "", ctx.Err()
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.Lookup(context.Background(), "file") }()
	}
	<-entered
	<-entered
	select {
	case <-entered:
		t.Error("more than two decoders ran")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	wg.Wait()
}
func TestSecureFingerprintFullDurationAndCleanup(t *testing.T) {
	// Real decoder fixture stays local. No fpcalc dependency is required for this test:
	// a tiny fake reads only the trusted WAV, exposing the command's input to the test.
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	dir := t.TempDir()
	audio := filepath.Join(dir, "input.wav")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=130", "-ar", "11025", "-ac", "1", audio)
	if err = cmd.Run(); err != nil {
		t.Fatal("failed to create local fixture")
	}
	capture := filepath.Join(dir, "capture")
	fake := filepath.Join(dir, "fpcalc")
	// This test-only executable is invoked directly; production never invokes a shell.
	script := "#!/bin/sh\nprintf '%s' \"$4\" > '" + capture + "'\nprintf '%s' '{\"duration\":120,\"fingerprint\":\"trusted\"}'\n"
	if err = os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c := New("test")
	c.ffmpeg, c.ffprobe, c.fpcalc = ffmpeg, ffprobe, fake
	duration, fingerprint, err := c.localFingerprint(context.Background(), audio)
	if err != nil || duration < 129 || fingerprint != "trusted" {
		t.Fatalf("duration=%v fingerprint=%q err=%v", duration, fingerprint, err)
	}
	wav, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(string(wav)); !os.IsNotExist(err) {
		t.Error("temporary decoded audio leaked")
	}
	playlist := filepath.Join(dir, "playlist")
	if err = os.WriteFile(playlist, []byte("#EXTM3U\nhttps://example.invalid/audio.mp3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = c.localFingerprint(context.Background(), playlist); !errors.Is(err, ErrInvalidAudio) {
		t.Fatalf("playlist accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = c.localFingerprint(ctx, audio); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
