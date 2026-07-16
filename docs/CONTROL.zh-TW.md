# Baton — Control

[English](CONTROL.md) · **繁體中文**

> 讓一個 agent 來指揮整支隊伍。Baton 的 socket 是一整套控制平面(control plane):座艙送出的那些指令,
> 同樣能由程式來驅動。**conductor(指揮)** 就是 baton 為此而生的 agent——它會開面板、分組、送訊號,
> 並像你一樣對其他面板下提示。

指揮棒在你手上;conductor 是握著它的第二隻手。本文件是進入控制平面三種途徑的合約——**conductor** 面板、
**`baton ctl`** CLI,以及 **`baton mcp`** 伺服器——還有那些讓一個驅動自身宿主的 agent 不至於把它搞壞的護欄。

## conductor

在儀表板上按 **`C`** 開啟 conductor。它就是一個一般的 agent(你的預設 agent 設定檔,開箱即 `claude`),
只有三點不同:

- **單例(Singleton)。** 每台伺服器有且只有一個 conductor;伺服器會拒絕第二個。它**不是隊伍裡的一張卡片**
  ——而是顯示為 **`FLEET` 標題上的一個標記**(帶著它的即時狀態),因為它是驅動整隊、而非隊伍的一員,
  於是不列入名冊、不計入數量、也不觸發提醒推播。`C` 是你抵達它的方式:它會**放大(zoom)** 一個運行中的
  conductor,讓你看它工作;**重跑(re-run)** 一個已結束的(全新工作區,因此會重新載入它的簡報)並放大重啟後的它;
  或在沒有任何 conductor 時**開一個(spawn)**,並在它落地的當下就放大進去。
- **僅供控制的工作區。** conductor 跑在 baton runtime 目錄下一個全新、私有、用過即丟的目錄裡——
  絕不會是你的原始碼樹。它唯一的本機介面是 baton 放進去的控制接線:簡報(同時寫成 `BATON.md` 與 `CLAUDE.md`,
  後者是為了讓預設的 Claude conductor 自動把它讀成專案指示)以及一個 `.mcp.json`。如此一來,這個 agent 阻力最小
  的路徑就是去驅動 baton,而不是四處翻你的程式碼。工作區會在重生時重新產生,並在 conductor 關閉時移除。
- **圍上柵欄(Fenced)。** conductor 在一個限定範圍的角色下行動(見 [護欄](#護欄)):它驅動隊伍其餘成員,
  但不能對自己的面板動手、不能停掉伺服器,也不能對宿主 fork-bomb。

這份隔離是一道**護欄,而非沙箱**:這個 agent 仍以你的使用者身分執行,所以它有可能用絕對路徑碰到工作區之外。
Baton 塑造的是讓控制成為那條好走的路的環境;它並不把行程關進牢裡。

### 操作者簡報 — `$HOME/.baton/CONDUCTOR.md`

內建的入門說明告訴 conductor _如何_ 驅動 baton;而 _該做什麼_ 由你來說。在 `$HOME/.baton/CONDUCTOR.md`
寫下一個目標與指引,baton 就會在每次 conductor 被開啟或重跑時,把它接在 conductor 簡報的 **Operator's brief**
標題底下——所以編輯這個檔案再重跑 conductor(對已結束的按 `C`,或在你從放大畫面停掉一個運行中的之後),
就能更新它的常設指示。這個檔案是選用的,而且永遠不會取代入門說明:agent 始終保有控制機制與那些被禁止的動作。
例如:

```md
# Mission

Keep a reviewer agent running on each open PR worktree. When one finishes, summarise its findings into a shell panel
named "report" and pause for me.
```

## `baton ctl` — CLI

`baton ctl` 是一個架在 session socket 之上、輕薄的同步客戶端。從一般 shell 執行,它以全權座艙角色行動;
在 conductor 面板內執行,它繼承 conductor 身分並被圍上柵欄。每道指令都是連上、動作、然後退出。

| 指令                                                               | 作用                                                       |
| ------------------------------------------------------------------ | ---------------------------------------------------------- |
| `baton ctl list`                                                   | 以 JSON 印出隊伍(id、title、state、group、…)               |
| `baton ctl tree [--json]`                                          | 畫出 daemon 的行程樹:group → panel → 各自的 OS 子行程      |
| `baton ctl spawn [--agent CMD] [--arg A] [--dir D]`                | 開一個面板(有 `--agent` 就是 agent,否則是 shell);印出新 id |
| `baton ctl send <id> <text> [--no-enter]`                          | 把文字打進某個面板;除非 `--no-enter`,否則以換行送出        |
| `baton ctl group <name> <id>...`                                   | 把面板歸入一個工作項目(斜線 `path` 可巢狀:`backend/api`)   |
| `baton ctl rename [--id ID \| --group G] <name>`                   | 重新命名面板或群組(把群組改名成路徑即可重新掛父層)         |
| `baton ctl pin <id>...` / `unpin <id>...`                          | 把面板釘上 / 取消釘於即時磚                                |
| `baton ctl signal <signal> <id>...`                                | 送出訊號,例如 `SIGINT`                                     |
| `baton ctl close <id>...`                                          | 關閉面板                                                   |
| `baton ctl dispatch <id> <prompt>`                                 | 指派一份任務簡報給某個面板,並整批送達                      |
| `baton ctl dispatch-group <group> <prompt>`                        | 把一份簡報散發給一個工作項目的整棵子樹(含巢狀群組)         |
| `baton ctl queue add <prompt> [--group G]`                         | 把一項任務排入佇列,交由排程器抽取分派給空閒的 agent        |
| `baton ctl queue add <prompt> --command <cmd> [--dir D] [--close]` | 隨需開新(spawn-on-demand):沒人空閒時就備一個 agent         |
| `baton ctl queue list`                                             | 以 JSON 印出待辦(id、prompt、status、panel、group、…)      |
| `baton ctl queue cancel <id>`                                      | 依 id 取消一項已排入的任務                                 |
| `baton ctl queue promote <id>` / `demote <id>`                     | 把一項已排入的任務移到待辦的最前 / 最後                    |
| `baton ctl queue drain`                                            | 清空每一項已排入的任務                                     |

```sh
# Stand up a reviewer next to a worker and hand it the task.
id=$(baton ctl spawn --agent claude --dir ~/src/api)
baton ctl group review "$id"
baton ctl dispatch "$id" "review the open diff and list correctness risks"

# Or queue a batch and let the scheduler fan it across whoever comes free.
baton ctl queue add "audit the auth module"   --group review
baton ctl queue add "audit the billing module" --group review
baton ctl queue list

# Burst a fresh worker fleet through the backlog: each task spawns its own
# ephemeral agent when none is free, and closes it when the task is done.
baton ctl queue add "port module A" --command claude --dir ~/src --close
baton ctl queue add "port module B" --command claude --dir ~/src --close

# 看看 daemon 實際在跑什麼:把隊伍接上每個面板真正開出的 OS 行程。--json 餵給
# 監看程式或腳本。
baton ctl tree
```

**行程樹。** `tree` 以 daemon 為根,鋪出隊伍裡巢狀的工作項目群組,把每個面板依群組歸位並標上它 process group
leader 的 pid,再把該面板底下即時的 OS 子孫行程掛上去——這是 `ps`/`pstree` 給不了的畫面,因為只有 baton 知道哪個
pid 是哪個 agent:

```text
baton (daemon) pid=41022  baton
├─ [group: feature-x]
│  ├─ [hale/running] pid=41180  claude
│  │  └─ pid=41199  node
│  └─ [ellis/idle] pid=41205  bash
└─ [ungrouped]
   └─ [shell/running] pid=41240  zsh
```

**dispatch 與 send 的差別。** `send` 打的是原始按鍵;`dispatch` 交給伺服器的是那份 _目標_,伺服器會把它記在
面板上(於是它會傳到每一張卡片與快照)並整批送達——它會等 agent 就緒,而不是與一道運行中的指令交錯插入。
模型細節見 [任務與佇列](./SPEC.zh-TW.md#任務與佇列)。

## `baton mcp` — MCP 伺服器

`baton mcp` 是一個跑在 stdio 上的 [Model Context Protocol](https://modelcontextprotocol.io) 伺服器
(以換行分隔的 JSON-RPC 2.0)。它把同一組動詞以 MCP 工具形式對外提供,讓一個會講 MCP 的 agent 透過結構化的
工具呼叫來驅動隊伍,而不必去 shell 出去執行:

`baton_list` · `baton_spawn` · `baton_send` · `baton_dispatch` · `baton_dispatch_group` · `baton_enqueue` ·
`baton_queue` · `baton_reorder` · `baton_group` · `baton_rename` · `baton_pin` · `baton_unpin` · `baton_signal` ·
`baton_close`

`baton_dispatch` / `baton_dispatch_group` 把一份任務簡報指派給某個面板或整個工作項目;`baton_enqueue`
把一項加入待辦(可選隨需開新,附一個 `command` 以便沒人空閒時備一個 worker),`baton_queue` 讀回它,
而 `baton_reorder` 把一項等待中的任務移到最前或最後。這些正是 conductor 用來跑那條招牌
**你 → conductor → 隊伍** 流程的動詞:你把一批目標交給 conductor,它把它們排入佇列,排程器則在 worker
一空下來就把它們抽取分派過去。

conductor 的工作區隨附一個 `.mcp.json`,指向這支以 `baton mcp` 執行的同一份執行檔,所以一個 Claude conductor
會自動載入這些工具——無需設定。這個 MCP 子行程繼承 conductor 面板的環境,因此它被圍上柵欄的方式與 CLI 完全相同。
一次工具失敗(參數錯誤、指令被拒、常駐程式掛掉)會以一個 MCP 錯誤結果回傳,讓模型能讀到並從中復原,
而不是斷掉連線。

## 直接對接線路

兩種介面都只是 socket 之上的輕薄包裝——偏好原始 JSON-RPC 的 agent 可以直接講。控制客戶端在 `hello`
握手時宣告自己的身分:

| 欄位   | 含義                                                  |
| ------ | ----------------------------------------------------- |
| `role` | `"conductor"` 表示要被圍上柵欄;留空(座艙)則為全權。   |
| `self` | 客戶端自己的面板 id——伺服器會拒絕讓它對這個面板動手。 |

一次 dispatch 多帶兩個欄位:`prompt`(那份簡報)以及一個選用的 `submit` 覆寫值(接在後面用來送出的按鍵,
預設為換行),用在 `panel.dispatch` / `panel.dispatch-group`;`task.enqueue` / `task.cancel` / `task.promote` /
`task.demote` / `task.drain` / `task.list` 驅動待辦,並以一份 `tasks` 快照回覆。一次隨需開新的 `task.enqueue`
會帶著 worker 的 `path` / `args` / `dir`,以及一個 `ephemeral` 完成即關的旗標。

Baton 把接線注入 conductor 面板的行程,`baton ctl` 與 `baton mcp` 兩者都會自動讀取:

| 變數             | 是                                       |
| ---------------- | ---------------------------------------- |
| `BATON_SOCK`     | 要撥接的控制 socket                      |
| `BATON_ROLE`     | `conductor`——在 hello 時要宣告的限定角色 |
| `BATON_PANEL_ID` | conductor 自己的面板 id(它的 `self`)     |

## 護欄

conductor 角色由伺服器端強制執行,早在任何指令生效之前。它以那道**僅限本 uid 的 socket** 上自行宣告的角色
為依據——這是一道防範 agent 意外的護欄,而非安全邊界(你這個使用者的任何本機行程,永遠都能直接對 socket 說話)。

| conductor 可以做                       | conductor 不可以做                        |
| -------------------------------------- | ----------------------------------------- |
| list、spawn、group、rename、pin、move  | 關閉、送訊號,或送輸入到**它自己的**面板   |
| 對**其他**面板送訊號與送輸入           | **把任務 dispatch 給它自己的**面板        |
| dispatch 給其他面板、把任務排入佇列    | **清空佇列**——把待辦清光是操作者專屬      |
| 重新排序已排入的任務(promote / demote) |                                           |
| 關閉其他面板、清除已結束               | 重載或停掉伺服器                          |
|                                        | 開新的速度超過速率上限,或超過隊伍上限(64) |

所以 conductor 能填滿待辦並從中 dispatch,卻無法把它抹除;而佇列也給不了它繞過自我柵欄的路子:它排入的一份簡報,
會被排程器抽取分派給 _其他_ 空閒的 agent,絕不會回到它自己身上。

一個純座艙連線不宣告任何角色,也永遠不會被圍上柵欄。
