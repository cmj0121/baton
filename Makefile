SUBDIR :=

.PHONY: all clean lint test run build upgrade help $(SUBDIR)

# strip the symbol table (-s) and DWARF debug info (-w), and trim absolute paths,
# to keep the release binary small and reproducible.
LDFLAGS := -s -w

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

run:				# run in the local environment
	go run ./cmd/baton

build:				# build the binary/library
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/baton ./cmd/baton

upgrade:			# upgrade all the necessary packages
	pre-commit autoupdate

help:				# show this message
	@printf "Usage: make [OPTION]\n"
	@printf "\n"
	@perl -nle 'print $$& if m{^[\w-]+:.*?#.*$$}' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?#"} {printf "    %-18s %s\n", $$1, $$2}'

$(SUBDIR):
	$(MAKE) -C $@ $(MAKECMDGOALS)
