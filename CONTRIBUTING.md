# Contributing to Baton

Thanks for your interest in improving Baton! This guide covers how to build,
test, and submit changes.

## Prerequisites

- [Go](https://go.dev) 1.26 or newer (the version is pinned in `go.mod`).
- [`pre-commit`](https://pre-commit.com) for the local hooks (optional but
  recommended).

## Getting started

```sh
git clone https://github.com/cmj0121/baton.git
cd baton
make build      # build the binary
make run        # build and run locally
```

## Development workflow

Baton ships a `Makefile` that mirrors the CI pipeline, so you can run the exact
same checks locally before pushing:

| Command          | What it does                                          |
| ---------------- | ----------------------------------------------------- |
| `make build`     | Build the binary.                                     |
| `make lint`      | Run the Go linters (`golangci-lint`).                 |
| `make test`      | Run the test suite.                                   |
| `make test-race` | Run the suite with the race detector.                 |
| `make cover`     | Run race + coverage and gate **each package at 80%**. |
| `make ci`        | Local mirror of CI: `build` → `lint` → `cover`.       |

Run `make ci` before opening a pull request — if it passes locally, it passes
in CI.

## Comments that outlive the code

The commonest defect found in review here is not wrong code. It is a comment
describing a neighbouring piece of code that the same commit changed. Writing
the comment is not the failure; not re-reading it once the code beside it moved
is. Two habits, in order of how much they buy:

### `make stale-comments`

A rename leaves the old name sitting in the comment beside it, and the compiler
never looks at a comment. `make stale-comments` walks every comment line your
branch **adds** to a `.go` file, pulls out every code-shaped name, and checks it
against every name the code can reach. A name that survives only in prose is the
residue of an edit that moved on without it.

"Every name the code can reach" is `go/parser`'s answer, not a grep's: every
identifier the tree declares or uses, every name spelled inside one of its
string literals, the **exported surface of every package it imports**, and its
filenames. So `ExtraFiles` in a comment about `os/exec` resolves, and so does
`4GiB` beside a `"4GiB"` in a table.

It needs no list of what was renamed, which is the point — it cannot be told
the wrong answer by a list that is out of date. Run it before asking for review.
Its exit codes are `0` clean, `1` names found, and `2` **nothing was checked** —
the last being a failure, never a pass, because a sweep that examined nothing
looks exactly like one that examined everything and approved it. Pass a base
revision to sweep a different range: `make stale-comments` compares against the
nearest merge-base, and `go run ./cmd/stalecomment <rev>` against `<rev>`.

It is not part of `make ci` and not a pre-commit hook. Replayed over the last
400 commits it fires on 25 and ten of those are real — `keychainRun` and `rowAt`
name functions that never existed — but the other fifteen are prose about names
outside this module (`init.defaultBranch`, `proc_pidpath`, `known_hosts`), which
no symbol table can reach. Read its output; do not let it block you.

### Claims a grep cannot check

The sweep only finds stale **identifiers**. Two other ways a comment goes stale
have no name in them at all, so no tool will ever see them:

- a comment that **enumerates cases** and goes stale when a case is added;
- a comment that **states an arithmetic result** and goes stale when the
  function it describes changes.

The only form of either that survives the next edit is one the compiler or the
suite checks: a test that recomputes the figure from the function itself, or a
typed value replacing a closed enumeration written out in prose. When you catch
yourself writing a claim a reader would have to verify by hand, move it into an
assertion instead. That is a habit, not a tool, and `make stale-comments`
passing says nothing about it.

### Pre-commit hooks

Install the hooks once and they run automatically on every commit:

```sh
pre-commit install
```

They check YAML, fix end-of-file and trailing whitespace, lint Markdown, run
`prettier`, run `golangci-lint`, and scan for secrets with `gitleaks`.

## Coverage

CI enforces a per-package coverage floor of **80%** (packages with no
statements or no tests are skipped). New code should arrive with tests that
keep its package above the threshold. Coverage is also uploaded to Codecov.

## Commit conventions

- Use [Conventional Commits](https://www.conventionalcommits.org/) for the
  subject line, e.g. `feat(tui): add ...`, `fix(paths): ...`, `docs: ...`,
  `ci: ...`.
- Keep the subject concise; explain the _why_ in the body.
- Wrap the body and indent it 4 spaces.
- Make one commit per purpose — don't bundle unrelated changes.

## Pull requests

1. Create a feature branch off `main`.
2. Make your change with tests and docs as needed.
3. Run `make ci` and ensure it is green.
4. Open a pull request describing the change and its motivation.

## License

By contributing, you agree that your contributions will be licensed under the
[MIT License](LICENSE).
