package assets

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
)

// The two programs that play a song.
//
// They are fetched rather than shipped, and fetched with everything else at
// first launch rather than at the first song.
//
// Not shipped, because between them they are bigger than the whole of
// MikkiLens -- a full ffmpeg is about a hundred and ninety megabytes against
// an installer of ninety -- and because yt-dlp goes stale. YouTube changes
// something every few months and yt-dlp answers within days; one frozen into
// an installer would work until it quietly did not, and the failure would land
// mid-stream on a song that used to play. Fetched, it can be replaced.
//
// At first launch, because the alternative was tried and was worse. Fetching
// them the first time she played something meant that song began with eighty-
// five seconds of downloading -- announced, but mid-stream, which is precisely
// when there is no eighty-five seconds to spare. First launch is already the
// moment this application asks her to wait, so this waits with the rest of it.
//
// [MissingPlayer] is still consulted at the first song, and that is a safety
// net rather than the path: a machine whose first-run download failed, or one
// where music was switched on afterwards, should be a slow song rather than no
// song at all.
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
// Its own function rather than part of [Missing] because it answers a
// different question from a different setting: these are wanted when music is
// switched on, they are found by looking down the PATH as well as on disk, and
// a machine that already has ffmpeg needs only the eighteen megabytes of the
// other one. [WithMusic] is how it joins the first-run download.
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

// WithMusic folds the music programs into a first-run download.
//
// They are fetched at first launch rather than at first song, which was the
// other way round to begin with and wrong. Fetching them lazily meant the first
// "putar nomor dua" of a machine's life spent eighty-five seconds downloading
// before any music came out -- announced, but mid-stream, which is exactly when
// there is no eighty-five seconds to spare. First launch is already the moment
// this application asks her to wait for things.
//
// Spliced in ahead of the graphics build rather than appended. That build is
// six hundred megabytes and it is an upgrade -- recognition already works
// without it -- so leaving music behind it would mean a feature she asked for
// waiting on one she did not.
func WithMusic(wanted, music Wanted) Wanted {
	if music.Empty() {
		return wanted
	}

	combined := Wanted{Bytes: wanted.Bytes + music.Bytes}
	for _, stage := range wanted.Stages {
		if stage == StageGPU {
			combined.Stages = append(combined.Stages, music.Stages...)
			music.Stages = nil
		}
		combined.Stages = append(combined.Stages, stage)
	}
	// No graphics build to go in front of, so they go on the end.
	combined.Stages = append(combined.Stages, music.Stages...)
	return combined
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
