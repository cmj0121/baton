// Package agentplugin is baton's public Claude Code plugin: the Agent Skills an
// agent running outside baton installs to learn how to drive it.
//
// It lives at the top of the repository, not under internal/skills where the
// rest of baton's skills are, for two reasons that pin it here. The repo-root
// .claude-plugin/marketplace.json serves this directory as the plugin's source,
// so `/plugin install baton@cmj0121-baton` copies exactly this tree, and a path
// under internal/ would publish an implementation detail as a public address.
// And go:embed cannot reach up out of its own package directory, so the one copy
// both the marketplace and the binary read has to be embedded from beside it;
// internal/skills reads it through FS rather than keeping a second copy that
// could drift from what the marketplace hands out.
package agentplugin

import "embed"

// FS is skills/<name>/SKILL.md for every public skill, plus the plugin's own
// .claude-plugin/plugin.json. The manifest is named file by file because a
// directory pattern skips dot-prefixed paths, and it is embedded at all so the
// version test can hold it to the binary's version.
//
//go:embed skills .claude-plugin/plugin.json
var FS embed.FS
