package lyrics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSubprocessBoundsAndCancellation(t *testing.T) {
	dir := t.TempDir()
	noisy := filepath.Join(dir, "noisy")
	if err := os.WriteFile(noisy, []byte("#!/bin/sh\nprintf '0123456789'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := run(context.Background(), noisy, nil, &output, 5); !errors.Is(err, ErrInvalidAudio) || output.Len() > 5 {
		t.Fatalf("output cap ignored: %v %d", err, output.Len())
	}
	sleepy := filepath.Join(dir, "sleepy")
	if err := os.WriteFile(sleepy, []byte("#!/bin/sh\nsleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := run(ctx, sleepy, nil, io.Discard, 100); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wrong cancellation error %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("subprocess children held lookup open")
	}
}
func TestLimiterQueuedCancellation(t *testing.T) {
	l := limiter{interval: time.Second}
	if err := l.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = l.wait(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	started := time.Now()
	if err := l.wait(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("queued request could not cancel")
	}
	cancel()
	wg.Wait()
}
func TestUnavailableAndInvalidDuration(t *testing.T) {
	for _, missing := range []string{"key", "ffmpeg", "ffprobe", "fpcalc"} {
		c := New("key")
		c.ffmpeg, c.ffprobe, c.fpcalc = "ffmpeg", "ffprobe", "fpcalc"
		switch missing {
		case "key":
			c.key = ""
		case "ffmpeg":
			c.ffmpeg = ""
		case "ffprobe":
			c.ffprobe = ""
		case "fpcalc":
			c.fpcalc = ""
		}
		if c.Available() {
			t.Errorf("available without %s", missing)
		}
	}
	for _, duration := range []float64{0, -1, 86401} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid duration contacted provider") })
		c.fingerprint = func(context.Context, string) (float64, string, error) { return duration, "fp", nil }
		if _, err := c.Lookup(context.Background(), "file"); !errors.Is(err, ErrInvalidAudio) {
			t.Fatal(err)
		}
	}
}
func TestHTTPTimeoutAndRedirect(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Lookup(ctx, "file"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout not propagated %v", err)
	}
	redirected := false
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/acoustid" {
			http.Redirect(w, r, "/leak", http.StatusFound)
		} else {
			redirected = true
			t.Error("followed provider redirect")
		}
	})
	if _, err := c.Lookup(context.Background(), "file"); !errors.Is(err, ErrProvider) || redirected {
		t.Fatalf("redirect not refused %v", err)
	}
}
