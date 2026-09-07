package assets

import (
	"slices"
	"testing"
)

// Where the local voice sits in a first-run download, and who is made to wait
// for it.

func TestTheVoiceGoesAheadOfTheGraphicsBuild(t *testing.T) {
	recognition := Wanted{
		Stages: []Stage{StageEngine, StageModel, StageWake, StageGPU},
		Bytes:  100,
	}
	voice := Wanted{Stages: []Stage{StageVoice}, Bytes: 400}

	got := WithVoice(recognition, voice)
	want := []Stage{StageEngine, StageModel, StageWake, StageVoice, StageGPU}

	if !slices.Equal(got.Stages, want) {
		t.Errorf("stages = %v, want %v", got.Stages, want)
	}
	if got.Bytes != 500 {
		t.Errorf("bytes = %d, want 500", got.Bytes)
	}
}

// Most machines have no graphics build to go in front of.
func TestTheVoiceGoesLastWithNoGraphicsBuild(t *testing.T) {
	recognition := Wanted{Stages: []Stage{StageEngine, StageModel}, Bytes: 100}
	voice := Wanted{Stages: []Stage{StageVoice}, Bytes: 400}

	got := WithVoice(recognition, voice)
	want := []Stage{StageEngine, StageModel, StageVoice}

	if !slices.Equal(got.Stages, want) {
		t.Errorf("stages = %v, want %v", got.Stages, want)
	}
}

// The voice goes in front of the music. Both are things she asked for, but one
// of them is how MikkiLens talks at all.
func TestTheVoiceGoesAheadOfTheMusic(t *testing.T) {
	recognition := Wanted{Stages: []Stage{StageEngine, StageModel, StageGPU}}

	// The order the engine applies them in.
	got := WithMusic(
		WithVoice(recognition, Wanted{Stages: []Stage{StageVoice}}),
		Wanted{Stages: []Stage{StagePlayer, StageFFmpeg}})

	voiceAt := slices.Index(got.Stages, StageVoice)
	playerAt := slices.Index(got.Stages, StagePlayer)
	graphicsAt := slices.Index(got.Stages, StageGPU)

	if voiceAt < 0 || playerAt < 0 || graphicsAt < 0 {
		t.Fatalf("stages = %v, want all three present", got.Stages)
	}
	if voiceAt > playerAt {
		t.Errorf("stages = %v, want the voice before the music", got.Stages)
	}
	if playerAt > graphicsAt {
		t.Errorf("stages = %v, want the music before the graphics build", got.Stages)
	}
}

// Nothing changes for somebody with no local voice to fetch.
func TestNothingIsAddedWhenThereIsNoVoiceToFetch(t *testing.T) {
	recognition := Wanted{Stages: []Stage{StageEngine, StageModel}, Bytes: 100}

	got := WithVoice(recognition, Wanted{})
	if !slices.Equal(got.Stages, recognition.Stages) || got.Bytes != recognition.Bytes {
		t.Errorf("stages = %v (%d bytes), want them untouched", got.Stages, got.Bytes)
	}
}

// Somebody reading through the online voices should never be made to wait for
// four hundred megabytes they will not use.
func TestTheOtherEnginesFetchNothing(t *testing.T) {
	for _, engine := range []string{"online", "windows"} {
		if got := MissingVoice(engine); !got.Empty() {
			t.Errorf("MissingVoice(%q) = %v, want nothing", engine, got.Stages)
		}
	}
}

// An empty engine means local everywhere else, and it has to mean local here
// too -- otherwise the default configuration never downloads its own voice.
func TestAnEmptyEngineStillWantsTheVoice(t *testing.T) {
	empty := MissingVoice("")
	local := MissingVoice("local")
	if !slices.Equal(empty.Stages, local.Stages) {
		t.Errorf("MissingVoice(\"\") = %v, MissingVoice(\"local\") = %v; want the same",
			empty.Stages, local.Stages)
	}
}

// The size is said out loud before the download starts, so it has to be there.
func TestTheVoiceHasASizeToAnnounce(t *testing.T) {
	if Bytes[StageVoice] <= 0 {
		t.Error("the voice stage has no size, so the download announces nothing")
	}
}
