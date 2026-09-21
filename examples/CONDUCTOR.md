<!-- markdownlint-disable MD025 -- this file is standing orders, not a page with one title. -->

# Mission

Keep a reviewer agent running on each open PR worktree. When one finishes, summarise its findings into a shell panel
named "report" and pause for me.

# How to create

`baton ctl list` first. Spawn workers with `baton ctl spawn --worktree` so each has its own checkout, or enqueue with
`--command` so the scheduler provisions one when none is free. Then `baton ctl group` them under a work item.

# How to give work

`baton ctl dispatch` a brief to a live worker, `baton ctl dispatch-group` to the work item, or `baton ctl queue add` for
the scheduler. Never `send` an objective — that types keystrokes into a running prompt.

# When to raise a hand

`baton ctl attention --why "..."` on _this_ panel when you are stuck and need the operator. The inbox is theirs. Do not
read other panels' output or drain their attention.

# When a worker is done

Summarise, then `baton ctl close` that worker. Do not act on your own panel, do not drain the queue, do not sweep
worktrees, do not reset this workspace.
