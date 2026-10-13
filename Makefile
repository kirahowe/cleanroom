# Everything a reviewer needs is here. No target prompts for input.
#
#   make build   build cleanroom, the reader, the sources and the seed command
#   make test    run every test in the three modules
#   make inbox   start Mailpit with the demo inbox and leave it running

BIN      := bin
MAILPIT  := $(shell command -v mailpit 2>/dev/null || echo .tools/mailpit)
# Not Mailpit's defaults (8025 and 1025), so the demo inbox cannot collide
# with a Mailpit or a mail bridge that is already running.
HTTP     := 127.0.0.1:18025
SMTP     := 127.0.0.1:11025

.PHONY: build test inbox mailpit clean

build:
	go build -o $(BIN)/cleanroom ./cmd/cleanroom
	go build -o $(BIN)/claude-reader ./cmd/claude-reader
	go build -o $(BIN)/seed ./demo/seed
	cd sources/mail && go build -o ../../$(BIN)/mail-source .
	cd sources/calendar && go build -o ../../$(BIN)/calendar-source .

test:
	go vet ./... && go test ./...
	cd sources/mail && go vet ./... && go test ./...
	cd sources/calendar && go vet ./... && go test ./...

# Download Mailpit into .tools/ unless it is already installed.
mailpit:
	@test -x "$(MAILPIT)" || ./scripts/get-mailpit.sh

inbox: build mailpit
	@echo "Mailpit inbox: http://$(HTTP)  (Ctrl-C to stop)"
	@( sleep 1; $(BIN)/seed --api http://$(HTTP) --smtp $(SMTP) --reset ) &
	@$(MAILPIT) --listen $(HTTP) --smtp $(SMTP) --quiet

clean:
	rm -rf $(BIN) demo/.cleanroom
