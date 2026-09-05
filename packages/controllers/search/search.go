// Package search looks things up on the web, so MikkiLens can answer a
// question it does not already know.
//
// The model behind [model] has no live access -- it says so when asked, and
// the endpoint accepts a web_search option and then ignores it. So the
// searching is done here and the results are handed to the model as fact,
// which is the same shape as every other command: MikkiLens finds out, the
// model turns it into a sentence worth hearing.
//
// DuckDuckGo's HTML endpoint, because it needs no key and no account. That
// matters more than it sounds: an API key is a thing to obtain, store and have
// expire, and a search she cannot use until she has been through a signup page
// is a search she does not have. The trade is that this is scraped rather than
// promised, so it is written to fail quietly and say so rather than to be
// clever.
package search

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Result is one thing found.
type Result struct {
	Title   string
	Snippet string
}

// Error is a lookup that did not work, phrased for reading aloud.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

// Limit is how many results are worth carrying.
//
// The model reads these, not her, and a snippet apiece is enough to answer
// from. More would mostly add ways to be distracted from the first one, which
// is usually the answer.
const Limit = 5

// timeout is the whole lookup. She is standing there waiting for it, and past
// this she is better served by "I could not look that up" than by more
// silence.
const timeout = 12 * time.Second

var client = &http.Client{Timeout: timeout}

var (
	titlePattern   = regexp.MustCompile(`(?s)class="result__a"[^>]*>(.*?)</a>`)
	snippetPattern = regexp.MustCompile(`(?s)class="result__snippet"[^>]*>(.*?)</a>`)
	tagPattern     = regexp.MustCompile(`<[^>]+>`)
	spacePattern   = regexp.MustCompile(`\s+`)
)

// Web searches for a query and returns what came back.
//
// POST rather than GET, which is not a style choice: the GET form answers with
// a challenge page and no results at all, and the difference is silent -- a
// page that parses fine and simply contains nothing.
func Web(ctx context.Context, query string) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, &Error{Reason: "there was nothing to search for"}
	}

	timed, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	form := url.Values{"q": {query}}
	request, err := http.NewRequestWithContext(timed, http.MethodPost,
		"https://html.duckduckgo.com/html/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, &Error{Reason: err.Error()}
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// A browser's user agent, because the endpoint answers a bare one with a
	// challenge page. Not a disguise: this is the same public page a person
	// gets, asked for once, on her behalf.
	request.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	response, err := client.Do(request)
	if err != nil {
		return nil, &Error{Reason: "could not reach the search: " + err.Error()}
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, &Error{Reason: fmt.Sprintf("the search answered %s", response.Status)}
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, &Error{Reason: err.Error()}
	}
	return parse(string(body)), nil
}

// parse pulls the titles and snippets out of the results page.
//
// Positional: the two lists come back in the same order, and a page with more
// titles than snippets is matched up as far as it goes rather than abandoned.
// This is somebody else's markup and it will change one day; when it does,
// this returns nothing rather than nonsense, and nothing is a state the caller
// already handles.
func parse(page string) []Result {
	titles := titlePattern.FindAllStringSubmatch(page, -1)
	snippets := snippetPattern.FindAllStringSubmatch(page, -1)

	results := make([]Result, 0, Limit)
	for index, title := range titles {
		if len(results) == Limit {
			break
		}
		text := clean(title[1])
		if text == "" {
			continue
		}
		snippet := ""
		if index < len(snippets) {
			snippet = clean(snippets[index][1])
		}
		results = append(results, Result{Title: text, Snippet: snippet})
	}
	return results
}

func clean(raw string) string {
	return strings.TrimSpace(spacePattern.ReplaceAllString(
		html.UnescapeString(tagPattern.ReplaceAllString(raw, " ")), " "))
}

// Readable turns results into the paragraph the model is given.
//
// Numbered, because the model is told to prefer the first thing that answers
// and numbering makes "the first" mean something. Empty when nothing was
// found, which the caller reads as "say so" rather than as an error.
func Readable(results []Result) string {
	if len(results) == 0 {
		return ""
	}
	var builder strings.Builder
	for index, result := range results {
		fmt.Fprintf(&builder, "%d. %s", index+1, result.Title)
		if result.Snippet != "" {
			fmt.Fprintf(&builder, " -- %s", result.Snippet)
		}
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}
