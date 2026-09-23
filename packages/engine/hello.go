package engine

import (
	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/core/intent"
)

// Introducing herself, which is the one command here that is not for her.
//
// Everything else MikkiLens does is addressed to the person running the
// stream: what the time is, whether the microphone is live, what chat has been
// saying. This is addressed to the people watching, and she is the one who
// asks for it -- somebody new arrives, asks what the voice is, and the answer
// is quicker said by MikkiLens than explained by her.
//
// It is deliberately a fixed sentence rather than something generated. It is
// said to an audience, so it should be the same every time and known in
// advance rather than whatever a model decided this once; it costs nothing and
// cannot fail; and it is one line of a locale file, which is where every other
// thing MikkiLens says already lives and where she can reword it without
// touching any of this.

func helloHandlers(e *Engine) map[string]intent.Handler {
	return map[string]intent.Handler{
		"say_hello": e.sayHello,
	}
}

// sayHello reads the introduction out.
//
// Spoken at Result, the same tier as an answer she asked for, so it goes
// ahead of the chat backlog rather than queueing behind twenty messages --
// an introduction that arrives a minute after someone asked for it is worse
// than none.
func (e *Engine) sayHello(map[string]string) error {
	e.bus.SayKey("hello.intro", feedback.Result)
	return nil
}
