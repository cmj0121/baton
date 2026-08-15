<!-- markdownlint-disable MD041 -- a PR body is a fragment, not a document; an H1 here just shouts. -->
<!--
Thanks for the patch. Keeping this short is fine — the why matters more than the what,
since the diff already says what.
-->

## What and why

<!-- What changes, and the problem it solves. Link an issue if there is one. -->

## How it was verified

<!-- `make ci`, a manual run in the cockpit, a new test — whatever you actually did. -->

## Checklist

- [ ] `make ci` is green (build, lint, race + per-package 80% coverage).
- [ ] New behaviour arrives with tests that keep its package above the coverage floor.
- [ ] Docs updated if a key, config field, or socket message changed.
- [ ] One commit per purpose, [Conventional Commits](https://www.conventionalcommits.org/) subject, body indented 4 spaces.
