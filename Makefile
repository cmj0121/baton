SUBDIR :=

.PHONY: all clean lint test test-race cover ci run build install uninstall upgrade help $(SUBDIR)

# strip the symbol table (-s) and DWARF debug info (-w), and trim absolute paths,
# to keep the release binary small and reproducible.
LDFLAGS := -s -w

# system install prefix; baton also lands in the Go bin dir via `go install`.
PREFIX ?= /usr/local
# elevate only when the system bin dir is not writable by the current user.
SUDO := $(shell [ -w $(PREFIX)/bin ] 2>/dev/null || echo sudo)

all: $(SUBDIR) 		# default action
	@[ -f .git/hooks/pre-commit ] || pre-commit install --install-hooks
	@git config commit.template .git-commit-template

clean: $(SUBDIR)	# clean-up environment
	@find . -name '*.sw[po]' -delete

lint:				# run the go linters
	go vet ./...
	golangci-lint run

test:				# run test
	go test ./...

test-race:			# run test with the race detector
	go test -race ./...

cover:				# run race+coverage and gate each package at 80%
	./scripts/coverage-gate.sh 80

ci: build lint cover	# local mirror of the CI pipeline (build -> lint -> cover)

run:				# run in the local environment
	go run ./cmd/baton

build:				# build the binary/library
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/baton ./cmd/baton

install: build		# install baton to the Go bin dir and $(PREFIX)/bin
	go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/baton
	$(SUDO) install -m 0755 bin/baton $(PREFIX)/bin/baton

uninstall:			# remove baton from the Go bin dir and $(PREFIX)/bin
	rm -f $(shell go env GOPATH)/bin/baton
	$(SUDO) rm -f $(PREFIX)/bin/baton

upgrade:			# upgrade all the necessary packages
	pre-commit autoupdate

help:				# show this message
	@printf "Usage: make [OPTION]\n"
	@printf "\n"
	@perl -nle 'print $$& if m{^[\w-]+:.*?#.*$$}' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?#"} {printf "    %-18s %s\n", $$1, $$2}'

$(SUBDIR):
	$(MAKE) -C $@ $(MAKECMDGOALS)
