// Package vision describes the screen through an OpenAI-compatible model.
//
// It captures the desktop across every monitor, downscales it, and asks the
// model her own question about it. The answer comes back in her language and
// is kept short, because it is going to be spoken rather than skimmed.
package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"strconv"
	"strings"
	"time"

	"github.com/kbinani/screenshot"
	"golang.org/x/image/draw"

	"github.com/exzork/mikkilens/packages/controllers/llm"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
)

const (
	defaultQuestion = "Describe what is on this screen."
	jpegQuality     = 78
)

// Error is a capture or description failure worth speaking aloud.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

// Controller captures the screen and asks a model about it.
type Controller struct {
	settings config.Config
	locale   *i18n.Locale
	llm      *llm.Controller
}

// New builds a controller for the current settings.
func New(settings config.Config, locale *i18n.Locale) *Controller {
	return &Controller{settings: settings, locale: locale, llm: llm.New(settings, locale)}
}

// Endpoint is the configured vision provider.
func (c *Controller) Endpoint() llm.Endpoint {
	vision := c.settings.Vision
	return llm.Endpoint{
		BaseURL: vision.Base,
		Model:   vision.Model,
		APIKey:  c.settings.VisionAPIKey(),
		Timeout: time.Duration(vision.TimeoutS * float64(time.Second)),
	}
}

// -- capture ------------------------------------------------------------------

// Capture grabs the screen as JPEG bytes, downscaled to the configured size.
func (c *Controller) Capture() ([]byte, error) {
	bounds, err := c.targetBounds()
	if err != nil {
		return nil, err
	}
	shot, err := screenshot.CaptureRect(bounds)
	if err != nil {
		return nil, &Error{Reason: "tidak bisa mengambil gambar layar: " + err.Error()}
	}
	return encodeJPEG(c.downscale(shot))
}

// targetBounds resolves the monitors setting to a rectangle.
func (c *Controller) targetBounds() (image.Rectangle, error) {
	count := screenshot.NumActiveDisplays()
	if count == 0 {
		return image.Rectangle{}, &Error{Reason: "tidak menemukan layar"}
	}

	selection := strings.ToLower(strings.TrimSpace(c.settings.Vision.Monitors))
	switch {
	case selection == "primary":
		return screenshot.GetDisplayBounds(0), nil
	case selection != "" && selection != "all":
		if index, err := strconv.Atoi(selection); err == nil {
			return screenshot.GetDisplayBounds(max(0, min(count-1, index-1))), nil
		}
	}

	// "all" is the union of every display, which is what she means by "the
	// screen" when OBS is on one monitor and the game is on another.
	union := screenshot.GetDisplayBounds(0)
	for index := 1; index < count; index++ {
		union = union.Union(screenshot.GetDisplayBounds(index))
	}
	return union, nil
}

// downscale shrinks the capture so the request is affordable and fast. A model
// gains nothing from four thousand pixels of desktop.
func (c *Controller) downscale(source image.Image) image.Image {
	maxEdge := max(256, c.settings.Vision.MaxEdge)
	bounds := source.Bounds()
	longest := max(bounds.Dx(), bounds.Dy())
	if longest <= maxEdge {
		return source
	}

	scale := float64(maxEdge) / float64(longest)
	width := max(1, int(float64(bounds.Dx())*scale))
	height := max(1, int(float64(bounds.Dy())*scale))

	resized := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(resized, resized.Bounds(), source, bounds, draw.Over, nil)
	return resized
}

func encodeJPEG(source image.Image) ([]byte, error) {
	buffer := &bytes.Buffer{}
	if err := jpeg.Encode(buffer, source, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, &Error{Reason: "tidak bisa menyimpan gambar layar: " + err.Error()}
	}
	return buffer.Bytes(), nil
}

// Info reports what the capture can see, for the settings page.
type Info struct {
	Monitors  int    `json:"monitors"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Selection string `json:"selection"`
}

// CaptureInfo describes the monitors without capturing anything.
func (c *Controller) CaptureInfo() Info {
	bounds, err := c.targetBounds()
	if err != nil {
		return Info{Selection: c.settings.Vision.Monitors}
	}
	return Info{
		Monitors:  screenshot.NumActiveDisplays(),
		Width:     bounds.Dx(),
		Height:    bounds.Dy(),
		Selection: c.settings.Vision.Monitors,
	}
}

// -- description --------------------------------------------------------------

// Describe answers a question about the screen, or describes it if she asked
// nothing in particular.
func (c *Controller) Describe(ctx context.Context, question string) (string, error) {
	captured, err := c.Capture()
	if err != nil {
		return "", err
	}
	return c.DescribeImage(ctx, question, captured)
}

// DescribeImage answers a question about an image already captured. The self
// test uses it to check the provider without grabbing her actual screen.
func (c *Controller) DescribeImage(ctx context.Context, question string, jpegBytes []byte) (string, error) {
	endpoint := c.Endpoint()
	if !endpoint.Configured() {
		return "", &Error{Reason: c.locale.T("vision.no_provider")}
	}

	asked := strings.TrimSpace(question)
	if asked == "" {
		asked = defaultQuestion
	}
	encoded := base64.StdEncoding.EncodeToString(jpegBytes)

	answer, err := c.llm.Complete(ctx, []llm.Message{
		{Role: "system", Content: "You describe a computer screen for a blind live " +
			"streamer. Lead with the single most important thing, then add detail. " +
			"Mention errors, dialogs and unread counts. If asked a specific question, " +
			"answer only that. " + c.llm.LanguageInstruction()},
		{Role: "user", Content: []any{
			llm.TextPart{Type: "text", Text: asked},
			llm.ImagePart{
				Type:     "image_url",
				ImageURL: llm.ImageURL{URL: "data:image/jpeg;base64," + encoded},
			},
		}},
	}, endpoint, 400)
	if err != nil {
		return "", &Error{Reason: err.Error()}
	}

	// Trim before testing: a whitespace-only reply is not an answer, and
	// speaking it would come out as unexplained silence.
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "", &Error{Reason: "model tidak menjawab"}
	}

	limit := c.settings.Vision.MaxAnswerChars
	runes := []rune(answer)
	if limit > 0 && len(runes) > limit {
		return strings.TrimRight(string(runes[:limit]), " ") + "…", nil
	}
	return answer, nil
}

// -- diagnostics --------------------------------------------------------------

// SelfTest sends a small generated image, so a misconfigured provider fails
// here, audibly, rather than the first time she asks about her screen.
func (c *Controller) SelfTest(ctx context.Context) llm.SelfTestResult {
	endpoint := c.Endpoint()
	if !endpoint.Configured() {
		return llm.SelfTestResult{Error: "model penglihatan belum diatur"}
	}

	sample, err := encodeJPEG(testCard())
	if err != nil {
		return llm.SelfTestResult{Model: endpoint.Model, Error: err.Error()}
	}
	answer, err := c.DescribeImage(ctx, "What shapes and colours are in this image?", sample)
	if err != nil {
		return llm.SelfTestResult{Model: endpoint.Model, Error: err.Error()}
	}
	return llm.SelfTestResult{OK: true, Model: endpoint.Model, Answer: clip(answer, 200)}
}

// testCard draws something a vision model can describe unambiguously: three
// solid blocks on white. Text would depend on the model reading it, which is a
// different capability than the one being tested.
func testCard() image.Image {
	const width, height = 320, 120
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	fill(canvas, canvas.Bounds(), color.RGBA{255, 255, 255, 255})

	blocks := []color.RGBA{
		{220, 40, 40, 255}, // red
		{40, 160, 60, 255}, // green
		{40, 80, 210, 255}, // blue
	}
	for index, shade := range blocks {
		left := 20 + index*100
		fill(canvas, image.Rect(left, 20, left+80, 100), shade)
	}
	return canvas
}

func fill(canvas *image.RGBA, area image.Rectangle, shade color.RGBA) {
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			canvas.SetRGBA(x, y, shade)
		}
	}
}

func clip(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
