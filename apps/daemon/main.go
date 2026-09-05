// Command mikkilensd is the voice engine.
//
// Running it with no arguments starts MikkiLens. The other subcommands exist
// mostly so a problem can be diagnosed by ear, without reading a screen: say,
// devices, earcons, listen and selftest each exercise one layer on its own.
//
// The desktop app spawns this binary and talks to it over the local API, but
// nothing here depends on that: the engine runs perfectly well on its own, and
// closing the window never stops her stream being controllable.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/exzork/mikkilens/packages/audio/devices"
	"github.com/exzork/mikkilens/packages/audio/earcons"
	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/audio/tts"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/paths"
	"github.com/exzork/mikkilens/packages/engine"
	"github.com/exzork/mikkilens/packages/httpapi"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(arguments []string) int {
	global := flag.NewFlagSet("mikkilensd", flag.ContinueOnError)
	verbose := global.Bool("v", false, "log everything, not just what matters")
	language := global.String("language", "", "override the spoken language for this run")
	global.Usage = func() { usage(global.Output()) }

	// Split the global flags from the subcommand and its own flags.
	command, rest := splitCommand(arguments)
	if err := global.Parse(rest.flags); err != nil {
		return 2
	}

	configureLogging(*verbose)

	switch command {
	case "", "run":
		return commandRun(rest.arguments, *language)
	case "setup":
		return commandSetup(*language)
	case "selftest":
		return commandSelfTest(*language)
	case "devices":
		return commandDevices()
	case "listen":
		return commandListen(rest.arguments, *language)
	case "say":
		return commandSay(rest.arguments, *language)
	case "earcons":
		return commandEarcons(*language)
	case "warmup":
		return commandWarmup(*language)
	case "enable-obs":
		return commandEnableOBS(*language)
	case "do":
		return commandDo(rest.arguments)
	case "help", "-h", "--help":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "mikkilensd: unknown command %q\n\n", command)
		usage(os.Stderr)
		return 2
	}
}

type parsed struct {
	flags     []string
	arguments []string
}

// splitCommand pulls the subcommand out of the argument list, wherever the
// global flags happen to sit around it.
func splitCommand(arguments []string) (string, parsed) {
	result := parsed{}
	command := ""

	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch {
		case command == "" && strings.HasPrefix(argument, "-"):
			result.flags = append(result.flags, argument)
			// --language takes a value; -v does not.
			if strings.Contains(argument, "language") && !strings.Contains(argument, "=") &&
				index+1 < len(arguments) {
				index++
				result.flags = append(result.flags, arguments[index])
			}
		case command == "":
			command = argument
		default:
			result.arguments = append(result.arguments, argument)
		}
	}
	return command, result
}

func usage(out io.Writer) {
	fmt.Fprint(out, `MikkiLens -- voice-operated stream control.

Usage:
  mikkilensd [flags] <command> [arguments]

Commands:
  run                 run MikkiLens (the default)
  setup               spoken first-run setup
  selftest            check every part and read the result aloud
  devices             list the audio devices
  listen [-n 3]       record and transcribe, for testing
  say <text>          speak some text, to test the voice
  earcons             play every tone in turn
  warmup              load the models ahead of time
  enable-obs          turn on the OBS WebSocket server and copy its password
  do <command>        run a command in the running MikkiLens, as if she had
                      said it -- this is what a Stream Deck button runs
  do --list           list the commands a button or a key can run

Flags:
  -v                  log everything
  --language <code>   override the spoken language for this run

Environment:
  MIKKILENS_HOME      where config.toml and data/ live
  MIKKILENS_SILENT=1  suppress all sound without changing anything else
`)
}

func configureLogging(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}

	writers := []io.Writer{os.Stderr}
	if _, err := paths.EnsureDataDir(); err == nil {
		file, err := os.OpenFile(paths.LogFile(),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			writers = append(writers, file)
		}
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(
		io.MultiWriter(writers...), &slog.HandlerOptions{Level: level})))
}

// load reads the configuration.
//
// A broken config is spoken about rather than only printed: a message that
// only reaches the terminal reaches nobody who is working by ear.
func load(languageOverride string) (config.Config, *i18n.Locale, error) {
	settings, err := config.Load("")
	if err != nil {
		locale := i18n.Load(settings.Language.Output)
		bus := feedback.New(settings, locale, nil)
		bus.Start()
		bus.SayKey("error.config_invalid", feedback.Error, i18n.Args{"reason": err.Error()})
		bus.WaitUntilIdle(20 * time.Second)
		bus.Stop()
		return settings, locale, err
	}
	if languageOverride != "" {
		settings.Language.Output = languageOverride
	}
	return settings, i18n.Load(settings.Language.Output), nil
}

// speakingBus builds a started speech bus for the one-shot commands.
func speakingBus(settings config.Config, locale *i18n.Locale) *feedback.Bus {
	device, err := devices.Resolve(settings.Speech.OutputDevice, devices.Output)
	if err != nil {
		slog.Error("no output device; MikkiLens will not be able to speak", "error", err)
	}
	bus := feedback.New(settings, locale, device)
	bus.Start()
	return bus
}

// -- run ----------------------------------------------------------------------

func commandRun(arguments []string, languageOverride string) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	echo := flags.Bool("echo", false,
		"say which command was recognised instead of acting on it")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}

	settings, locale, err := load(languageOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration problem: %v\n", err)
		return 1
	}
	if !engine.HasRunBefore() {
		fmt.Println("No config.toml yet -- run `mikkilensd setup` first, or use the desktop app.")
	}

	app := engine.New(settings, locale)
	// The YouTube consent screen has to land in her real browser. Nothing else
	// in the engine opens a window, so this is handed in rather than reached
	// for: a build with no desktop simply has nowhere to send it.
	app.OpenBrowser = httpapi.OpenBrowser
	if *echo {
		// Phase-one verification: repeat back whatever was heard, so the whole
		// microphone to recognition to dispatch path can be checked by ear.
		for _, id := range app.Commands().Order {
			if id == "help" || id == "reload_commands" {
				continue
			}
			command := id
			app.Router().Register(command, func(slots map[string]string) error {
				app.Bus().SayKey("listen.heard", feedback.Result,
					i18n.Args{"text": describeCommand(command, slots)})
				return nil
			})
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	api := httpapi.NewServer(app)
	if err := api.Start(); err != nil {
		// The API is optional; the voice is not.
		slog.Error("could not start the settings API", "error", err)
	}

	app.Start(ctx)
	slog.Info("ready",
		"hotkey", combinationOf(app), "wake_word", wakeModelOf(app), "api", api.URL())
	fmt.Printf("MikkiLens is running. Settings API on %s. Press Ctrl+C to stop.\n", api.URL())

	<-ctx.Done()
	fmt.Println("\nStopping...")

	api.Stop()
	app.Stop()
	return 0
}

func describeCommand(command string, slots map[string]string) string {
	if len(slots) == 0 {
		return command
	}
	parts := make([]string, 0, len(slots))
	for _, value := range slots {
		parts = append(parts, value)
	}
	return command + " " + strings.Join(parts, " ")
}

func combinationOf(app *engine.Engine) string {
	if watcher := app.Hotkey(); watcher != nil {
		return watcher.Combination()
	}
	return "disabled"
}

func wakeModelOf(app *engine.Engine) string {
	if detector := app.Wake(); detector != nil {
		return detector.ModelName()
	}
	return "disabled"
}

// -- setup and diagnosis ------------------------------------------------------

func commandSetup(languageOverride string) int {
	settings, locale, err := load(languageOverride)
	if err != nil {
		return 1
	}
	bus := speakingBus(settings, locale)
	defer func() {
		bus.WaitUntilIdle(30 * time.Second)
		bus.Stop()
	}()

	wizard := engine.NewWizard(settings, locale, bus, os.Stdin, os.Stdout)
	if _, err := wizard.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "setup failed: %v\n", err)
		return 1
	}
	return 0
}

func commandSelfTest(languageOverride string) int {
	settings, locale, err := load(languageOverride)
	if err != nil {
		return 1
	}
	bus := speakingBus(settings, locale)

	wizard := engine.NewWizard(settings, locale, bus, os.Stdin, os.Stdout)
	results := wizard.SelfTest(context.Background())

	bus.WaitUntilIdle(60 * time.Second)
	bus.Stop()

	failed := false
	for _, result := range results {
		mark := "OK  "
		if !result.OK {
			mark = "FAIL"
			failed = true
		}
		fmt.Printf("  %s %s: %s\n", mark, result.Item, result.Detail)
	}
	if failed {
		return 1
	}
	return 0
}

func commandDevices() int {
	if api := devices.HostAPI(); api != "" {
		fmt.Printf("Audio backend: %s\n", api)
	}
	for _, kind := range []devices.Kind{devices.Output, devices.Input} {
		found, err := devices.List(kind)
		if err != nil {
			fmt.Fprintf(os.Stderr, "could not list %s devices: %v\n", kind, err)
			return 1
		}
		fmt.Printf("\n%s devices:\n", strings.ToUpper(string(kind)[:1])+string(kind)[1:])
		for index, device := range found {
			fmt.Printf("  %2d. %s\n", index+1, device.Label())
		}
	}
	return 0
}

func commandListen(arguments []string, languageOverride string) int {
	flags := flag.NewFlagSet("listen", flag.ContinueOnError)
	times := flags.Int("n", 3, "how many times to listen")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}

	settings, locale, err := load(languageOverride)
	if err != nil {
		return 1
	}

	app := engine.New(settings, locale)
	defer app.Stop()

	ctx := context.Background()
	if err := app.Transcriber().Load(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "speech recognition is not available: %v\n", err)
		return 1
	}
	fmt.Printf("Speech recognition backend: %s\n", app.Transcriber().Describe())

	app.Bus().Start()
	app.StartMicrophone()
	if app.Microphone() == nil {
		fmt.Fprintln(os.Stderr, "no microphone")
		return 1
	}

	for round := 1; round <= *times; round++ {
		fmt.Printf("\n[%d/%d] Press Enter, then speak: ", round, *times)
		var discard string
		if _, err := fmt.Scanln(&discard); err != nil && discard == "" {
			// A bare Enter is exactly what we want; nothing to report.
			_ = err
		}

		app.BeginListening()
		time.Sleep(200 * time.Millisecond)
		for app.State().Get("listening") == true {
			time.Sleep(100 * time.Millisecond)
		}
		app.Bus().WaitUntilIdle(30 * time.Second)

		fmt.Printf("  heard:   %q\n", app.State().Get("last_transcript"))
		command := app.State().Get("last_command")
		if command == "" {
			command = "(none)"
		}
		fmt.Printf("  command: %v\n", command)
	}
	return 0
}

func commandSay(arguments []string, languageOverride string) int {
	if len(arguments) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mikkilensd say <text>")
		return 2
	}
	settings, locale, err := load(languageOverride)
	if err != nil {
		return 1
	}

	bus := speakingBus(settings, locale)
	bus.Say(strings.Join(arguments, " "), feedback.Result)
	bus.WaitUntilIdle(60 * time.Second)
	bus.Stop()
	return 0
}

func commandEarcons(languageOverride string) int {
	settings, locale, err := load(languageOverride)
	if err != nil {
		return 1
	}
	bus := speakingBus(settings, locale)
	defer bus.Stop()

	for _, name := range earcons.Names() {
		fmt.Printf("  %s\n", name)
		bus.Say(name, feedback.Result)
		bus.WaitUntilIdle(20 * time.Second)
		bus.Earcon(name)
		time.Sleep(time.Second)
	}
	return 0
}

// commandWarmup loads everything ahead of time, so the first real use is not a
// silent wait.
func commandWarmup(languageOverride string) int {
	settings, locale, err := load(languageOverride)
	if err != nil {
		return 1
	}
	ok := true

	fmt.Println("Loading speech recognition...")
	app := engine.New(settings, locale)
	if err := app.Transcriber().Load(context.Background()); err != nil {
		fmt.Printf("  failed: %v\n", err)
		ok = false
	} else {
		fmt.Printf("  ready: %s\n", app.Transcriber().Describe())
	}

	fmt.Println("Warming the voice...")
	warmed := tts.Prewarm(context.Background(), commonPhrases(locale), tts.Options{
		Voice: settings.Voice(locale.DefaultVoice()),
		Rate:  settings.Speech.Rate, Volume: settings.Speech.Volume,
	})
	fmt.Printf("  cached %d phrases\n", warmed)

	if !ok {
		return 1
	}
	return 0
}

// commonPhrases are the lines MikkiLens says most, which are the ones worth
// having ready before she needs them.
func commonPhrases(locale *i18n.Locale) []string {
	keys := []string{
		"app.ready", "app.starting", "app.shutdown",
		"obs.mic_muted", "obs.mic_unmuted", "obs.stream_started", "obs.stream_stopped",
		"obs.connected", "obs.disconnected",
		"status.live", "status.not_live", "status.mic_muted", "status.mic_live",
		"listen.no_speech", "chat.paused", "chat.resumed", "chat.up_to_date",
		"confirm.timeout", "confirm.cancelled", "confirm.not_understood",
	}
	phrases := make([]string, 0, len(keys))
	for _, key := range keys {
		phrases = append(phrases, locale.T(key))
	}
	return phrases
}

// -- OBS convenience ----------------------------------------------------------

// commandEnableOBS turns on the OBS WebSocket server and copies its password
// into config, so she never has to read it off a dialog.
//
// It backs the file up first and never changes a password that already exists.
// OBS reads this at startup, so it has to be restarted afterwards.
func commandEnableOBS(languageOverride string) int {
	obsConfig := filepath.Join(os.Getenv("APPDATA"),
		"obs-studio", "plugin_config", "obs-websocket", "config.json")

	settings, err := readOBSConfig(obsConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	backup := obsConfig + ".mikkilens-backup"
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		if raw, err := os.ReadFile(obsConfig); err == nil {
			if err := os.WriteFile(backup, raw, 0o644); err == nil {
				fmt.Printf("Backed up to %s\n", filepath.Base(backup))
			}
		}
	}

	wasEnabled, _ := settings["server_enabled"].(bool)
	settings["server_enabled"] = true
	if err := writeOBSConfig(obsConfig, settings); err != nil {
		fmt.Fprintf(os.Stderr, "could not write the OBS config: %v\n", err)
		return 1
	}

	current, locale, err := load(languageOverride)
	if err != nil {
		return 1
	}
	_ = locale

	if port, ok := settings["server_port"].(float64); ok {
		current.OBS.Port = int(port)
	}
	if password, ok := settings["server_password"].(string); ok {
		current.OBS.Password = password
	}
	if _, err := current.Save(""); err != nil {
		fmt.Fprintf(os.Stderr, "could not save the configuration: %v\n", err)
		return 1
	}

	fmt.Printf("OBS WebSocket enabled on port %d\n", current.OBS.Port)
	if !wasEnabled {
		fmt.Println("Restart OBS for the change to take effect.")
	}
	return 0
}
