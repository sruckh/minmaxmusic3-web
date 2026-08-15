// Package audio handles audio transcoding using ffmpeg.
package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// GenerateTestWAV creates a minimal valid 16-bit stereo PCM WAV byte slice for tests.
func GenerateTestWAV(sampleRate, channels, numSamples int) []byte {
	var buf bytes.Buffer
	dataSize := uint32(numSamples * channels * 2)
	byteRate := uint32(sampleRate * channels * 2)
	blockAlign := uint16(channels * 2)
	totalChunkSize := 36 + dataSize

	// RIFF header
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, totalChunkSize)
	buf.WriteString("WAVE")

	// fmt subchunk
	buf.WriteString("fmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16)) // Subchunk1Size
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))  // AudioFormat (PCM = 1)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&buf, binary.LittleEndian, byteRate)
	_ = binary.Write(&buf, binary.LittleEndian, blockAlign)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(16)) // BitsPerSample

	// data subchunk
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, dataSize)
	buf.Write(make([]byte, dataSize))

	return buf.Bytes()
}

// EncodeWAVToM4A transcodes raw WAV audio bytes into a stereo 192kbps AAC .m4a file.
func EncodeWAVToM4A(ctx context.Context, wavData []byte, outPath string) error {
	tmpFile, err := os.CreateTemp("", "mm3-transcode-*.wav")
	if err != nil {
		return fmt.Errorf("creating temp wav file: %w", err)
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)

	if _, err := tmpFile.Write(wavData); err != nil {
		tmpFile.Close()
		return fmt.Errorf("writing temp wav file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("closing temp wav file: %w", err)
	}

	return EncodeWAVFileToM4A(ctx, tmpName, outPath)
}

// EncodeWAVFileToM4A converts a WAV file on disk to a stereo 192kbps AAC .m4a file using ffmpeg.
func EncodeWAVFileToM4A(ctx context.Context, inWAVPath, outM4APath string) error {
	if err := os.MkdirAll(filepath.Dir(outM4APath), 0o750); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-i", inWAVPath, "-c:a", "aac", "-b:a", "192k", "-ac", "2", outM4APath)
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		_ = os.Remove(outM4APath)
		return fmt.Errorf("ffmpeg transcoding failed (%w): %s", err, stderr.String())
	}
	return nil
}
