package assets

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
)

// The two programs that play a song.
//
// They are fetched rather than shipped, and fetched late rather than at
// startup, which is two separate decisions.
//
// Not shipped, because between them they are bigger than the whole of
// MikkiLens -- a full ffmpeg is about a hundred and ninety megabytes against
// an installer of ninety -- and because yt-dlp goes stale. YouTube changes
// something every few months and yt-dlp answers within days; one frozen into
// an installer would work until it quietly did not, and the failure would land
// mid-stream on a song that used to play. Fetched, it can be replaced.
//
// Not at startup, because the first run already asks her to wait for half a
// gigabyte of speech model before MikkiLens can hear at all, and a person who
// never asks for a song should never pay for one. So these come down the first
// time she plays something, announced like everything else.
//
// Anything already on the machine wins over both. ffmpeg in particular is
// already installed on a lot of streaming machines, and downloading a second
// copy of a program she has would be rude about her disk and her connection.

const (
	// StagePlayer is yt-dlp, which works out the audio URL behind a song.
	StagePlayer Stage = "player"
	// StageFFmpeg is ffmpeg, which decodes it as it arrives.
	StageFFmpeg Stage = "ffmpeg"
)

// ytDlpURL is the release yt-dlp keeps up to date. Deliberately "latest"
// rather than pinned: a pinned yt-dlp is one that stops working, and the whole
// reason this is a download is so that it does not have to.
const ytDlpURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe"

// ffmpegURL is the build yt-dlp's own documentation points at.
const ffmpegURL = "https://github.com/yt-dlp/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip"

// PlayerTools is where the two programs were found, empty when they were not.
type PlayerTools struct {
	YtDlp  string
	FFmpeg string
}

// Ready reports whether a song can be played right now.
func (t PlayerTools) Ready() bool { return t.YtDlp != "" && t.FFmpeg != "" }

// FindPlayerTools looks for both, preferring what she has already.
//
// Config first, because someone who has written a path there has a reason;
// then MikkiLens's own copy; then the PATH, which is where a machine that
// already runs ffmpeg for something else will have it.
func FindPlayerTools(configuredYtDlp, configuredFFmpeg string) PlayerTools {
	return PlayerTools{
		YtDlp:  findTool(configuredYtDlp, "yt-dlp.exe", "yt-dlp"),
		FFmpeg: findTool(configuredFFmpeg, "ffmpeg.exe", "ffmpeg"),
	}
}

func findTool(configured, fileName, commandName string) string {
	if path := strings.TrimSpace(configured); path != "" {
		if exists(path) {
			return path
		}
		// A configured path that is not there is worth reporting rather than
		// silently falling back to a different program than the one asked for.
		return ""
	}
	if own := filepath.Join(modelsDir(), fileName); exists(own) {
		return own
	}
	if found, err := exec.LookPath(commandName); err == nil {
		return found
	}
	return ""
}

// MissingPlayer is what still has to be fetched before a song can play.
//
// Its own function rather than part of [Missing]: these are wanted the first
// time she asks for a song, not at startup, and a machine that already has
// ffmpeg needs only the eighteen megabytes of the other one.
func MissingPlayer(configuredYtDlp, configuredFFmpeg string) Wanted {
	tools := FindPlayerTools(configuredYtDlp, configuredFFmpeg)

	wanted := Wanted{}
	if tools.YtDlp == "" {
		wanted.Stages = append(wanted.Stages, StagePlayer)
	}
	if tools.FFmpeg == "" {
		wanted.Stages = append(wanted.Stages, StageFFmpeg)
	}
	for _, stage := range wanted.Stages {
		wanted.Bytes += Bytes[stage]
	}
	return wanted
}

// fetchPlayer downloads yt-dlp, which is one executable and needs no unpacking.
func (i *Installer) fetchPlayer(ctx context.Context, track func(int64, int64, float64)) error {
	return download(ctx, ytDlpURL,
		filepath.Join(modelsDir(), "yt-dlp.exe"), Bytes[StagePlayer], track)
}

// fetchFFmpeg downloads the build and keeps the one file out of it that
// matters.
//
// The archive carries ffprobe and a pile of shared libraries as well. Only
// ffmpeg.exe is ever run, and unpacking the rest would cost another hundred
// megabytes of disk to hold programs nothing here calls.
func (i *Installer) fetchFFmpeg(ctx context.Context, track func(int64, int64, float64)) error {
	return i.fetchArchive(ctx, ffmpegURL, "ffmpeg.zip", modelsDir(), wantedFromFFmpeg, track)
}

func wantedFromFFmpeg(name string) bool {
	return strings.EqualFold(filepath.Base(name), "ffmpeg.exe")
}
