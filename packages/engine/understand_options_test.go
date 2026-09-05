package engine

import (
	"testing"

	"github.com/exzork/mikkilens/packages/core/intent"
)

// What the model is offered as tools is built from the command file, so these
// pin the two things a wrong schema would cost: a slot that never reaches the
// model at all, and a slot marked compulsory for a command that can be said
// without one -- which pushes a model into inventing a value rather than
// leaving it out.

func optionsFrom(t *testing.T, document map[string]any) map[string]struct {
	slots    []string
	required []string
	phrases  []string
} {
	t.Helper()
	set, err := intent.SetFromMap(document, "test")
	if err != nil {
		t.Fatalf("SetFromMap: %v", err)
	}

	built := map[string]struct {
		slots    []string
		required []string
		phrases  []string
	}{}
	for _, option := range commandOptions(set) {
		built[option.ID] = struct {
			slots    []string
			required []string
			phrases  []string
		}{option.Slots, option.Required, option.Phrases}
	}
	return built
}

func TestASlotEveryPhrasingTakesIsRequired(t *testing.T) {
	built := optionsFrom(t, map[string]any{"commands": map[string]any{
		"switch_scene": map[string]any{"phrases": []any{
			"ganti ke {scene}", "pindah scene ke {scene}",
		}},
	}})

	scene := built["switch_scene"]
	if len(scene.slots) != 1 || scene.slots[0] != "scene" {
		t.Fatalf("slots are %v, want [scene]", scene.slots)
	}
	if len(scene.required) != 1 || scene.required[0] != "scene" {
		t.Errorf("required is %v, want [scene]", scene.required)
	}
}

func TestASlotOnlySomePhrasingsTakeIsOptional(t *testing.T) {
	built := optionsFrom(t, map[string]any{"commands": map[string]any{
		"stop_stream": map[string]any{"phrases": []any{
			"hentikan siaran",         // no slot at all
			"hentikan siaran {value}", // and one that takes one
		}},
	}})

	stop := built["stop_stream"]
	if len(stop.slots) != 1 || stop.slots[0] != "value" {
		t.Fatalf("slots are %v, want [value]", stop.slots)
	}
	if len(stop.required) != 0 {
		t.Errorf("required is %v, want none: it can be said without a value", stop.required)
	}
}

func TestACommandWithNoSlotsRequiresNothing(t *testing.T) {
	built := optionsFrom(t, map[string]any{"commands": map[string]any{
		"chat_pause": map[string]any{"phrases": []any{"jeda chat", "berhenti bacakan chat"}},
	}})

	pause := built["chat_pause"]
	if len(pause.slots) != 0 || len(pause.required) != 0 {
		t.Errorf("slots %v, required %v -- want neither", pause.slots, pause.required)
	}
	// The phrases are the tool's description, so losing them would leave the
	// model choosing between bare ids.
	if len(pause.phrases) != 2 {
		t.Errorf("phrases are %v, want both", pause.phrases)
	}
}
