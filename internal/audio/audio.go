package audio

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// TranscodeMP3 transcodes a source audio file to MP3 192 kbps (no loudnorm) —
// used for music, which is already mastered.
func TranscodeMP3(ctx context.Context, srcPath, dstPath string) error {
	args := []string{"-y", "-i", srcPath, "-map", "0:a:0",
		"-codec:a", "libmp3lame", "-b:a", "192k", "-ar", "44100", "-ac", "2", dstPath}
	return runFFmpeg(ctx, args)
}

// NormalizeMP3 applies loudnorm + MP3 192 kbps — used for speech (news/intros).
func NormalizeMP3(ctx context.Context, srcPath, dstPath string) error {
	args := []string{"-y", "-i", srcPath,
		"-af", "loudnorm=I=-16:TP=-1.5:LRA=11",
		"-codec:a", "libmp3lame", "-b:a", "192k", "-ar", "44100", "-ac", "2", dstPath}
	return runFFmpeg(ctx, args)
}

// ProbeDuration returns the duration in seconds of an audio file via ffprobe.
func ProbeDuration(path string) (float64, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, err
	}
	return d, nil
}

func runFFmpeg(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg %v: %s", err, tail(out))
	}
	return nil
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		s = s[len(s)-400:]
	}
	return s
}
