# Baton — 外掛(Lua)

[English](PLUGIN.md) · **繁體中文**

> **狀態:已推出。** 四大支柱全數上線——fleet API、hook、指令,以及 Lua 設定——再加上
> client-fetches-config 這條傳輸線,所以一個外掛能重塑整個座艙,而不只是常駐程式。實作位於
> [`internal/plugin`](../internal/plugin);本文件就是它的契約。

一個 baton 外掛是伺服器在啟動時載入的單一 Lua 檔。透過一個物件——`baton`——它能讀取整隊、
驅動每一項核心動作、對生命週期事件做出反應,並加入自己的指令。外掛之於 baton,就像
`init.lua` 之於 Neovim:一份你自己擁有、能把工具重塑成你工作流的可信任程式碼。

架構早已為這個位置命名。在 [SPEC.md](./SPEC.zh-TW.md) 裡,**Lua runtime** 座落於常駐程式之內,
就在那道 socket 指令、Lua 呼叫與事件註冊全都會經過的唯一 **`baton.*` API** 閘門之後。本文件
就是要把那個方框填滿。

## 為什麼放在伺服器端

外掛跑在**常駐程式(daemon)**裡,而不是座艙裡。常駐程式是唯一的真實來源:它掌管整隊、
每一項核心動作,以及事件串流。一個載入在那裡的外掛:

- 一次影響**每一個前端**(今天是 TUI,以後是瀏覽器)——規則活在傳輸線之下,而不是在某個
  單一 client 裡;
- 能在座艙重啟、卸離、重新接上之後**存活**——它活得跟整隊一樣久;
- 觸及**任何 client 都看不到的事件**——當你卸離時,某個面板落入 `attention` 仍會觸發你的 hook。

代價是:一個外掛作用於**整隊**,而不是「座艙當下的選取」。選取、檢視與縮放都是 client 端的
狀態。一個外掛可以*提供*一個由座艙針對其選取執行的指令(由 client 傳入),但外掛本身思考的
是面板 id 與群組,永遠不是「我正在看的那個面板」。

## 這個檔案

|              |                                                                              |
| ------------ | ---------------------------------------------------------------------------- |
| **預設路徑** | `$HOME/.baton/plug-in.lua`                                                   |
| **覆寫**     | `--plugin FILE` 旗標,或 `BATON_PLUGIN=FILE`                                  |
| **時機**     | 在常駐程式啟動時載入一次,在 YAML 設定之後、整隊開始服務之前                  |
| **重載**     | 在 `C-t R` / `SIGHUP` 時重新全新執行,與設定相同——編輯後重載,不重啟、不掉面板 |
| **錯誤**     | 載入錯誤會被記錄且**非致命**——常駐程式會帶著 YAML 預設值繼續跑,絕不卡死      |
| **缺檔**     | 沒有檔案就是乾淨的無動作——外掛是可選加入的                                   |

一次重載會從頭重建整個 Lua 世界(一個全新的直譯器),所以你的 hook 與指令恰好就是檔案現在
所寫的樣子——不會有前一版遺留下來的過期註冊。

## `baton` 物件

一切都透過一個全域 table——`baton`——來取用。它由 Go 支撐:每一次呼叫都落在與 socket 指令
相同的那項核心動作上,所以外掛做不到前端做不到的任何事,也沒有任何東西能繞過那唯一的閘門。

### 驅動整隊

```lua
local id = baton.spawn{ kind = "agent", command = "claude", dir = "~/src/api", group = "api" }
baton.signal(id, "SIGINT")
baton.group({ id1, id2 }, "api")
baton.rename{ id = id, name = "claude·api" }
baton.pin(id)
baton.close(id)
```

| 呼叫                                                | 核心動作                         | 說明                                      |
| --------------------------------------------------- | -------------------------------- | ----------------------------------------- |
| `baton.spawn{kind=, command=, args=, dir=, group=}` | `panel.create` (+ `panel.group`) | 回傳新的面板 id                           |
| `baton.respawn(id)`                                 | `panel.respawn`                  | 重跑一個已結束的面板                      |
| `baton.close(id \| {ids})`                          | `panel.close`                    |                                           |
| `baton.purge()`                                     | `panel.purge`                    | 丟棄每一個已結束的面板                    |
| `baton.signal(id \| {ids}, name)`                   | `panel.signal`                   | 名稱或數字,例如 `"SIGTERM"` 或 `15`       |
| `baton.group({ids}, name)`                          | `panel.group`                    | 帶斜線的 `path` 名稱會巢狀(`backend/api`) |
| `baton.ungroup({ids} \| name)`                      | `panel.ungroup`                  | 解散一個群組會把它的子樹上提              |
| `baton.rename{id= \| group=, name=}`                | `panel.rename`                   | 把群組改名成一個 path 即可重新掛親        |
| `baton.move({ids}, index)`                          | `panel.move`                     | 重排整隊                                  |
| `baton.pin(id)` / `baton.unpin(id)`                 | `panel.pin` / `panel.unpin`      |                                           |
| `baton.group_show(name, n)`                         | `group.show`                     | 一個群組的即時磚數量                      |
| `baton.dispatch(id, prompt)`                        | `panel.dispatch`                 | 指派一份簡報並以一個單位送達              |
| `baton.dispatch_group(group, prompt)`               | `panel.dispatch-group`           | 把一份簡報散發給整個子樹;回傳數量         |
| `baton.enqueue(prompt, group)`                      | `task.enqueue`                   | 加入待辦佇列;回傳 task id                 |

`baton.spawn` 也接受一個 `prompt =`——一次呼叫就開一個 agent 並派出它的第一個任務。外掛發起的
派送會直接送到核心動作,並**繞過 `task.pre` 過濾器**(見下文),所以一個會排隊的 hook 永遠
不會再次進入自己。`baton.enqueue` 同樣如此,它的任務不會在任何地方被過濾:來源記在任務上,
所以這個繞過能撐過在待辦裡的等待——連同一次 daemon 重啟。

每一次寫入都回傳 `ok, err`(Lua 慣用法):在與 socket 回報相同的失敗情況下回傳
`nil, "the name \"api\" is already taken"`,所以外掛會處理一個被拒的動作,而不是崩潰。

### 讀取整隊

```lua
for _, p in ipairs(baton.panels()) do
  if p.state == "attention" then print(p.title, p.group) end
end
```

- `baton.panels()` → `{ id, kind, title, state, group, activity, pinned, favourite }` 的陣列
- `baton.panel(id)` → 一個面板 table,或 `nil`
- `baton.groups()` → `{ group, shown }` 的陣列

讀取是你呼叫當下那一刻的快照——與儀表板拿來繪製的是同一份檢視。

### 對事件做出反應 — `baton.on`

```lua
baton.on("panel.attention", function(p)
  baton.notify(string.format("%s needs you", p.title))
end)

baton.on("panel.exit", function(p)
  if p.exit_code ~= 0 then baton.log("warn", p.title .. " failed") end
end)
```

一個 handler 跑在常駐程式唯一的那個 Lua worker 上,**在每一道伺服器鎖之外**,所以它可以
自由地回頭呼叫 `baton.*`。Handler 是盡力而為的:一個緩慢或會拋錯的 handler 會被記錄並隔離,
絕不會拖住 Monitor;而一波湧入、速度超過 worker 的事件會由舊到新丟棄(就像遙測資料),
而不是阻塞整隊。

事件集合(衍生自 SPEC.md 中的生命週期):

| 事件              | 觸發時機                 | 酬載                                                           |
| ----------------- | ------------------------ | -------------------------------------------------------------- |
| `panel.spawn`     | 一個面板被建立           | 該面板                                                         |
| `panel.state`     | 任何生命週期轉換         | 面板 + `from`、`to`                                            |
| `panel.attention` | 一個面板進入 `attention` | 該面板                                                         |
| `panel.idle`      | 一個面板穩定至 `idle`    | 該面板                                                         |
| `panel.exit`      | 一支行程自行結束         | 面板 + `exit_code`                                             |
| `panel.close`     | 一個面板被退場           | `{ id }`                                                       |
| `group.change`    | 成員/改名/顯示數量變動   | 該群組                                                         |
| `task.change`     | 一個任務被記錄或改變狀態 | 該任務——`id`、`prompt`、`status`、`panel`、`group`、`attempts` |
| `server.reload`   | 設定/外掛被重載          | —                                                              |
| `panel.output`    | 有位元組抵達某個面板     | 面板 + `data`——**高流量、需選擇加入、預設關閉**                |
| `panel.title`     | 一個面板生成或改變狀態   | 該面板——**這是一個過濾器:回傳要顯示的標題字串**                |

`panel.attention` 與 `panel.exit` 是最主打的 hook:「當某個 agent 需要我時提醒我」、「當這一步
完成時啟動下一步」、「完成時發桌面通知」全都由它們衍生而來。

### 可程式化的標題 — `panel.title`

`panel.title` 是一個像 `task.pre` 的**過濾器 hook(filter hook)**:它回傳一個值供座艙使用。
這個函式收到一個面板,並回傳要顯示在儀表板卡片與該面板群組磚上的標題字串。它在面板生成時
與其狀態改變時執行——**不是每次繪製都跑**——而且結果會被快取,所以在兩次變動之間它不花任何
成本(除非有註冊 `panel.title` hook,否則整條路徑都會被略過)。

```lua
local BADGE = { running = "▶", idle = "•", attention = "!", exited = "×", spawning = "…" }

baton.on("panel.title", function(p)
  -- p carries id, kind, title, state, group, activity. Return a string to set the
  -- title, or nil to leave it unchanged.
  return string.format("%s %s", BADGE[p.state] or "?", p.title)
end)
```

這個 hook 永遠讀取面板的**基礎** `"<command> · <workdir>"` 標題,絕不會讀到自己上一次的結果,
所以它永遠不會回饋自己——每次都從頭重建標題。Hook 會串接(較後者會看到前面跑出來的結果),
而回傳 `nil` 或 `""` 的 hook 會讓標題保持不動。移除該 hook 並重載(`C-t R`)會還原內建標題。
一個帶狀態徽章與群組的標題範例,見 [`examples/title-hook.lua`](../examples/title-hook.lua)。

### 任務與佇列

一次派送([SPEC.md](./SPEC.zh-TW.md#任務與佇列))是一個被追蹤的任務,而外掛既能
**監看**它、也能**塑形**它。監看是一個普通的 hook——`task.change` 會在每一次轉換時觸發,
所以「記錄每一份簡報」、「任務失敗時通知」、「一步完成時串起下一步」讀起來全都和面板 hook 一樣:

```lua
baton.on("task.change", function(t)
  if t.status == "failed" then baton.notify(t.id .. " failed: " .. (t.prompt or "")) end
end)
```

塑形則是唯一的那個**過濾器 hook**,`task.pre`。不像其他每一個 hook——那些都是發完即忘、
忽略其回傳值——`task.pre` 會在**一份簡報送達之前同步執行**,而它的回傳值會改變該動作。
它是外掛能改寫或否決工作的唯一位置,是放置 allow-list、prompt 前言,或路由標籤最自然的座位:

```lua
baton.on("task.pre", function(t)
  if t.prompt:find("rm %-rf") then return false end           -- veto: drop the task
  if t.group == "review" then return "[read-only] " .. t.prompt end  -- rewrite the brief
  -- return nothing to pass it through unchanged
end)
```

這個 hook 收到一個 `{ prompt, group, score, cwd, profile, panel }` table——那份簡報,加上它
一路攜帶的脈絡:

| 欄位      | 內容                                                                   |
| --------- | ---------------------------------------------------------------------- |
| `prompt`  | 簡報的文字——每一個攔截點都會帶上的唯一欄位                             |
| `group`   | 任務指向的群組;沒有指向任何群組時為空                                  |
| `score`   | 一次派送將會注入的 score 區塊,沒有時為空——排入佇列的任務尚未攜帶 score |
| `cwd`     | 任務將落地的工作目錄;群組 fan-out 時為空                               |
| `profile` | 目標面板的 agent profile;群組 fan-out 時為空                           |
| `panel`   | 簡報將送達的面板 id;群組 fan-out 時為空                                |

`prompt` 與 `score` 是 hook 可以改寫的兩個欄位;其餘皆為唯讀的脈絡。回傳契約向後相容——
一個在 `score` 存在之前寫成的 hook,意義維持與從前一字不差:

| 回傳                        | 效果                                                                                  |
| --------------------------- | ------------------------------------------------------------------------------------- |
| `nil` / 無 / `true`         | 原封不動地讓簡報通過(prompt 與 score 皆然)                                            |
| 一個字串                    | 只改寫 prompt;score 不受影響                                                          |
| 一個 table                  | 可設定 `prompt`、`drop` 與 `score`——`score = ""` 會丟棄 score 區塊,鍵不存在則保持不動 |
| `false` / `{ drop = true }` | 否決——任務被丟棄;派送的呼叫者會收到錯誤,排隊中的任務則在待辦裡失敗                    |

Table 裡的 `score` 值必須是字串;任何其他型別都會被忽略。Hook 會在這兩個欄位上一路串接
(較後的 hook 會同時看到前一個對 prompt 與 score 的改寫),而第一個否決會終止整條串接。
它在 `dispatch` 與 `dispatch-group` 這兩個攔截點執行;而排入佇列的任務,則是在排程器把它
排到某個面板上的那一刻才執行,而不是在 enqueue 的當下——在那之前沒有面板可以塑形這份簡報。
外掛發起的派送會繞過它,所以它永遠不會再次進入自己——包含 `baton.enqueue` 排進來的任務,
無論它等了多久,送達時都是原封不動的 prompt。

**改寫只會被送出,不會被記錄。**`task list`、面板卡片與整隊快照留下的都是作者原本寫的 prompt;
改寫後的文字只送給 agent,不去別的地方。這正是讓一個會改寫的 hook 保持冪等的關鍵——每次 daemon
重啟都會把在途任務重新排回佇列,而串接是在送達時才跑,所以一個把改寫寫回自己身上的任務,每重啟
一次就會再被改寫一次。這也讓「我到底要求了什麼」仍然有答案。

送達時的否決已經沒有呼叫者可以回覆,所以任務會以 `failed` 收在待辦裡,把否決的理由一起帶著,
同時 daemon 會記下一行警告,指名任務、面板與 prompt。如果 hook 拒絕得很頻繁,請看日誌而不是
待辦:已完成的任務只保留最近的一批,所以一個什麼都拒絕的 hook 會把自己的證據擠出 `task list`。

在送達時執行有兩項成本值得先知道。hook 現在卡在排程器指派任務與 agent 收到任務之間,所以
**一個慢的 hook 會拖慢整隊視圖**:送達是在 monitor 自己的迴圈上進行的,在它們跑完之前,那個
迴圈的其他工作——telemetry、settle 成 idle、收掉已完成的 ephemeral worker——都不會回報。每個
tick 在這件事上花掉一個 monitor 間隔之後就會停下,其餘留給下一個 tick,所以延遲的上限是每個
tick 而不是整個待辦的長度;超時的 tick 會記下警告。快的 hook 永遠碰不到這個上限,所以在沒有
任何東西變慢時,一整批排隊的工作不會多花任何代價。另外,fan-out 會對每個成員各跑一次串接,並且是在呼叫端的
連線上跑:請讓 `task.pre` 保持快速,否則一個很寬的群組對發出指令的人來說就會很慢。

`score` 欄位由 Score——整隊範圍的記憶——餵入
([#37](https://github.com/cmj0121/baton/issues/37),開發中);在它推出之前,這個欄位就只是
空著。要為某一個群組剝除這個區塊,一個 table 回傳就夠了:

```lua
baton.on("task.pre", function(t)
  if t.group == "scratch" then return { score = "" } end  -- this group works without fleet memory
end)
```

`task.pre` 依契約是**故障開放(fail-open)**的:沒有 hook、一個會拋錯的 hook,或一個跑過
逾時的 hook,全都讓簡報保持不變。這個過濾器會在唯一的 Lua worker 上阻塞派送,所以一個
緩慢的 hook 是有界限的——過了截止時間,呼叫者就帶著原始簡報繼續,遲到的結果會被丟棄。
一個壞掉或緩慢的外掛永遠無法丟掉一個任務或卡死整隊;它最糟也只是過濾失敗而已。

### 加入指令 — `baton.command`

```lua
baton.command{
  name = "spawn-api-stack",
  desc = "three claude agents on the API repo, grouped",
  run = function()
    for _, dir in ipairs({ "api", "api/worker", "api/web" }) do
      baton.spawn{ kind = "agent", command = "claude", dir = "~/src/" .. dir, group = "api" }
    end
  end,
}
```

一個註冊過的指令會成為一等公民的動詞:出現在座艙的指令挑選器裡(`C-t c`),並可綁定到一個
按鍵。這就是外掛擴充 baton 詞彙的方式,而不只是在載入時把它腳本化一次。

### 設定 — `baton.config`

「設定即 Lua」這根支柱:YAML 設定所擁有的一切,都能從 Lua 設定(甚至計算)。

```lua
baton.config{
  prefix = "ctrl+t",
  default_agent = "claude",
  workdir = os.getenv("HOME") .. "/src",
  allow_name_conflict = false,
  replay_kb = 256,
}

baton.agent{ name = "claude", command = "claude", args = { "--dangerously-skip-permissions" } }
baton.agent{ name = "aider",  command = "aider" }

baton.bind("D", "diff")   -- key → action, client-side binding
```

### 工具函式

- `baton.log(level, msg)` — 寫入常駐程式的日誌(`info` / `warn` / `debug` / `error`)。
- `baton.notify(msg)` — 對已接上的座艙浮現一則**短暫的**通知(頁尾狀態列上的一則 toast)。
- `baton.footer(text)` — 設定一段**持久的**頁尾區段,顯示在每一個座艙裡;`""` 會清除它。
  不像通知,它不會淡出,所以適合放一個即時讀數(token 計數器、build 狀態)。常駐程式會保留
  最新的值,並把它交給一個剛接上的座艙,所以它在每一個 client 上都一樣。
- Lua 標準函式庫可用,所以 `os`、`io`、`string` 這些都在觸手可及之處(見下方的*信任*)。

## 範例:頁尾裡的已用 token

一個完整的外掛位於 [`examples/token-footer.lua`](../examples/token-footer.lua)。它監看 agent 的
輸出以取得 token 計數,並把整隊的總和顯示在頁尾——這是 `panel.output` + `baton.footer` 最主打
的用途:

```lua
local used = {} -- panel id -> last token count
local last = -1
local function redraw()
  local total = 0
  for _, n in pairs(used) do total = total + n end
  if total ~= last then
    last = total
    baton.footer(total > 0 and string.format("⊙ %d tok", total) or "")
  end
end

baton.on("panel.output", function(p)
  for m in p.data:gmatch("(%d[%d,]*)%s*tokens") do  -- adjust the pattern to your agent
    used[p.id] = tonumber((m:gsub(",", "")))
  end
  redraw()
end)

baton.on("panel.exit", function(p) used[p.id] = nil; redraw() end)
```

把它複製到 `$HOME/.baton/plug-in.lua`,用 `C-t R` 重載,頁尾就會隨著你的 agent 工作而帶著
`⊙ N tok`。這個檔案本身會剝除 ANSI escape,並只在總和變動時才重繪;調整 `TOKEN_PATTERN`
以符合你的 agent 印出用量的方式。

## 設定:YAML 與 Lua 並用

YAML 設定(`$HOME/.baton/config`)仍然保留——它是簡單的路徑,沒有任何東西強迫你用外掛。順序:

```txt
built-in defaults  →  YAML config  →  plug-in.lua
```

外掛在 YAML **之後**載入,能讀取生效中的設定並覆寫它們,所以 YAML 是你的基底,而 Lua 有
最後決定權。兩者都餵入同一條重載路徑,所以 `C-t R` 會一步之內重讀設定*並*重跑外掛。

## 信任

這個外掛檔是**你自己寫的可信任程式碼**,就像一個 shell rc 或一個 Neovim `init.lua`。它以
常駐程式的完整權限,以及(預設)完整的 Lua 標準函式庫執行——這正是「幾乎能控制一切」的用意。
它位於一個私有路徑(`0600`,在 `$HOME/.baton` 底下)。Baton 不對它做沙盒隔離。若有需求,
一個未來可選加入的受限模式(沒有 `os`/`io`、沒有網路)是可能的,但預設是完整的力量。

## 實作

- **引擎:** [`gopher-lua`](https://github.com/yuin/gopher-lua)——一個純 Go 的 Lua 5.1 VM。沒有
  cgo,所以 baton 維持單一靜態執行檔,不需新的建置步驟。
- **套件:** [`internal/plugin`](../internal/plugin) 掌管 `*lua.LState`、事件佇列,以及 `baton`
  table;伺服器透過架構為它保留的那個 event-dispatcher hook 餵給它。
- **單一 Lua goroutine。** VM 是單執行緒的。一個專屬的 goroutine 擁有 `LState`;載入、hook 與
  指令全都跑在它上面。伺服器事件會被送進一個由它抽空的緩衝 channel——**永遠不在 `s.mu` 之下**
  ——所以一個回頭呼叫 `baton.*` 的 hook 會重新進入(各自獨立上鎖的)核心動作而不會死鎖。唯一
  的那個同步 hook `task.pre` 搭同一個 worker,但會在一個有界限的請求上**阻塞派送**:伺服器
  在釋放 `s.mu` 的情況下,從一個緩衝 channel 讀出簡報結果,而一個逾時讓它故障開放,所以一個
  卡死的過濾器永遠不會拖住整隊。
- **對應是機械式的。** 每一次 `baton.*` 寫入都會把它的 Lua 引數封送成 socket handler 所做的
  同一個呼叫,所以外掛與傳輸線在「一項動作代表什麼、它可能怎麼失敗」上永遠不會出現分歧。

## 設計決策

實作所奠基的那些選擇:

1. **範圍——四大支柱全部到位。** Fleet API、hook、`baton.command`,以及
   `baton.config`/`agent`/`bind` 全數上線。
2. **Client 觸及——client 從常駐程式取回它的設定。** 一個外掛可以設定按鍵綁定、prefix,以及
   座艙開關;常駐程式透過 socket 提供合併後的生效設定,而座艙在接上時(以及重載時)套用它。
   註冊過的指令搭同一條 channel,並出現在座艙的指令挑選器裡。
3. **`panel.output` 是可選加入且有速率限制的**——只有在外掛為它註冊時才會掛上 handler,而且
   這條串流會被合併,所以一個多話的面板無法淹沒 worker。
4. **`baton.notify`** 是一則 server→client 的通知,與寫入常駐程式日誌的 `baton.log` 並列。
5. **設定優先序是 `defaults → YAML → Lua`**——YAML 是基底,外掛在其後載入並勝出。
6. **引擎是 `gopher-lua`**——純 Go 的 Lua 5.1,沒有 cgo。
