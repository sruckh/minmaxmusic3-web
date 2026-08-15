package audio

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEncodeWAVToM4A(t *testing.T) {
	wav := GenerateTestWAV(32000, 2, 3200) // 0.1s stereo
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "test.m4a")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := EncodeWAVToM4A(ctx, wav, outPath); err != nil {
		t.Fatalf("EncodeWAVToM4A error: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("output file stat error: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("output file is empty")
	}
}
