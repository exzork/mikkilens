# MikkiLens build.
#
# The two halves build independently on purpose: the engine is what actually
# controls her stream, and it has to be buildable and runnable without Node
# anywhere near it.

GO      ?= go
NPM     ?= npm
DIST    ?= dist
ENGINE  := $(DIST)/mikkilensd.exe

# The C debug sections must be stripped from the link.
#
# cgo (needed by the wake word, for ONNX Runtime) links with the system C
# toolchain, and an old MinGW places its DWARF sections at a virtual address
# outside the image. Windows then refuses the executable with "not a valid
# application for this OS platform", which points nowhere near the cause.
# --strip-debug drops those sections and keeps Go's own symbols, so panic
# traces stay readable.
LDFLAGS := -ldflags "-extldflags=-Wl,--strip-debug"

# The wake word is trained in Python, and only in Python. Nothing in apps/ or
# packages/ imports any of it; what crosses back over is one 850 KB .onnx file,
# already committed. uv builds the environment on demand rather than there being
# one to keep in sync -- this runs perhaps twice a year.
WAKEWORD := uv run --python 3.11 --with-requirements $(CURDIR)/tools/wakeword/requirements.txt python

.PHONY: all engine desktop app install stt wake dev test test-go test-desktop lint fmt run setup devices selftest wakeword clean

all: engine desktop

## engine: build the voice engine
#
# Plain `go build`, so this carries no built-in YouTube sign-in and needs
# data/client_secret.json to connect. That is deliberate: the engine stays
# buildable with no Node anywhere near it, and the credential belongs to the
# release, which goes through `npm run release` and scripts/build-daemon.mjs.
engine:
	$(GO) build $(LDFLAGS) -o $(ENGINE) ./apps/daemon

## desktop: build the settings app
desktop:
	$(NPM) run build:desktop

## app: the one-click executable, engine and window in one file
app: engine wake
	$(NPM) run package

## install: fetch every dependency
install:
	$(GO) mod download
	$(NPM) install

## stt: fetch the GPU whisper.cpp build and the speech model
stt:
	node scripts/fetch-whisper.mjs $(ARGS)

## wake: fetch the ONNX runtime and shared models the installer carries
#
# Unlike the speech models, these are small and go inside the installer, so a
# fresh machine has a working wake word before it has ever been online.
wake:
	node scripts/fetch-wake.mjs $(ARGS)

## dev: watch both halves -- air rebuilds the engine, the window reloads itself
dev:
	$(NPM) run dev

## test: everything
test: test-go test-desktop

test-go:
	MIKKILENS_SILENT=1 $(GO) test ./...

test-desktop:
	$(NPM) run test:desktop

## lint: formatting and vet
lint:
	@gofmt -l apps packages | tee /dev/stderr | (! read)
	$(GO) vet ./...

fmt:
	gofmt -w apps packages

## run: start the engine in this terminal
run: engine
	./$(ENGINE) run

## setup: the spoken first-run setup
setup: engine
	./$(ENGINE) setup

## devices: list the audio devices
devices: engine
	./$(ENGINE) devices

## selftest: check every part and read the result aloud
selftest: engine
	./$(ENGINE) selftest

## wakeword: retrain the wake word -- about two hours, most of it downloading
#
# You do not need this to build MikkiLens. The trained model is committed and
# ships inside the executable; this rebuilds it, and is what you run to change
# the wake word to something else. tools/wakeword/README.md says how.
wakeword:
	cd tools/wakeword && $(WAKEWORD) fetch.py
	cd tools/wakeword && $(WAKEWORD) speak.py
	cd tools/wakeword && $(WAKEWORD) alignment_test.py
	cd tools/wakeword && $(WAKEWORD) features.py
	cd tools/wakeword && $(WAKEWORD) train.py --install
	cd tools/wakeword && $(WAKEWORD) verify.py

clean:
	rm -rf $(DIST) apps/desktop/out
	rm -f apps/desktop/*.tsbuildinfo
