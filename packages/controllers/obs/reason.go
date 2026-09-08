package obs

import "strings"

// Reasons a connection failed, as codes rather than sentences: the words are
// chosen where they can be translated, and the engine and the settings page
// want to say different things about the same failure.
const (
	// ReasonAuth is OBS refusing the password. obs-websocket closes the socket
	// with 4009 and says nothing further.
	ReasonAuth = "auth"

	// ReasonUnreachable is nothing listening: OBS is closed, the server is
	// switched off, or the port is not the one it is on.
	ReasonUnreachable = "unreachable"

	// ReasonUnknown is everything else, where the raw text is all there is.
	ReasonUnknown = ""
)

// classify names what went wrong, for the two callers that treat these
// differently.
//
// The distinction that matters is whether waiting will fix it. OBS not being
// open yet is the ordinary state of things at startup and resolves itself the
// moment she opens it, so it is retried quietly. A rejected password never
// resolves itself: the reconnect loop will run until she gives up, and nothing
// about it is her fault or visible to her, so that one gets said out loud.
func classify(err error) string {
	if err == nil {
		return ReasonUnknown
	}
	text := strings.ToLower(err.Error())
	switch {
	// goobs surfaces the close code and OBS's own wording. Either half is
	// enough on its own, which matters because the library has reworded this
	// before and only the number comes from the protocol.
	case strings.Contains(text, "4009"),
		strings.Contains(text, "authentication failed"):
		return ReasonAuth
	case strings.Contains(text, "connection refused"),
		strings.Contains(text, "no connection could be made"),
		strings.Contains(text, "actively refused"),
		strings.Contains(text, "connectex"),
		strings.Contains(text, "no such host"),
		strings.Contains(text, "i/o timeout"):
		return ReasonUnreachable
	}
	return ReasonUnknown
}
