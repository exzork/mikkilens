package assets

import "testing"

// Where the music programs sit in a first-run download.
//
// The ordering is the whole point of WithMusic. The graphics build is six
// hundred megabytes and is an upgrade -- recognition already works without it
// -- so music waiting behind it would mean a feature she asked for waiting on
// one she did not.

func stages(wanted Wanted) []Stage { return wanted.Stages }

func TestMusicGoesAheadOfTheGraphicsBuild(t *testing.T) {
	recognition := Wanted{
		Stages: []Stage{StageEngine, StageModel, StageWake, StageGPU},
		Bytes:  100,
	}
	music := Wanted{Stages: []Stage{StagePlayer, StageFFmpeg}, Bytes: 10}

	got := WithMusic(recognition, music)
	want := []Stage{StageEngine, StageModel, StageWake, StagePlayer, StageFFmpeg, StageGPU}

	if len(got.Stages) != len(want) {
		t.Fatalf("stages = %v, want %v", stages(got), want)
	}
	for index, stage := range want {
		if got.Stages[index] != stage {
			t.Errorf("stage %d = %q, want %q", index, got.Stages[index], stage)
		}
	}
	if got.Bytes != 110 {
		t.Errorf("bytes = %d, want 110", got.Bytes)
	}
}

// Most machines have no graphics build to go in front of.
func TestMusicGoesLastWithNoGraphicsBuild(t *testing.T) {
	recognition := Wanted{Stages: []Stage{StageEngine, StageModel}, Bytes: 100}
	music := Wanted{Stages: []Stage{StagePlayer}, Bytes: 10}

	got := WithMusic(recognition, music)
	want := []Stage{StageEngine, StageModel, StagePlayer}

	if len(got.Stages) != len(want) {
		t.Fatalf("stages = %v, want %v", stages(got), want)
	}
	for index, stage := range want {
		if got.Stages[index] != stage {
			t.Errorf("stage %d = %q, want %q", index, got.Stages[index], stage)
		}
	}
}

// A machine that already has both programs must not have its first-run
// download grow by a byte, and must not be given an empty stage to announce.
func TestNothingIsAddedWhenBothProgramsAreThere(t *testing.T) {
	recognition := Wanted{Stages: []Stage{StageEngine}, Bytes: 100}

	got := WithMusic(recognition, Wanted{})
	if len(got.Stages) != 1 || got.Stages[0] != StageEngine {
		t.Errorf("stages = %v, want just the engine", stages(got))
	}
	if got.Bytes != 100 {
		t.Errorf("bytes = %d, want 100", got.Bytes)
	}
}

// Music switched on for a machine that needs nothing else: the download is the
// music programs and only them, rather than nothing at all.
func TestMusicAloneIsStillADownload(t *testing.T) {
	music := Wanted{Stages: []Stage{StagePlayer, StageFFmpeg}, Bytes: 110}

	got := WithMusic(Wanted{}, music)
	if got.Empty() {
		t.Fatal("music alone came back as nothing to do")
	}
	if len(got.Stages) != 2 || got.Bytes != 110 {
		t.Errorf("got %v and %d bytes", stages(got), got.Bytes)
	}
}

// Both are named in both languages, or a download announces itself with a
// bracketed key instead of a sentence.
func TestTheMusicStagesHaveASizeToSay(t *testing.T) {
	for _, stage := range []Stage{StagePlayer, StageFFmpeg} {
		if Bytes[stage] <= 0 {
			t.Errorf("stage %q has no size, so it cannot be announced", stage)
		}
	}
}
