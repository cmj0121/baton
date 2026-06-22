-- token-footer.lua — show the agents' used tokens in the cockpit footer.
--
-- Copy to $HOME/.baton/plug-in.lua (or point --plugin / BATON_PLUGIN at it). It
-- watches panel output for a token count printed by your agent CLI, keeps a running
-- per-panel total, and surfaces it as a persistent footer segment via baton.footer.
--
-- panel.output is opt-in and high-volume: registering this handler is what turns the
-- output stream on, and the daemon only delivers it because of that. We keep the work
-- tiny (a pattern match per chunk) and only repaint when the total actually moves.
--
-- Agents print token usage in different shapes; adjust TOKEN_PATTERN to match yours.
-- The default matches a run of digits (optionally comma-grouped) before the word
-- "tokens", e.g. "12,345 tokens" or "1234 tokens".

local TOKEN_PATTERN = "(%d[%d,]*)%s*tokens"

local used = {} -- panel id -> last token count seen on that panel

-- plain strips CSI escape sequences so the match reads the text, not the colour codes
-- an agent's TUI wraps it in.
local function plain(s)
  return (s:gsub("\27%[[%d;?]*[ -/]*[@-~]", ""))
end

-- redraw recomputes the fleet-wide total and updates the footer only when it changes,
-- so a chatty agent does not repaint every chunk.
local last = -1
local function redraw()
  local total = 0
  for _, n in pairs(used) do
    total = total + n
  end
  if total == last then
    return
  end
  last = total
  if total > 0 then
    baton.footer(string.format("⊙ %d tok", total))
  else
    baton.footer("") -- nothing tracked → clear the segment
  end
end

baton.on("panel.output", function(p)
  local latest
  for m in plain(p.data or ""):gmatch(TOKEN_PATTERN) do
    latest = tonumber((m:gsub(",", ""))) -- keep the last (most recent) count in the chunk
  end
  if latest then
    used[p.id] = latest
    redraw()
  end
end)

-- Drop a panel's tally when it exits or is closed, so the total tracks the live fleet.
local function forget(p)
  if used[p.id] ~= nil then
    used[p.id] = nil
    redraw()
  end
end
baton.on("panel.exit", forget)
baton.on("panel.close", forget)
