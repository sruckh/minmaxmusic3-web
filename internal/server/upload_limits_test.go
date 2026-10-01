package server

import (
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type blankAudioReader struct{}

func (blankAudioReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestCoverUploadCapRejectsRatherThanTruncates(t *testing.T) {
	_, s, _, _ := newCoverEnv(t)
	if name, err := s.storeUpload(io.LimitReader(blankAudioReader{}, maxUploadBytes+1), "audio.wav"); err == nil || name != "" {
		t.Fatalf("oversize was stored: %q %v", name, err)
	}
	files, err := os.ReadDir(s.sourceDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatal("oversize partial file retained")
	}
}

func TestMultipartRequestCapAndTemporaryCleanup(t *testing.T) {
	h, _, _, tok := newCoverEnv(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	prefix := "--boundary\r\nContent-Disposition: form-data; name=\"source_upload\"; filename=\"audio.wav\"\r\n\r\n"
	body := io.MultiReader(strings.NewReader(prefix), io.LimitReader(blankAudioReader{}, maxUploadBytes+(2<<20)), strings.NewReader("\r\n--boundary--\r\n"))
	req := httptest.NewRequest("POST", "/jobs", body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	req.AddCookie(cookieFor(tok))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 413 {
		t.Fatalf("oversize request status: %d %s", rec.Code, rec.Body.String())
	}
	files, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatal("failed multipart parse left temporary files")
	}
}
