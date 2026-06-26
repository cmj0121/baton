-- title-hook.lua — give every panel a custom title in the cockpit.
--
-- Copy to $HOME/.baton/plug-in.lua (or point --plugin / BATON_PLUGIN at it). The
-- panel.title hook computes the title a panel shows on the dashboard and in its
-- group tile. It is a filter: it receives the panel and returns the string to show.
--
-- The hook runs when a panel spawns and whenever its state changes — not on every
-- render — and the result is cached, so it costs nothing between changes. It always
-- receives the panel's BASE title ("<command> · <workdir>"), never a previous result
-- of its own, so you can rebuild the title from scratch each time without feedback.
--
-- Return a string to set the title, or nil to leave it unchanged. Removing the hook
-- (and reloading with C-t R) restores the built-in "<command> · <workdir>" titles.

-- A small state badge so you can read a panel's lifecycle at a glance in its title.
local BADGE = {
  spawning = "…",
  running = "▶",
  idle = "•",
  attention = "!",
  exited = "×",
}

baton.on("panel.title", function(p)
  local badge = BADGE[p.state] or "?"
  -- Lead with the work item when the panel belongs to one, so grouped tiles read
  -- "api ▶ claude · api" and lone panels just "▶ claude · scratch".
  if p.group ~= nil and p.group ~= "" then
    return string.format("%s %s %s", p.group, badge, p.title)
  end
  return string.format("%s %s", badge, p.title)
end)
