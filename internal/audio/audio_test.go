package audio

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestEncodeAudioToM4A(t *testing.T) {
	wav := GenerateTestWAV(32000, 2, 3200) // 0.1s stereo
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "test.m4a")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := EncodeAudioToM4A(ctx, wav, outPath); err != nil {
		t.Fatalf("EncodeAudioToM4A error: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("output file stat error: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("output file is empty")
	}
}

// YuE2 answers with 48 kHz stereo FLAC where MiniMax answers with WAV, and both
// come through this one encoder. This is the regression guard for that: the
// source reaches us as bytes with no filename at all, so the encoder has to
// probe the container rather than trust an extension — which is exactly what
// breaks the day someone "tidies up" the extension-less temp file.
func TestEncodeAudioToM4AAcceptsFLAC(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dir := t.TempDir()
	srcWAV := filepath.Join(dir, "source.wav")
	if err := os.WriteFile(srcWAV, GenerateTestWAV(48000, 2, 4800), 0o600); err != nil {
		t.Fatalf("writing source wav: %v", err)
	}
	flac := filepath.Join(dir, "source.flac")
	if out, err := exec.CommandContext(ctx, "ffmpeg", "-y", "-i", srcWAV,
		"-ar", "48000", "-ac", "2", flac).CombinedOutput(); err != nil {
		t.Skipf("no usable ffmpeg to build a FLAC fixture: %v\n%s", err, out)
	}
	data, err := os.ReadFile(flac)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	outPath := filepath.Join(dir, "out.m4a")
	if err := EncodeAudioToM4A(ctx, data, outPath); err != nil {
		t.Fatalf("EncodeAudioToM4A(FLAC) error: %v", err)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("output file stat error: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("output file is empty")
	}
}
