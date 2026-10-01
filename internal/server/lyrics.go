package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/lyrics"
)

// Lookup is opt-in and separate from Generate: users review the draft before
// spending GPU time. No source is retained or signed by this route.
func (s *Server) handleLyricsLookup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	fail := func(code int, message string) { writeJSON(w, code, map[string]string{"error": message}) }
	if !s.cfg.Yue2Enabled() || !s.lyrics.Available() {
		fail(http.StatusServiceUnavailable, "Lyrics lookup is not configured. You can still supply lyrics or let YuE2 transcribe the recording.")
		return
	}
	if !s.lyricsLimiter.allow(s.caller(r).UserID) {
		w.Header().Set("Retry-After", "86400")
		fail(http.StatusTooManyRequests, "Lyrics lookup limit reached — try again tomorrow, or supply the words yourself.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	defer removeMultipart(r)
	if err := parseCoverForm(w, r); err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			fail(http.StatusRequestEntityTooLarge, "That recording is too large — keep it under 64 MB.")
		} else {
			fail(http.StatusBadRequest, "Choose an audio file and retry the lookup.")
		}
		return
	}
	if r.FormValue("find_lyrics") != "1" || r.MultipartForm == nil || len(r.MultipartForm.File["source_upload"]) != 1 {
		fail(http.StatusBadRequest, "Choose one recording and enable Find lyrics.")
		return
	}
	f, hdr, err := r.FormFile("source_upload")
	if err != nil {
		fail(http.StatusBadRequest, "Choose an audio file and retry the lookup.")
		return
	}
	defer f.Close()
	if hdr.Size <= 0 || hdr.Size > maxUploadBytes {
		fail(http.StatusBadRequest, "Choose a nonempty recording under 64 MB.")
		return
	}
	tmp, err := os.CreateTemp("", "mm3-lyrics-upload-*")
	if err != nil {
		fail(http.StatusInternalServerError, "Could not read the upload — retry in a moment.")
		return
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(f, maxUploadBytes+1))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil || n == 0 || n > maxUploadBytes {
		fail(http.StatusBadRequest, "Could not read the recording — choose a file under 64 MB.")
		return
	}
	result, err := s.lyrics.Lookup(ctx, tmp.Name())
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			fail(http.StatusGatewayTimeout, "Lyrics lookup took too long. Retry, supply the words, or leave them blank for transcription.")
		case errors.Is(err, lyrics.ErrInvalidAudio):
			fail(http.StatusBadRequest, "Could not identify that audio. Try a WAV, MP3, FLAC, Ogg, M4A or AAC file.")
		case errors.Is(err, lyrics.ErrRateLimited):
			w.Header().Set("Retry-After", "60")
			fail(http.StatusTooManyRequests, "The lyrics service is busy — retry in a minute.")
		default:
			fail(http.StatusBadGateway, "Lyrics lookup is unavailable right now. You can still make the cover without it.")
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}
