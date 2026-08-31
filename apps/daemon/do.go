package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/intent"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// `mikkilensd do <command>` is the binding for anything that can start a
// program but cannot send a keystroke.
//
// Every Stream Deck, of every brand, can run an executable -- it is the one
// action they all have, along with sending a key. Between this and the
// [[bindings]] keys, there is no deck, pedal or mouse that cannot drive
// MikkiLens.
//
// It talks to the engine that is already running rather than starting one.
// A second engine would fight the first for the microphone and the hotkey,
// and she would have two of everything answering her at once.

// doTimeout is how long to wait for the engine to accept the command. It is
// short on purpose: this runs from a button, and a button that hangs is worse
// than one that reports it could not be delivered.
const doTimeout = 5 * time.Second

func commandDo(arguments []string) int {
	flags := flag.NewFlagSet("do", flag.ContinueOnError)
	list := flags.Bool("list", false, "list the commands that can be run")
	url := flags.String("url", "", "the engine's API, if it is not the one in config.toml")
	noConfirm := flags.Bool("no-confirm", false,
		"run it without asking first, even if the command normally asks")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}

	if *list {
		return listCommands()
	}

	name := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if name == "" {
		fmt.Fprint(os.Stderr, "usage: mikkilensd do <command>\n"+
			"Run `mikkilensd do --list` to see the command names.\n")
		return 2
	}

	base := *url
	if base == "" {
		base = engineURL()
	}

	body, err := json.Marshal(map[string]any{"command": name, "confirm": !*noConfirm})
	if err != nil {
		fmt.Fprintf(os.Stderr, "mikkilensd: %v\n", err)
		return 1
	}

	client := &http.Client{Timeout: doTimeout}
	response, err := client.Post(base+"/api/command", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"mikkilensd: could not reach MikkiLens at %s.\n"+
				"Is it running? Start it with run.bat or the MikkiLens app.\n", base)
		return 1
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		fmt.Fprintf(os.Stderr, "mikkilensd: %s\n", strings.TrimSpace(string(message)))
		return 1
	}

	// Nothing is printed on success. What happened is said out loud by the
	// engine, which is the only report that reaches her; a line of text in a
	// window that a Stream Deck opened and closed again reaches nobody.
	return 0
}

// listCommands names every command a key can be bound to.
func listCommands() int {
	set, err := intent.SetFromFile(paths.CommandsFile(currentLanguage()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "mikkilensd: could not read the commands: %v\n", err)
		return 1
	}

	ids := make([]string, 0, len(set.Commands))
	for id := range set.Commands {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		if set.Commands[id].Confirm {
			fmt.Printf("  %-16s (asks first)\n", id)
		} else {
			fmt.Printf("  %s\n", id)
		}
	}
	return 0
}

// engineURL is where the running engine is listening, according to the same
// config file it read.
func engineURL() string {
	settings, err := config.Load("")
	if err != nil {
		settings = config.Default()
	}
	host := settings.UI.Host
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	port := settings.UI.Port
	if port == 0 {
		port = config.Default().UI.Port
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

func currentLanguage() string {
	settings, err := config.Load("")
	if err != nil || settings.Language.Output == "" {
		return config.Default().Language.Output
	}
	return settings.Language.Output
}
