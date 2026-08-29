package hotkey_test

import (
	"runtime"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/audio/hotkey"
)

// The combination is parsed the way her config file writes it, so a config
// carried over from the Python version keeps working unchanged.

func TestParsesTheConfiguredCombination(t *testing.T) {
	for _, combination := range []string{
		"<ctrl>+<alt>+<space>",
		"<ctrl>+<shift>+m",
		"<f13>",
		"<num_0>",
	} {
		watcher, err := hotkey.New(hotkey.Options{Combination: combination})
		if err != nil {
			t.Errorf("%q: %v", combination, err)
			continue
		}
		if watcher.Combination() != combination {
			t.Errorf("Combination() = %q, want %q", watcher.Combination(), combination)
		}
	}
}

func TestRefusesSomethingItCannotRegister(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the hotkey is only available on Windows")
	}
	// Windows can only register modifiers plus one real key. Saying so here is
	// far better than a numeric failure later.
	for _, combination := range []string{"<ctrl>+<alt>", "<shift>", ""} {
		if _, err := hotkey.New(hotkey.Options{Combination: combination}); err == nil {
			t.Errorf("%q should have been refused", combination)
		}
	}
}

func TestRefusesAKeyItDoesNotKnow(t *testing.T) {
	if _, err := hotkey.New(hotkey.Options{Combination: "<wibble>+x"}); err == nil {
		t.Error("an unknown key name should be refused, with the name in the message")
	}
}

// TestRegistersAndReleases is the real check: Windows accepts the
// registration, and stopping gives the key back so a second run can take it.
func TestRegistersAndReleases(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the hotkey is only available on Windows")
	}

	// Something nothing else is likely to hold.
	const combination = "<ctrl>+<alt>+<shift>+<f24>"

	first, err := hotkey.New(hotkey.Options{
		Combination: combination, PushToTalk: true,
		OnActivate: func() {}, OnRelease: func() {},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := first.Start(); err != nil {
		t.Skipf("could not register %s here: %v", combination, err)
	}
	if !first.Running() {
		t.Error("the watcher should report itself running")
	}

	first.Stop()
	if first.Running() {
		t.Error("the watcher should report itself stopped")
	}

	// If Stop did not unregister, this second registration fails.
	second, err := hotkey.New(hotkey.Options{Combination: combination})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := second.Start(); err != nil {
		t.Fatalf("the key was not given back on Stop: %v", err)
	}
	second.Stop()
}

// TestTakingTheSameKeyTwiceIsReported: two engines fighting over the hotkey is
// something she should hear about, not something that silently half-works.
func TestTakingTheSameKeyTwiceIsReported(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the hotkey is only available on Windows")
	}
	const combination = "<ctrl>+<alt>+<shift>+<f23>"

	first, err := hotkey.New(hotkey.Options{Combination: combination})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Start(); err != nil {
		t.Skipf("could not register %s here: %v", combination, err)
	}
	defer first.Stop()

	second, err := hotkey.New(hotkey.Options{Combination: combination})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); err == nil {
		second.Stop()
		t.Error("registering the same key twice should have been refused")
	}
}

// TestStopIsPromptBecauseShutdownWaitsForIt: Stop runs while MikkiLens is
// closing, and a slow one delays everything after it.
func TestStopIsPrompt(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the hotkey is only available on Windows")
	}
	watcher, err := hotkey.New(hotkey.Options{Combination: "<ctrl>+<alt>+<shift>+<f22>"})
	if err != nil {
		t.Fatal(err)
	}
	if err := watcher.Start(); err != nil {
		t.Skipf("could not register: %v", err)
	}

	started := time.Now()
	watcher.Stop()
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("Stop took %v", elapsed)
	}
}
