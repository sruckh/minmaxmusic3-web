package lyrics

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Host tests can lack fpcalc; the Docker test stage installs it, so that build
// exercises real decoding rather than treating a mocked fingerprint as proof.
func TestPackagedFingerprintKeepsFullDuration(t *testing.T) {
	c := New("fixture-key")
	if !c.Available() {
		t.Skip("requires local ffmpeg, ffprobe and fpcalc; Docker installs all three")
	}
	const rate = 11025
	const seconds = 135
	data := make([]byte, 44+rate*seconds*2)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], rate)
	binary.LittleEndian.PutUint32(data[28:], rate*2)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(len(data)-44))
	for i := range rate * seconds {
		sample := int16(12000 * math.Sin(2*math.Pi*440*float64(i)/rate))
		binary.LittleEndian.PutUint16(data[44+i*2:], uint16(sample))
	}
	path := filepath.Join(t.TempDir(), "recording")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	duration, fp, err := c.localFingerprint(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(duration-seconds) > 0.01 || fp == "" {
		t.Fatalf("fingerprint duration=%v, bytes=%d", duration, len(fp))
	}
	files, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("decoded temp files retained: %v", files)
	}
	playlist := filepath.Join(t.TempDir(), "playlist")
	if err := os.WriteFile(playlist, []byte("#EXTM3U\nhttp://127.0.0.1:9/audio\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.localFingerprint(context.Background(), playlist); err == nil {
		t.Fatal("playlist accepted")
	}
}
