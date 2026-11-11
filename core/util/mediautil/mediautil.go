package mediautil

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/yapingcat/gomedia/go-mp4"
)

func split(mime string) (primary string, sub string, ok bool) {
	types := strings.Split(mime, "/")

	if len(types) != 2 {
		return "", "", false
	}

	return types[0], types[1], true
}

func IsVideo(mime string) bool {
	primary, _, ok := split(mime)

	return primary == "video" && ok
}

func IsAudio(mime string) bool {
	primary, _, ok := split(mime)

	return primary == "audio" && ok
}

func IsImage(mime string) bool {
	primary, _, ok := split(mime)

	return primary == "image" && ok
}

// GetMP4Info returns duration, width, height, error
func GetMP4Info(r io.ReadSeeker) (int, int, int, error) {
	d := mp4.CreateMp4Demuxer(r)

	tracks, err := d.ReadHead()
	if err != nil {
		return 0, 0, 0, err
	}

	for _, track := range tracks {
		if track.Cid == mp4.MP4_CODEC_H264 || track.Cid == mp4.MP4_CODEC_H265 {
			info := d.GetMp4Info()
			return int(info.Duration / info.Timescale), int(track.Width), int(track.Height), nil
		}
	}

	return 0, 0, 0, fmt.Errorf("no h264/h265 track found")
}

type VideoMeta struct {
	W        int
	H        int
	Duration float64
}

// ProbeVideo uses ffprobe to get video resolution and duration
func ProbeVideo(videoPath string) (*VideoMeta, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height:format=duration", "-of", "csv=p=0", videoPath)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	meta := &VideoMeta{}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, ",") {
			parts := strings.Split(line, ",")
			if len(parts) >= 2 {
				w, _ := strconv.Atoi(parts[0])
				h, _ := strconv.Atoi(parts[1])
				meta.W = w
				meta.H = h
			}
		} else if line != "" {
			dur, _ := strconv.ParseFloat(line, 64)
			meta.Duration = dur
		}
	}

	if meta.W == 0 && meta.H == 0 && meta.Duration == 0 {
		return nil, fmt.Errorf("empty probe output: %q", string(out))
	}

	return meta, nil
}

// ExtractThumbnail extracts a frame from video using ffmpeg, scaling it down to Telegram standard 320x320 box.
func ExtractThumbnail(videoPath string) (string, error) {
	tempFile, err := os.CreateTemp("", "tdl_thumb_*.jpg")
	if err != nil {
		return "", err
	}
	_ = tempFile.Close()

	// Try extracting frame at 1s with max 320x320 scale filter, fallback to 0s if it fails
	scaleFilter := "scale=w=320:h=320:force_original_aspect_ratio=decrease"
	cmd := exec.Command("ffmpeg", "-ss", "00:00:01", "-i", videoPath, "-vframes", "1", "-vf", scaleFilter, "-q:v", "2", tempFile.Name(), "-y")
	if _, err := cmd.CombinedOutput(); err != nil {
		cmd = exec.Command("ffmpeg", "-ss", "00:00:00", "-i", videoPath, "-vframes", "1", "-vf", scaleFilter, "-q:v", "2", tempFile.Name(), "-y")
		if out2, err2 := cmd.CombinedOutput(); err2 != nil {
			_ = os.Remove(tempFile.Name())
			return "", fmt.Errorf("ffmpeg failed: %v, detail: %s", err2, string(out2))
		}
	}

	return tempFile.Name(), nil
}
