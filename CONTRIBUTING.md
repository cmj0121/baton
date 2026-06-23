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
