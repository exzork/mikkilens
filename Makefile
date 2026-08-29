# MikkiLens build.
#
# The two halves build independently on purpose: the engine is what actually
# controls her stream, and it has to be buildable and runnable without Node
# anywhere near it.

GO      ?= go
NPM     ?= npm
DIST    ?= dist
ENGINE  := $(DIST)/mikkilensd.exe

.PHONY: all engine desktop install test test-go test-desktop lint fmt run setup devices selftest clean

all: engine desktop

## engine: build the voice engine
engine:
	$(GO) build -o $(ENGINE) ./apps/daemon

## desktop: build the settings app
desktop:
	$(NPM) run build:desktop

## install: fetch every dependency
install:
	$(GO) mod download
	$(NPM) install

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

clean:
	rm -rf $(DIST) apps/desktop/out
