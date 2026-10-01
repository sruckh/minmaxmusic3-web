package lyrics

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const formats = "wav,mp3,flac,ogg,mov,aac"

// cappedWriter bounds both subprocess JSON and decoded PCM, including malformed inputs.
type cappedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("process output limit exceeded")
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}
func run(ctx context.Context, binary string, args []string, out io.Writer, limit int64) error {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	cmd.Stdout = &cappedWriter{writer: out, remaining: limit}
	// Never return subprocess diagnostics: inputs and provider secrets must not leak.
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrInvalidAudio
	}
	return nil
}

func (c *Client) localFingerprint(parent context.Context, path string) (float64, string, error) {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	absolute, err := filepath.Abs(path)
	if err != nil {
		return 0, "", ErrInvalidAudio
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 64<<20 {
		return 0, "", ErrInvalidAudio
	}
	var probe strings.Builder
	args := []string{"-v", "error", "-protocol_whitelist", "file,pipe", "-format_whitelist", formats, "-show_entries", "format=duration,format_name", "-of", "json", absolute}
	if err = run(ctx, c.ffprobe, args, &probe, 16384); err != nil {
		return 0, "", err
	}
	var metadata struct {
		Format struct {
			Duration string `json:"duration"`
			Name     string `json:"format_name"`
		} `json:"format"`
	}
	if json.Unmarshal([]byte(probe.String()), &metadata) != nil {
		return 0, "", ErrInvalidAudio
	}
	duration, err := strconv.ParseFloat(metadata.Format.Duration, 64)
	if err != nil || !validDuration(duration) {
		return 0, "", ErrInvalidAudio
	}
	allowed := false
	for _, name := range strings.Split(metadata.Format.Name, ",") {
		for _, format := range strings.Split(formats, ",") {
			if name == format {
				allowed = true
			}
		}
	}
	if !allowed {
		return 0, "", ErrInvalidAudio
	}
	dir, err := os.MkdirTemp("", "mm3-fingerprint-")
	if err != nil {
		return 0, "", ErrInvalidAudio
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "audio.wav")
	file, err := os.OpenFile(wav, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, "", ErrInvalidAudio
	}
	args = []string{"-nostdin", "-v", "error", "-threads", "1", "-protocol_whitelist", "file,pipe", "-format_whitelist", formats, "-i", absolute, "-map", "0:a:0", "-t", "120", "-vn", "-sn", "-dn", "-ac", "1", "-ar", "11025", "-c:a", "pcm_s16le", "-f", "wav", "pipe:1"}
	err = run(ctx, c.ffmpeg, args, file, 3<<20)
	closeErr := file.Close()
	if err != nil {
		return 0, "", err
	}
	if closeErr != nil {
		return 0, "", ErrInvalidAudio
	}
	var output strings.Builder
	if err = run(ctx, c.fpcalc, []string{"-json", "-length", "120", wav}, &output, 128<<10); err != nil {
		return 0, "", err
	}
	var fingerprint struct {
		Fingerprint string `json:"fingerprint"`
	}
	if json.Unmarshal([]byte(output.String()), &fingerprint) != nil || fingerprint.Fingerprint == "" {
		return 0, "", ErrInvalidAudio
	}
	// The decoded WAV stops at 120s; matching requires the ORIGINAL duration.
	return duration, fingerprint.Fingerprint, nil
}
