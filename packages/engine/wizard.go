package engine

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/exzork/mikkilens/packages/audio/capture"
	"github.com/exzork/mikkilens/packages/audio/devices"
	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
)

// The wizard says everything it does.
//
// A device picker in particular is useless to someone who cannot see it, so
// instead of showing a list it plays a tone through each device in turn and
// names it: she chooses by hearing which one came out of her headphones.
// Numbers are typed rather than spoken because speech recognition is not up
// yet at this point in startup.

// micTestSeconds is how long the microphone check listens for.
const micTestSeconds = 3 * time.Second

// micSilentLevel is the peak below which a microphone is effectively silent:
// muted, unplugged, or simply the wrong device.
const micSilentLevel = 0.01

// CheckResult is one line of the self test.
type CheckResult struct {
	Item   string `json:"item"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Wizard runs the spoken first-run setup.
type Wizard struct {
	settings config.Config
	locale   *i18n.Locale
	bus      *feedback.Bus
	in       io.Reader
	out      io.Writer
}

// NewWizard builds a wizard around a speech bus and a terminal.
func NewWizard(settings config.Config, locale *i18n.Locale, bus *feedback.Bus, in io.Reader, out io.Writer) *Wizard {
	return &Wizard{settings: settings, locale: locale, bus: bus, in: in, out: out}
}

// Config is the settings as the wizard has left them.
func (w *Wizard) Config() config.Config { return w.settings }

// say speaks and waits, so a prompt never overlaps the tone that follows it.
func (w *Wizard) say(key string, args ...i18n.Args) {
	w.bus.SayKey(key, feedback.Confirm, args...)
	w.bus.WaitUntilIdle(30 * time.Second)
}

func (w *Wizard) printf(format string, args ...any) {
	fmt.Fprintf(w.out, format, args...)
}

// askNumber reads a 1-based choice. An empty line keeps what is set now.
func (w *Wizard) askNumber(prompt string, count int) (int, bool) {
	reader := bufio.NewReader(w.in)
	for {
		w.printf("%s [1-%d, Enter to keep current]: ", prompt, count)
		line, err := reader.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return 0, false
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return 0, false
		}
		if chosen, err := strconv.Atoi(trimmed); err == nil && chosen >= 1 && chosen <= count {
			return chosen - 1, true
		}
		w.printf("  Please type a number between 1 and %d.\n", count)
	}
}

// ChooseOutput plays a tone through each output device in turn, so she can
// pick the one she can actually hear.
func (w *Wizard) ChooseOutput() {
	outputs, err := devices.List(devices.Output)
	if err != nil || len(outputs) == 0 {
		w.bus.Error("error.no_audio_device")
		return
	}

	w.say("wizard.output_intro")
	for index, device := range outputs {
		w.printf("  %d. %s\n", index+1, device.Label())
		// The name is spoken through the *current* device so she always hears
		// the announcement; the tone then plays through the candidate, so she
		// can tell which physical device it is.
		w.say("wizard.output_device", i18n.Args{"index": index + 1, "name": device.Name})
		if err := devices.PlayTestTone(&outputs[index], 0.3); err != nil {
			w.printf("     (could not play a tone: %v)\n", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	chosen, ok := w.askNumber("Which device should MikkiLens speak through?", len(outputs))
	if !ok {
		return
	}
	device := outputs[chosen]
	w.settings.Speech.OutputDevice = device.Name
	w.bus.SetDevice(&device)
	w.say("wizard.output_chosen", i18n.Args{"name": device.Name})

	// Phase zero has no OBS connection yet, so this only flags the obvious
	// case: the Windows default output, which is what a desktop-audio capture
	// source grabs. The OBS controller replaces this with a real answer later.
	if device.IsDefault {
		w.say("wizard.output_warning")
	}
}

// ChooseInput picks a microphone and checks that something comes out of it.
func (w *Wizard) ChooseInput() {
	inputs, err := devices.List(devices.Input)
	if err != nil || len(inputs) == 0 {
		w.bus.Error("error.no_audio_device")
		return
	}

	for index, device := range inputs {
		w.printf("  %d. %s\n", index+1, device.Label())
	}
	if chosen, ok := w.askNumber("Which microphone should MikkiLens listen to?", len(inputs)); ok {
		w.settings.Audio.InputDevice = inputs[chosen].Name
	}

	device, _ := devices.Resolve(w.settings.Audio.InputDevice, devices.Input)
	w.say("wizard.input_intro")
	w.bus.Earcon("listening")

	stream := capture.NewStream(device)
	if err := stream.Start(); err != nil {
		w.say("wizard.input_silent")
		return
	}
	defer stream.Stop()

	if capture.Measure(stream, micTestSeconds) < micSilentLevel {
		w.say("wizard.input_silent")
		return
	}
	w.say("wizard.input_ok")
}

// SelfTest checks each layer and reads the result aloud.
func (w *Wizard) SelfTest(ctx context.Context) []CheckResult {
	w.say("wizard.selftest_intro")

	results := []CheckResult{
		w.checkOutput(),
		w.checkInput(),
		w.checkVoice(),
		w.checkRecognition(ctx),
		w.checkModel(),
	}
	for _, result := range results {
		if result.OK {
			w.say("wizard.selftest_ok", i18n.Args{"item": result.Item})
			continue
		}
		w.say("wizard.selftest_fail",
			i18n.Args{"item": result.Item, "reason": result.Detail})
	}
	return results
}

func (w *Wizard) checkOutput() CheckResult {
	device, err := devices.Resolve(w.settings.Speech.OutputDevice, devices.Output)
	if err != nil {
		return CheckResult{Item: "Speaker", Detail: err.Error()}
	}
	if device == nil {
		return CheckResult{Item: "Speaker", OK: true, Detail: "system default"}
	}
	return CheckResult{Item: "Speaker", OK: true, Detail: device.Name}
}

func (w *Wizard) checkInput() CheckResult {
	device, err := devices.Resolve(w.settings.Audio.InputDevice, devices.Input)
	if err != nil {
		return CheckResult{Item: "Microphone", Detail: err.Error()}
	}
	if device == nil {
		found, err := devices.List(devices.Input)
		if err != nil || len(found) == 0 {
			return CheckResult{Item: "Microphone", Detail: "no microphone found"}
		}
		return CheckResult{Item: "Microphone", OK: true, Detail: "system default"}
	}
	return CheckResult{Item: "Microphone", OK: true, Detail: device.Name}
}

// checkVoice reports which voice is in use. By the time this runs she has
// already heard several sentences, so the voice demonstrably works.
func (w *Wizard) checkVoice() CheckResult {
	return CheckResult{
		Item: "Voice", OK: true,
		Detail: w.settings.Voice(w.locale.DefaultVoice()),
	}
}

func (w *Wizard) checkRecognition(ctx context.Context) CheckResult {
	transcriber := newTranscriberFor(w.settings)
	if err := transcriber.Load(ctx); err != nil {
		return CheckResult{Item: "Speech recognition", Detail: err.Error()}
	}
	return CheckResult{Item: "Speech recognition", OK: true, Detail: transcriber.Describe()}
}

func (w *Wizard) checkModel() CheckResult {
	if !w.settings.Model.Configured() {
		return CheckResult{Item: "Model", Detail: "not configured yet"}
	}
	return CheckResult{Item: "Model", OK: true, Detail: w.settings.Model.Model}
}

// Run walks the whole first-run setup and saves the result.
func (w *Wizard) Run(ctx context.Context) (config.Config, error) {
	w.say("wizard.welcome")
	w.ChooseOutput()
	w.ChooseInput()
	w.SelfTest(ctx)

	path, err := w.settings.Save("")
	if err != nil {
		return w.settings, err
	}
	w.say("wizard.done")
	w.printf("\nSaved configuration to %s\n", path)
	return w.settings, nil
}
