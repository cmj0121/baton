SUBDIR :=

.PHONY: all clean lint test test-race cover stale-comments ci run build install uninstall upgrade help $(SUBDIR)

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

# Deliberately not a dependency of `ci`, and deliberately not a pre-commit
# hook -- but not for the reason the line here used to give.
#
# Replayed over the last 400 commits, the sweep fires on 25 of them and ten of
# those are real: keychainRun and rowAt name functions that never existed,
# TestLeftWalksOutOfTheTree and TestFoldSimilarPrefDefaultsOn name tests that do
# not, and three of the ten are still stale on main today. "Every name it has
# raised was prose" was itself a comment that outlived the code it described.
#
# The other fifteen are prose about names outside this module -- a git config
# key, two darwin C calls, a vendor, an ssh filename. No symbol table can reach
# those, so the sweep cannot tell them from a rename, and one wrong block every
# twenty-odd commits still teaches people to add --no-verify. Worth running and
# worth reading before review; not yet worth blocking on.
#
# Its own tests are ordinary Go tests, so `make ci` now checks the checker even
# though it does not run it.
stale-comments:		# find names that survive only in comments a branch adds
	go run ./cmd/stalecomment

ci: build crossbuild lint cover	# local mirror of the CI pipeline (build -> cross -> lint -> cover)

crossbuild:			# GitHub CI builds on Linux; most of us do not
	@GOOS=linux GOARCH=amd64 go build ./... || \
		{ echo ">> the Linux build is broken. syscall differs by OS - see internal/control/session_unix.go"; exit 1; }

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
