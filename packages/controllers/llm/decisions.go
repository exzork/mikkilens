package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Asking a question that has a fixed set of answers.
//
// The rest of this package sends prose and gets prose back. That is the right
// shape for a summary or an answer read aloud, and the wrong shape for "which
// of these twenty commands did she mean", which is the question actually being
// asked every time the written phrases miss. Asked as text, the answer has to
// be coaxed into JSON, stripped of its code fence, and checked against the
// list it was supposed to choose from -- and after all that it is one name
// with nothing to say about how close the second place was.
//
// A decision model answers that question directly. The options go up as a
// record, and what comes back is a probability for each one. Nothing has to be
// parsed, nothing can come back that was not offered, and the confidence is a
// number rather than a tone of voice.
//
// This speaks OpenRouter's decisions protocol, which is not the OpenAI one:
// different path, different body, different answer. That is the whole reason
// it is a separate endpoint in config rather than a flag on [model] -- the one
// in [model] is still free to be a local server that has never heard of any of
// this.

// decideTimeout is the fallback when none is configured.
//
// The model answers in well under a second. Past a few seconds something is
// wrong with the network rather than slow, and the text matcher is a better
// use of the time that is left.
const decideTimeout = 5 * time.Second

// NoChoice is the key offered for "none of these".
//
// Refusing has to be something it can pick, not something inferred from low
// scores. A model asked to choose between commands with no way to decline will
// distribute its uncertainty across them and hand back the least bad one,
// which is exactly the confident wrong guess these commands must not make.
const NoChoice = "__none__"

// Choice is one answered choice question.
type Choice struct {
	// Key is the winning option, or NoChoice when it declined.
	Key string
	// Confidence is how sure it is, between 0 and 1.
	Confidence float64
	// Probabilities is the full distribution, kept for logging: when a command
	// fails the threshold it is the runner-up that says whether the phrases
	// need a new wording or the question was genuinely ambiguous.
	Probabilities map[string]float64

	// Model is the version that actually answered, which is worth having
	// because the configured name is usually a floating alias. When matching
	// gets worse overnight without anything here changing, this is the only
	// record of what changed underneath.
	Model string
}

// Chose reports whether a real option won at or above the given confidence.
func (c Choice) Chose(minimum float64) bool {
	return c.Key != "" && c.Key != NoChoice && c.Confidence >= minimum
}

// DecisionsEndpoint is the decision provider, when one is configured.
//
// Returns an unconfigured endpoint when [decisions] is off or empty, which is
// what keeps every caller's fallback path the normal one rather than a special
// case.
func (c *Controller) DecisionsEndpoint() Endpoint {
	if !c.settings.Decisions.Configured() {
		return Endpoint{}
	}
	base, model, key := c.settings.DecisionsEndpoint()
	timeout := time.Duration(c.settings.Decisions.TimeoutS * float64(time.Second))
	if timeout <= 0 {
		timeout = decideTimeout
	}
	return Endpoint{BaseURL: base, Model: model, APIKey: key, Timeout: timeout}
}

// MinConfidence is how sure a decision has to be before it is acted on.
func (c *Controller) MinConfidence() float64 {
	return c.settings.Decisions.MinConfidence
}

// Choose asks which of a fixed set of options fits the state.
//
// criteria maps each option's key to a description of when it applies. A
// NoChoice option is added automatically, so callers never have to remember to
// leave the model a way out.
func (c *Controller) Choose(
	ctx context.Context, state, instructions string, criteria map[string]string, endpoint Endpoint,
) (Choice, error) {
	if len(criteria) == 0 {
		return Choice{}, &Error{Reason: "no options to choose from"}
	}

	options := make(map[string]string, len(criteria)+1)
	for key, description := range criteria {
		options[key] = description
	}
	options[NoChoice] = "None of the other options is clearly what was meant. " +
		"Choosing this is correct and expected whenever there is real doubt."

	answer, model, err := c.decide(ctx, state, decisionQuestion{
		Type:         "choice",
		Instructions: instructions,
		Criteria:     options,
	}, endpoint)
	if err != nil {
		return Choice{}, err
	}
	return Choice{
		Key:           strings.TrimSpace(answer.Choice),
		Confidence:    answer.Confidence,
		Probabilities: answer.Probabilities,
		Model:         model,
	}, nil
}

// Likely asks a yes-or-no question and returns how likely yes is.
func (c *Controller) Likely(
	ctx context.Context, state, instructions string, endpoint Endpoint,
) (float64, error) {
	answer, _, err := c.decide(ctx, state, decisionQuestion{
		Type:         "noul",
		Instructions: instructions,
	}, endpoint)
	if err != nil {
		return 0, err
	}
	return answer.Noul, nil
}

// DecisionsSelfTestResult is what the settings page shows after pressing Test.
type DecisionsSelfTestResult struct {
	OK bool `json:"ok"`
	// Model is the version that answered, which is the fact worth showing:
	// the name she typed is usually an alias, and this is what it resolved to.
	Model      string  `json:"model,omitempty"`
	Choice     string  `json:"choice,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Error      string  `json:"error,omitempty"`
}

// DecisionsSelfTest checks the decision provider answers, and answers sensibly.
//
// Reaching it is not enough to report success. A key that works against an
// endpoint that is not actually a decision model, or a model id that resolves
// to something else, both answer without complaining -- so this asks a
// question with one obvious answer and checks it came back. Failing here on
// the settings page is worth a great deal more than failing mid-stream.
func (c *Controller) DecisionsSelfTest(ctx context.Context) DecisionsSelfTestResult {
	endpoint := c.DecisionsEndpoint()
	if !endpoint.Configured() {
		return DecisionsSelfTestResult{Error: "no decision endpoint is configured"}
	}

	const expected = "mute_mic"
	choice, err := c.Choose(ctx,
		decisionState("matikan mikrofon"),
		decisionInstructions,
		map[string]string{
			expected:  "She said something like: matikan mikrofon; matikan mic",
			"go_live": "She said something like: mulai siaran; mulai streaming",
		}, endpoint)
	if err != nil {
		return DecisionsSelfTestResult{Error: err.Error()}
	}
	if choice.Key != expected {
		return DecisionsSelfTestResult{
			Model: choice.Model, Choice: choice.Key, Confidence: choice.Confidence,
			Error: "it answered, but not sensibly: a plain command came back as " +
				choice.Key + " rather than " + expected,
		}
	}
	return DecisionsSelfTestResult{
		OK: true, Model: choice.Model,
		Choice: choice.Key, Confidence: choice.Confidence,
	}
}

// -- wire ------------------------------------------------------------------

// decisionQuestion is one question in the shape the provider expects.
//
// Criteria is a record for a choice and absent for a yes-or-no, which is why
// it is omitempty rather than always sent: the provider validates the two
// shapes separately and rejects the wrong one outright.
type decisionQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

type decisionRequest struct {
	Model     string                      `json:"model"`
	State     string                      `json:"state"`
	Questions map[string]decisionQuestion `json:"questions"`
}

// decisionAnswer is every answer shape flattened into one.
//
// The provider tags each answer with its type and fills only the fields that
// type uses, so one struct reads all of them and the caller looks at the field
// it asked for.
type decisionAnswer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

type decisionResponse struct {
	Model   string                    `json:"model"`
	Answers map[string]decisionAnswer `json:"answers"`
	Usage   struct {
		InputTokens  int     `json:"input_tokens"`
		OutputTokens int     `json:"output_tokens"`
		Cost         float64 `json:"cost"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// questionKey is the single question's name. The protocol allows many per
// call; nothing here needs more than one yet, and a fixed name keeps reading
// the answer back a lookup rather than a search.
const questionKey = "answer"

// decide runs one question against the decision provider.
//
// Returns the version that answered alongside the answer, because the model
// asked for is usually an alias and the one that replied is the fact worth
// keeping.
func (c *Controller) decide(
	ctx context.Context, state string, question decisionQuestion, endpoint Endpoint,
) (decisionAnswer, string, error) {
	if !endpoint.Configured() {
		return decisionAnswer{}, "", &Error{Reason: "no decision endpoint is configured"}
	}
	timeout := endpoint.Timeout
	if timeout <= 0 {
		timeout = decideTimeout
	}
	timed, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(decisionRequest{
		Model:     endpoint.Model,
		State:     state,
		Questions: map[string]decisionQuestion{questionKey: question},
	})
	if err != nil {
		return decisionAnswer{}, "", &Error{Reason: err.Error()}
	}

	url := strings.TrimRight(endpoint.BaseURL, "/") + "/decisions"
	request, err := http.NewRequestWithContext(timed, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return decisionAnswer{}, "", &Error{Reason: err.Error()}
	}
	request.Header.Set("Content-Type", "application/json")
	if endpoint.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+endpoint.APIKey)
	}

	response, err := c.client.Do(request)
	if err != nil {
		return decisionAnswer{}, "", &Error{Reason: Readable(err.Error())}
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return decisionAnswer{}, "", &Error{Reason: Readable(err.Error())}
	}

	var parsed decisionResponse
	_ = json.Unmarshal(payload, &parsed)

	if response.StatusCode != http.StatusOK {
		detail := response.Status
		if parsed.Error != nil && parsed.Error.Message != "" {
			detail = fmt.Sprintf("%d %s", response.StatusCode, parsed.Error.Message)
		}
		return decisionAnswer{}, "", &Error{Reason: Readable(detail)}
	}

	answer, found := parsed.Answers[questionKey]
	if !found {
		return decisionAnswer{}, "", &Error{Reason: "the decision model returned nothing"}
	}
	return answer, parsed.Model, nil
}
