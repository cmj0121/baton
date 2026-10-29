# Baton — Control

<!-- markdownlint-disable MD049 -- this page already uses underscore emphasis; prettier agrees. -->

[English](CONTROL.md) · **繁體中文**

> 讓一個 agent 來指揮整支隊伍。Baton 的 socket 是一整套控制平面(control plane):座艙送出的那些指令,
> 同樣能由程式來驅動。**conductor(指揮)** 就是 baton 為此而生的 agent——它會開面板、分組、送訊號,
> 並像你一樣對其他面板下提示。

指揮棒在你手上;conductor 是握著它的第二隻手。本文件是進入控制平面三種途徑的合約——**conductor** 面板、
**`baton ctl`** CLI,以及 **`baton mcp`** 伺服器——還有那些讓一個驅動自身宿主的 agent 不至於把它搞壞的護欄。

## conductor

在儀表板上按 **`n C`** 開啟 conductor。它就是一個一般的 agent(你的預設 agent 設定檔,開箱即 `claude`),
只有四點不同:

- **單例(Singleton)。** 每台伺服器有且只有一個 conductor;伺服器會拒絕第二個。它**不是隊伍裡的一張卡片**
  ——而是顯示為 **`FLEET` 標題上的一個標記**(帶著它的即時狀態),因為它是驅動整隊、而非隊伍的一員,
  於是不列入名冊、不計入數量、也不觸發提醒推播。`n C` 是你抵達它的方式:它會**放大(zoom)** 一個運行中的
  conductor,讓你看它工作;**重跑(re-run)** 一個已結束的(接線會重寫,因此會重新載入它的簡報)並放大重啟後的它;
  或在沒有任何 conductor 時**開一個(spawn)**,並在它落地的當下就放大進去。
- **僅供控制的工作區。** conductor 跑在 baton 自己的一個私有目錄裡——絕不會是你的原始碼樹。它唯一的本機介面是
  baton 放進去的控制接線:簡報(同時寫成 `BATON.md` 與 `CLAUDE.md`,後者是為了讓預設的 Claude conductor
  自動把它讀成專案指示)、一個 `.mcp.json`,以及放在 `.claude/skills/` 底下的[技能](#agent-技能)。如此一來,
  這個 agent 阻力最小的路徑就是去驅動 baton,而不是四處翻你的程式碼。
- **一個工作區,留到你重開機為止。** 工作區是每個控制 socket 一份,而不是每次開啟一份,所以關掉 conductor
  再開一次會回到同一個目錄、帶著它先前收集的設定——agent 寫在自己旁邊的權限授權會跨重啟存活,
  不必每次都重按一遍。它位於 `$XDG_RUNTIME_DIR/baton/`(或你的暫存目錄)底下,只有在不存在時才建立,
  並且**在主機重開機時清空**:baton 會為它蓋上所屬那次開機的戳記,一旦對不上就整個重建。
  `baton ctl conductor reset` 可以隨時清掉它——請先關閉 conductor,另外 conductor 自己被柵欄擋在這個指令之外。
  目錄名是 socket 的雜湊,所以 conductor 開啟時會把路徑寫進日誌。一個但書:base 取自 daemon 啟動時所處的環境,
  因此從 ssh session 啟動的 daemon 與從桌面終端機啟動的,可能會算出不同的工作區。
- **圍上柵欄(Fenced)。** conductor 在一個限定範圍的角色下行動(見 [護欄](#護欄)):它驅動隊伍其餘成員,
  但不能對自己的面板動手、不能停掉伺服器,也不能對宿主 fork-bomb。

這份隔離是一道**護欄,而非沙箱**:這個 agent 仍以你的使用者身分執行,所以它有可能用絕對路徑碰到工作區之外。
Baton 塑造的是讓控制成為那條好走的路的環境;它並不把行程關進牢裡。[資源上限](LIMITS.zh-TW.md)確實會對它能耗用
的量設下真正的天花板——CPU、記憶體、行程數——但那是資源邊界,不是檔案系統或網路的邊界。

**艦隊路徑**是你 → conductor → 艦隊。把一批目標交給它;它開工人、分組、派工或排進佇列。你留在收件匣。單一個
agent 仍是 **`A`**。一整隊是 **`n C`** 加上 `$HOME/.baton/CONDUCTOR.md` 裡的常設命令。

| 問題               | 選定                                                                                                                                                                                                                                                         |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 控制是什麼         | **只派工。** conductor 開、分組、派工、排隊、送訊號、關掉*別人的*面板。卡住時舉*自己的*手。收件匣仍是操作者的。conductor 不是旁觀再操盤,也不是坐在艦隊裡的規劃者。                                                                                           |
| 它怎麼在場         | **手動 `n C`**(或在它已結束的格子上按 `r`)。常駐程式啟動時不會自動開。磁碟上有 `CONDUCTOR.md` 不是開關。                                                                                                                                                     |
| `A` 鍵             | **不變。** 單一個 agent 仍是 `A`。艦隊路徑就在旁邊:`n C` + `$HOME/.baton/CONDUCTOR.md`。                                                                                                                                                                     |
| 工人的 `baton ctl` | **不變。** 空的 `BATON_ROLE` 仍是完整座艙。真正拿掉 `BATON_SOCK` 的旋鈕仍是隔離(`isolate: docker`)。                                                                                                                                                         |
| 工人對工人         | 預設關閉,直到你為某一個群組打開。互通(peers)旗標開著的群組,裡面的面板可以互相列出、讀彼此最近的輸出、往彼此的終端機打一行字,見 [群組互通](#群組互通)。由操作者打開;conductor 與 agent 面板會被拒絕使用這個動詞。這是一道護欄,有護欄的[限制](#它不保護什麼)。 |

### 操作者簡報 — `$HOME/.baton/CONDUCTOR.md`

內建的入門說明告訴 conductor _如何_ 驅動 baton;而 _該做什麼_ 由你來說。在 `$HOME/.baton/CONDUCTOR.md`
寫下一個目標與指引,baton 就會在每次 conductor 被開啟或重跑時,把它接在 conductor 簡報的 **Operator's brief**
標題底下。它同時也可以**熱重載**:按 `C-t R`(或對常駐程式送一個 `SIGHUP`)會把簡報重新寫進正在運行的 conductor
工作區,並在它的面板印一行提示——不需要關掉再重開。熱重載做不到的是當場改變 agent 的想法:agent 是在 session
起始時讀它的專案指示,所以更新後的簡報是它下次去讀時會看到的內容,而那行提示的用途就是告訴你「有新東西可以看了」。
這個檔案是選用的,而且永遠不會取代入門說明:agent 始終保有控制機制與那些被禁止的動作。把
[`examples/CONDUCTOR.md`](../examples/CONDUCTOR.md) 複製到 `$HOME/.baton/CONDUCTOR.md` 再改任務:

```md
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
```

### 黃金迴圈

動詞照這個順序,不要 `send`:

1. `baton ctl list` —— 看誰還活著。
2. `baton ctl spawn --worktree`(自己的 checkout)或 `baton ctl queue add --command`(沒人空閒時再開)。
3. `baton ctl group` —— 把工人歸進一個工作項目。
4. `baton ctl dispatch` / `dispatch-group` / `queue add` —— 給工作。

## `baton ctl` — CLI

`baton ctl` 是一個架在 session socket 之上、輕薄的同步客戶端。從一般 shell 執行,它以全權座艙角色行動;
在 conductor 面板內執行,它繼承 conductor 身分並被圍上柵欄。每道指令都是連上、動作、然後退出。

| 指令                                                                  | 作用                                                                      |
| --------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| `baton ctl list`                                                      | 以 JSON 印出隊伍(id、title、state、group、…)                              |
| `baton ctl tree [--json]`                                             | 畫出行程樹(group → panel → OS 子行程),附 CPU%/RSS                         |
| `baton ctl spawn [--agent CMD] [--arg A] [--dir D]`                   | 開一個面板(有 `--agent` 就是 agent,否則是 shell);印出新 id                |
| `baton ctl spawn --run CMD [--arg A] [--dir D]`                       | 開一個 command 面板:一個隊伍會看顧、卻永遠不編派工作給它的普通執行檔      |
| `baton ctl spawn --worktree --dir <repo> --branch <name> --agent CMD` | 從 `<repo>` 開一條分支、替它開一個 git worktree,並把 agent 開在那棵樹裡   |
| `baton ctl send <id> <text> [--no-enter]`                             | 把文字打進某個面板;除非 `--no-enter`,否則以換行送出                       |
| `baton ctl attention --why <text> [--id ID]`                          | 說這個面板需要人,以及為什麼——見 [舉手](#舉手)                             |
| `baton ctl resolve [--id ID]`                                         | 說那個理由已經過去了;面板離開待辦                                         |
| `baton ctl attention-mark --id ID`                                    | 說某個面板的畫面是在等:給分類器的一筆資料,面板本身不變                    |
| `baton ctl classifier status`                                         | 關於注意力分類器的一行:關著、連不上,或 mold 自己怎麼說                    |
| `baton ctl group <name> <id>...`                                      | 把面板歸入一個工作項目(斜線 `path` 可巢狀:`backend/api`)                  |
| `baton ctl group peers <group> on\|off`                               | 讓某一個群組裡的面板互相讀取與輸入,見 [群組互通](#群組互通)               |
| `baton ctl rename [--id ID \| --group G] <name>`                      | 重新命名面板或群組(把群組改名成路徑即可重新掛父層)                        |
| `baton ctl pin <id>...` / `unpin <id>...`                             | 把面板釘上 / 取消釘於即時磚                                               |
| `baton ctl signal <signal> <id>...`                                   | 送出訊號,例如 `SIGINT`                                                    |
| `baton ctl close <id>...`                                             | 關閉面板                                                                  |
| `baton ctl dispatch <id> <prompt>`                                    | 指派一份任務簡報給某個面板,並整批送達                                     |
| `baton ctl dispatch-group <group> <prompt>`                           | 把一份簡報散發給一個工作項目的整棵子樹(含巢狀群組)                        |
| `baton ctl queue add <prompt> [--group G]`                            | 把一項任務排入佇列,交由排程器抽取分派給空閒的 agent                       |
| `baton ctl queue add <prompt> --command <cmd> [--dir D] [--close]`    | 隨需開新(spawn-on-demand):沒人空閒時就備一個 agent                        |
| `baton ctl queue list`                                                | 以 JSON 印出待辦(id、prompt、status、panel、group、…)                     |
| `baton ctl queue cancel <id>`                                         | 依 id 取消一項已排入的任務                                                |
| `baton ctl queue promote <id>` / `demote <id>`                        | 把一項已排入的任務移到待辦的最前 / 最後                                   |
| `baton ctl queue drain`                                               | 清空每一項已排入的任務                                                    |
| `baton ctl conductor reset`                                           | 刪掉 conductor 的工作區,讓下一個從乾淨狀態開始                            |
| `baton ctl worktree list`                                             | 以 JSON 印出 baton 開過的 worktree,各標為 `live` / `dead-slot` / `orphan` |
| `baton ctl worktree sweep [--yes]`                                    | 移除其中的孤兒樹;在終端機會先確認,在指令稿裡需要 `--yes`                  |

```sh
# 在一個 worker 旁邊立一個 reviewer,然後把任務交給它。
id=$(baton ctl spawn --agent claude --dir ~/src/api)
baton ctl group review "$id"
baton ctl dispatch "$id" "review the open diff and list correctness risks"

# 或者排入一整批,讓排程器把它們散給任何空出來的人。
baton ctl queue add "audit the auth module"   --group review
baton ctl queue add "audit the billing module" --group review
baton ctl queue list

# 讓一支全新的 worker 隊伍衝過待辦:沒人空閒時,每項任務就開出自己的
# 臨時 agent,而任務做完就把它關掉。
baton ctl queue add "port module A" --command claude --dir ~/src --close
baton ctl queue add "port module B" --command claude --dir ~/src --close

# 讓每個 worker 有自己的 checkout,而不是共用一個。這裡的 --dir 是「儲存庫」,
# 不是工作目錄:工作目錄是 baton 開出來的那棵樹,而新面板會以分支名歸入工作項目。
baton ctl spawn --worktree --dir ~/src/api --branch feat/login --agent claude

# 看看 daemon 實際在跑什麼:把隊伍接上每個面板真正開出的 OS 行程。--json 餵給
# 監看程式或腳本。
baton ctl tree
```

加上 `--worktree` 之後,`--dir` 的意思就變了:它指的是要開分支的儲存庫,而面板的工作目錄是 baton 替它開的那個
worktree。`--branch` 是必填的,而少了分支、或 `--dir` 根本不是儲存庫,都會在造出任何樹之前就被拒絕。沒有
`--worktree` 時,`spawn` 一切照舊——`--dir` 就是工作目錄,而且不會跑任何 git。只要 conductor 的 worker
原本得共用同一個 checkout,它就該優先用這個:多個 agent 平行改同一棵樹,會互相蓋掉彼此的成果。

**行程樹。** `tree` 以 daemon 為根,鋪出隊伍裡巢狀的工作項目群組,把每個面板依群組歸位並標上它 process group
leader 的 pid,再把該面板底下即時的 OS 子孫行程掛上去——這是 `ps`/`pstree` 給不了的畫面,因為只有 baton 知道哪個
pid 是哪個 agent。每個帶 pid 的行還會附上該行程自啟動以來的累計 CPU% 與常駐記憶體(RSS);群組與已結束的面板不
帶欄位。`--json` 會把同樣的數字放進每個節點的 `cpu`/`rss` 欄位。

```text
baton (daemon) pid=41022  baton  0.3%  28.4M
├─ [group: feature-x]
│  ├─ [hale/running] pid=41180  claude  12.5%  180.2M
│  │  └─ pid=41199  node  3.1%  95.7M
│  └─ [ellis/idle] pid=41205  bash  0.0%  2.1M
└─ [ungrouped]
   └─ [shell/running] pid=41240  zsh  0.0%  3.4M
```

**dispatch 與 send 的差別。** `send` 打的是原始按鍵;`dispatch` 交給伺服器的是那份 _目標_,伺服器會把它記在
面板上(於是它會傳到每一張卡片與快照)並整批送達——它會等 agent 就緒,而不是與一道運行中的指令交錯插入。
模型細節見 [任務與佇列](./SPEC.zh-TW.md#任務與佇列)。

## 舉手

這裡其他每一個動詞,都是你**對**一個面板做的事。`attention` 與 `resolve` 是那兩個 agent 用來說**自己**的動詞——
它是唯一真正知道自己什麼時候被卡住的參與者。

```sh
# 在 agent 面板裡:卡在一個決定上,用人類會讀到的那句話說出來。
baton ctl attention --why "two migrations conflict — which one wins?"

# ……拿到答案之後,把手放下。
baton ctl resolve
```

上面兩者都不帶 id,因為連線早就知道它是哪個面板了:baton 會把 `BATON_PANEL_ID` 注入**每一個 agent 面板**
(見下表),控制客戶端在 `hello` 時宣告它,常駐程式就對那個面板動作——所以一個 agent 不必先發現自己的 id 就能舉手,
而且在隊伍裡的任何地方都成立。其他地方——**shell** 面板、腳本、你自己的終端機——請用 `--id` 指名面板;兩者皆無時,
常駐程式會回答 `no panel id, and this connection declared no self`,而不是對著空氣動作。一條 **conductor** 連線
永遠只能指名它自己(見[護欄](#護欄))。

**它為什麼壓過其他一切。** Baton 有兩種方式察覺一個面板需要你,而兩種都是從外面做的猜測:一個讀沉默的**計時器**,
以及一個讀最後一行有內容的列——再加上它上面一小段 overlay 片語——有沒有問句的**啟發式**。宣告是唯一一個來自
「被描述的那個東西自己」的訊號,所以它贏——狀態見[生命週期](./SPEC.zh-TW.md#生命週期),完整優先序與一隻舉起的手
會落進的那份待辦見 [ATTENTION.zh-TW.md](./ATTENTION.zh-TW.md)。具體來說:

- **它立刻生效**,而不是等到下一個 monitor tick。任務排程器的空閒池讀的是面板的狀態,所以一個 tick 的延遲,就是
  一扇窗——baton 會在那扇窗裡把待辦工作交給一個已經說了自己在等人的 agent。
- **它撐得過輸出。** 一個在等你的時候還在印轉輪的 agent 會保住它舉起的手;由啟發式升起的 attention,則會被下一個
  位元組收回。只有 `resolve`,或行程結束,能收回一份宣告。
- **`--why` 是必填的。** 一份宣告之所以能取代兩個猜測,正因為它說得出理由,而讀待辦的人看到的是那句話,而不是一行
  被刮下來的終端機文字。說不出理由的宣告會被拒絕。
- **它不持久化。** 宣告是一個活著的行程對自己的陳述;它隨面板一起死去,不會在還原之後回來。

**`resolve` 是讓另一半可信的那一半。** 一個放得下手的 agent,它舉起的手才有意義。當沒有任何宣告成立時它是 no-op
而不是錯誤,所以無條件執行它是安全的;而在它之後,常駐程式會從一般的生命週期重新推導面板的狀態,而不是去猜一個。
它同時會把該面板的尾巴啟發式**靜音**,直到該面板下次產生輸出——否則那個原封不動、還躺在捲動內容裡的同一個問題,
一秒後又會把旗子升起來,`resolve` 就成了一個會撤銷自己的動詞。

**理由會在進來時被清洗。** 常駐程式在接受宣告時,會把 `--why` 裡的控制字元與 escape 序列刮掉,連 Unicode 的
**format** 字元一起丟(像 `U+202E` 這種 bidi override 會讓那一列在操作者的終端機裡反著畫),把任何空白摺成單一
空格,並把結果截斷到 **200 個 rune**——它是一句給人讀的話,而且它會搭上每一次隊伍快照送到每一個客戶端。所以它被
存下來的時候,已經是一行短而安全的文字。

這裡刻意是清洗發生的邊界,因為這段文字會被扇出到座艙的待辦(畫進一台真的終端機)、`baton ctl list`、MCP 工具結果
與外掛事件處理器——**一個在畫 `reason` 的前端,拿到的是已經安全的文字,而且絕不該再跳脫一次。** 面板的**輸出**是
相反的情況,會逐位元組原樣通過:那是終端機串流,而該由模擬器去解釋它。

## `baton mcp` — MCP 伺服器

`baton mcp` 是一個跑在 stdio 上的 [Model Context Protocol](https://modelcontextprotocol.io) 伺服器
(以換行分隔的 JSON-RPC 2.0)。它把同一組動詞以 MCP 工具形式對外提供,讓一個會講 MCP 的 agent 透過結構化的
工具呼叫來驅動隊伍,而不必去 shell 出去執行:

`baton_list` · `baton_spawn` · `baton_send` · `baton_attention` · `baton_resolve` · `baton_dispatch` ·
`baton_dispatch_group` · `baton_enqueue` · `baton_queue` · `baton_reorder` · `baton_group` · `baton_rename` ·
`baton_pin` · `baton_unpin` · `baton_signal` · `baton_close`

`baton_spawn` 收 `{agent, args, dir}`;改收 `{run, args, dir}` 則會開出一個 command 面板——一個會被看顧、卻永遠
不會被交付工作的普通執行檔——而在 agent 那個型式上再加 `{worktree: true, branch}`,就會開進一個新的 git worktree,
而不是開在 `dir` 裡——用同一個動詞而不是另開一個工具,所以已經會開面板的 conductor 不必再去認識新東西。有 `worktree` 時,
`dir` 指的是要開分支的儲存庫;`worktree` 少了 `branch`、或 `dir` 不是儲存庫,都是工具錯誤,而隊伍原封不動。

這裡完全沒有 worktree 工具——既不能列出,也不能清掃。conductor 負責開 worktree(透過 `baton_spawn`),
而讓它們退場是操作者的事;一個讓 agent 看得到、卻不准它清掉的工具,只會讓它一直來催。不過光是「工具不
存在」還不是完整的圍欄:conductor 面板會被注入 `BATON_ROLE`,所以一個改去呼叫 `baton ctl worktree sweep`
的 agent,到了 daemon 一樣是一條 conductor 連線——而 daemon 會拒絕它的 `worktree.sweep`。少掉的工具擋住
MCP 那條路,這道拒絕擋住 `ctl` 那條路。

`baton_dispatch` / `baton_dispatch_group` 把一份任務簡報指派給某個面板或整個工作項目;`baton_enqueue`
把一項加入待辦(可選隨需開新,附一個 `command` 以便沒人空閒時備一個 worker),`baton_queue` 讀回它,
而 `baton_reorder` 把一項等待中的任務移到最前或最後。這些正是 conductor 用來跑那條招牌
**你 → conductor → 隊伍** 流程的動詞:你把一批目標交給 conductor,它把它們排入佇列,排程器則在 worker
一空下來就把它們抽取分派過去。

`baton_attention` / `baton_resolve` 是 agent 用在**自己**身上、而不是用在隊伍上的那一對:`why` 是必填的,而且
會成為人類讀到的那句話,兩者在沒給 `id` 時都預設指向呼叫者自己的面板。一份宣告為什麼壓過 baton 自己的猜測,見
[舉手](#舉手)。

conductor 的工作區隨附一個 `.mcp.json`,指向這支以 `baton mcp` 執行的同一份執行檔,所以一個 Claude conductor
會自動載入這些工具——無需設定。這個 MCP 子行程繼承 conductor 面板的環境,因此它被圍上柵欄的方式與 CLI 完全相同。
一次工具失敗(參數錯誤、指令被拒、常駐程式掛掉)會以一個 MCP 錯誤結果回傳,讓模型能讀到並從中復原,
而不是斷掉連線。

一般的 agent 面板拿到的是另一張小得多的表。worker 開啟時會指向一份 baton 寫在自己目錄裡的 MCP 設定
(`~/.baton/agent-mcp.json`,見 [SCORE.zh-TW.md](SCORE.zh-TW.md)),那份設定把這支執行檔跑成
`baton mcp --score-only`。這個旗標選的是 worker 表,只有四個工具,上面那些駕駛隊伍的動詞一個都沒有:

`score_submit` · `baton_peers` · `baton_peek` · `baton_tell`

旗標的名字取自第一個工具,比另外三個都早;留著它是因為已經在跑的面板,設定裡寫的就是它。`score_submit`
寫進隊伍的記憶。三個 `baton_*` 工具是 worker 碰到同群組其他面板的方式,也就是下一節要講的事。conductor 的表
是上面列的那一張,沒有這三個。

每個伺服器也會一開始就說明自己是做什麼的。`initialize` 的回覆帶著一段依角色而定的 `instructions` 字串:baton
是什麼、這個伺服器提供哪些工具、以及哪個技能解釋何時該用它們——conductor 是 `conduct`,worker 是 `peers` 與
`score`。不支援技能的 client 永遠找不到技能,但前兩句仍然足以讓它搞清楚方向。

### Agent 技能

工具說明講的是工具做什麼,卻完全不講何時該拿起它、禮節是什麼、被拒絕又代表什麼——而另一個載體,conductor
的 primer,只有 conductor 自己讀。baton 把這些指引做成 Agent Skills 隨附:每個主題一份 `SKILL.md`,Claude Code
只用它的一行說明把它列出來,等 agent 自己判斷需要時才讀全文,所以一個技能在被用到之前,只花一個 session
一行 context。內容嵌在執行檔裡,所以 agent 讀到的技能,描述的永遠是同一支執行檔提供的工具。

| 角色      | 技能                        | 寫到哪裡                                                                                             |
| --------- | --------------------------- | ---------------------------------------------------------------------------------------------------- |
| worker    | `baton`, `peers`, `score`   | `~/.baton/agent-plugin/<hash>/` 底下一個名為 `baton-panel` 的 plugin,以 `--plugin-dir` 交給 `claude` |
| conductor | `baton`, `conduct`          | conductor 工作區裡的 `.claude/skills/`                                                               |
| outside   | `baton`, `conduct`, `drive` | 你裝到哪裡就在哪裡——見 [baton 之外的 agent](#baton-之外的-agent)                                     |

`baton` 是每個角色共用的入口:baton 是什麼、怎麼知道自己是哪個角色、下一個該讀哪個技能。它不提任何工具,因為
沒有任何工具出現在每一張表裡。`peers` 與 `score` 涵蓋 worker 表;`conduct` 涵蓋 dispatch 與 send 的分別、
佇列、群組、signal、護欄以及整理 score。每個角色只拿到它手上工具的技能:worker 被拒絕使用駕駛隊伍的動詞,
conductor 被拒絕使用 peer 動詞,而教讀者一個它呼叫不了的工具,只會教它去被拒絕。

worker 的技能永遠不會落進你的 repo,理由和它的 MCP 設定一樣:它跑在你的工作目錄裡,baton 在那裡寫的 dotfile
會出現在 `git status` 上。plugin 目錄以其中全部內容的雜湊命名,而且從不就地重寫,所以運行中的面板會繼續讀它啟動
時的那棵樹,同時較新的 baton 在旁邊建下一棵。舊的雜湊目錄會留在磁碟上——運行中的面板可能還在讀——等沒有面板在
讀時就可以放心刪掉。雜湊目錄存在卻不完整,只可能是手動改出來的;baton 會拒絕它,而不是刪掉一個別的啟動可能
正在用的路徑,記一行 Warn,然後讓面板在沒有技能的情況下啟動。把那個目錄刪掉就好了。

conductor 的技能在每一次 spawn 與重跑時都會重寫,疊在它的 primer 之上,從不取而代之。primer(`CLAUDE.md`)每次
都會被讀,技能則只在 agent 選擇時才讀,所以 conductor 據以行動的工具清單留在 primer 裡,由 primer 指向
`.claude/skills/conduct/SKILL.md` 提供深度。`.claude/skills/` 很可能也放著你自己的技能,所以 baton 把它寫過的
名字記在 `.claude/skills/.baton-skills`,只會取代或移除那些。你自己放進去的技能,不管叫什麼名字都不會被動到。

worker 面板只有在下列條件全部成立時才會拿到技能:

- `panel.agent-mcp` 是開的(預設值),而且這是 agent 面板,不是 conductor。
- baton 把自己的 MCP 設定接進了這次啟動。CLI 不收 MCP 設定選項、命令列已經指定了自己的設定、或設定寫不進去時
  都沒有——技能教的是那個面板不會有的工具。`baton ctl score status` 會回報哪些面板拿到了設定、其餘的又為什麼
  沒有(見 [SCORE.zh-TW.md](SCORE.zh-TW.md#到底哪些面板寫得進去))。
- 後端是 Claude Code。`--plugin-dir` 是 `claude` 的旗標;其他後端一律沒有。
- 那個 `claude` 收 `--plugin-dir`。這個旗標比 CLI 年輕,舊版遇到不認得的旗標會直接結束,所以盲目附上它,會把
  「這個面板少了技能」變成「這個面板起不來」。附上之前,baton 會跑一次 `claude --help`——用的是在 daemon 的
  `PATH` 上找到的那支執行檔,也就是啟動時真正會 exec 的那支——不給 stdin、限時 2 秒,然後找這個旗標。答案依
  執行檔路徑與修改時間快取,所以就地升級會再問一次。
- 面板沒有隔離。在 `isolate: docker` 底下,執行檔在 image 裡,而主機上的 `claude`——probe 唯一跑得到的那支——
  對它什麼也說明不了。

和 MCP 設定一樣,這一切都在每一次啟動時決定,不會存在面板身上,所以重跑(`r`)或從快照還原的面板,只要條件
一成立就會拿到技能。

worker 沒有技能時,baton 會把原因留在 `$HOME/.baton/baton.log`;其中三行 probe 的記錄會帶上執行檔名:

| 那一行                                               | 等級  | 意思                                                       | 快取                   |
| ---------------------------------------------------- | ----- | ---------------------------------------------------------- | ---------------------- |
| `this claude predates --plugin-dir`                  | Debug | help 印出來了但沒有這個旗標:升級 `claude`                  | 直到執行檔改變         |
| `could not ask claude whether it takes --plugin-dir` | Warn  | `--help` 失敗,或執行檔跑不起來                             | 直到執行檔改變         |
| `claude did not answer --help in time`               | Warn  | 2 秒內沒有回應——磁碟還冷、主機太忙、第一次執行正在解壓自己 | 不快取:下次 spawn 再問 |
| `agent skills not written`                           | Warn  | plugin 樹建不起來,包括上面那種不完整目錄的情況             | 不快取                 |

Debug 那一行只有以 `-v` 啟動的 daemon 才會出現。快取的答案不會跑 probe,也什麼都不記,所以一個結論只會由提問的
那次 spawn 記一次;只有反覆逾時才會重複它的 Warn。daemon 記到 Debug 等級卻完全沒有 probe 的那一行,代表條件在更早
就擋下了——去看 `score status` 裡的 MCP 設定,以及這個面板是否被隔離、或跑的是別的後端;又或者在 daemon 的
`PATH` 上找不到 `claude`,這種情況會直接答否、一行也不記。

### baton 之外的 agent

上面的技能只到得了 baton 啟動的 agent。你在自己終端機裡跑的 agent——一個你叫它「在這個分支上開三個 reviewer」的
Claude Code session——從沒聽過 baton,所以 baton 為它另外附了第三組:`baton` 告訴它自己是哪個角色,`drive` 講怎麼
連上、以及哪些事可以不問就做,`conduct` 講怎麼駕駛整支隊伍。安裝方式有兩種。

從 Claude Code marketplace:

```text
/plugin marketplace add cmj0121/baton
/plugin install baton@cmj0121-baton
```

或者用你手上已經有的執行檔,完全不經過 marketplace:

```sh
baton skill install              # 目前目錄底下的 ./.claude/skills(預設,等同 --project)
baton skill install --user       # ~/.claude/skills,你跑的每一個 Claude Code session 都會讀
baton skill uninstall [--user]   # 移除 baton 在那裡裝的東西,其他一概不動
```

兩者提供的文字相同,差別在於它何時改變:`baton skill install` 寫入的是你所執行那支執行檔裡內嵌的版本,所以升級後
重跑一次,技能就和那個 daemon 對齊;而 marketplace 安裝只有在某次 release 把 plugin 的版本號往上調時,才會拿到
技能的變更——Claude Code 只在版本號改變時才更新已安裝的 plugin,同一個版本號底下的新檔案永遠到不了你手上。

只有技能,沒有 MCP 伺服器。兩種方式都不會把 `baton mcp` 接進來。外部 agent 連上時沒有角色、也沒有面板,也就是
cockpit 自己那個不受限的角色:conductor 的圍欄——不准清空佇列、不准清理 worktree、不准切換 peer——對它不適用,
而一個在使用者層級註冊的 MCP 伺服器,會把這份權限交給你開的每一個 Claude Code session,worker 面板也包括在內。
這一步比安裝技能該悄悄做的大得多。`drive` 會叫 agent 先用唯讀的動詞看清楚、做任何破壞性的事之前先問,而且它不靠
任何 MCP,透過預設 socket 上的 `baton ctl` 就能連上 baton。如果你還是想要結構化的工具,就自己選擇加入:

```sh
claude mcp add baton -- baton mcp
```

這會提供 conductor 那整張駕駛隊伍的工具表,而且不受限。

`--project` 會寫進你的 repo。它在目前目錄底下建立 `.claude/skills/{baton,conduct,drive}/` 與
`.claude/skills/.baton-skills` 這份清單,所以它們會出現在 `git status` 上:要讓 repo 上的每個人共用這些技能就
commit 它們,否則就加進 `.gitignore`。每一個在那個 repo 裡啟動的 agent 都會讀到它們——baton 在那裡開的 worker
面板也一樣。指令動手前會先印出它解析出的目錄,而解析到目前目錄之外的會被拒絕:repo 可以把 `.claude` commit
成 symlink,否則 install、`--force` 與 uninstall 就會在它指向的地方寫入與刪除。`--user` 則不管 `~/.claude`
連到哪裡都照著走,因為連進 dotfiles repo 的家目錄是你自己的配置。

worker 面板會被告知退下。worker 若透過專案或使用者層級的安裝找到 `drive`,就可能把自己當成外部角色,而它單純的
`baton ctl` 會撥到不受限的角色。所以 `baton`、`drive` 與 `conduct` 都帶著同一句話:如果設了 `BATON_PANEL_ID`
而 `BATON_ROLE` 不是 `conductor`,你就是 baton 裡的 worker——守好你的任務與 worker 的技能。同樣有
`BATON_PANEL_ID` 的 conductor 仍然適用。

`--user` 會遮住 conductor 自己那份。同名時個人技能優先於專案技能,所以 `~/.claude/skills` 裡的 `baton` 與
`conduct`,才是 conductor 真正讀的那份,而不是 baton 每次 spawn 時寫進它工作區的那份——內容凍結在最後一次安裝
它們的那個 baton 的版本。每次升級後都要重跑 `baton skill install --user`;指令結束時會這樣提醒,而 daemon 在一次
執行中第一次寫 conductor 工作區時,會為每個遮住它的路徑記一行 Warn(重新啟動後會再記一次)。

你自己的技能永遠不會被覆寫。寫入任何東西之前,每一個目標名稱都會先檢查。`.baton-skills` 沒有列出的同名目錄,
或是一個是 symlink、一般檔案而不是目錄的名稱,都算外來的;只要有任何一個外來名稱,整次安裝就會失敗,把它們全部
列出來,什麼都不寫。`--force` 會取代它們:每一個都先被移除——symlink 移除的是連結本身,從不是它指向的東西——再
重新寫入,清單也接手擁有權,所以之後 `baton skill uninstall` 會把它移除。uninstall 只會移除清單上列出、而且是
它安裝的那三個 `baton`、`conduct`、`drive`:`.baton-skills` 若列了別的名稱、不是一般檔案(例如 symlink),或
超過 4 KiB,就什麼都不算數(最後一種會直接報錯)。

## 群組互通

為一個群組打開互通(peers),裡面的面板就能彼此接觸:列出有誰在、讀同群組面板最近的輸出、往同群組面板的終端機
打一行字。它預設關閉,一個群組設一次。由操作者打開;agent 面板與 conductor 會被拒絕使用這個動詞。這道拒絕是
防意外的護欄,不是鎖,見[它不保護什麼](#它不保護什麼)。

```sh
# 你自己,在自己的終端機裡:為某一個群組打開這條通道,再把它關上。
baton ctl group peers review on
baton ctl group peers review off
```

在儀表板上,同一個開關是游標停在該群組那一列時的 `g p`(見 [KEYS.zh-TW.md](KEYS.zh-TW.md))。那個群組裡的
agent 接著就有三個工具可用:

| 工具          | 引數                               | 回傳                                                                                 |
| ------------- | ---------------------------------- | ------------------------------------------------------------------------------------ |
| `baton_peers` | —                                  | 它碰得到的同群組面板,JSON 格式:各自的 `id`、`title`、`kind`、`state`                 |
| `baton_peek`  | `id`、`bytes`(預設 4096,最多 8192) | 那個面板輸出的尾段,純文字,終端機控制序列已移除                                       |
| `baton_tell`  | `id`、`text`                       | 把 `text` 當成一行打進那個面板並按下 Enter;回覆打進去的大小與目標,不代表面板已經照做 |

`baton_peek` 讀的是面板印出內容的尾段,依大小截斷,所以開頭可能落在一行的中間,而全螢幕程式讀起來會是碎片。
結尾符號還沒出現就被截斷的跳脫字串,只會少掉它的引導碼:後面的文字會留下來,不會被吞掉。
沒有東西可顯示的面板會回 `(panel <id> has no output to show)`。

觸及範圍剛好就是一個群組。旗標掛在群組的完整路徑上,對其他路徑什麼都沒說:`backend` 開了互通,
`backend/api` 裡的面板並不是 `backend` 裡面板的 peer,反過來也不是。peer 指的是直接歸在呼叫者自己那個群組裡、
而且還在跑的另一個面板。呼叫者自己、已結束的面板、conductor 與全域 shell 永遠不是 peer;後兩者預設不屬於任何群組,
就算被歸進某個群組也一樣會被拒絕。

誰能切換它。操作者,從座艙,或是在一般終端機、shell 面板裡用 `baton ctl`。conductor 連線的 `group.peers`
開與關都會被拒絕,從 agent 面板裡執行的 `baton ctl group peers` 也一樣:在 agent 之間開一條通道,是在決定
哪些 agent 碰得到哪些終端機,能自己做這個決定的 agent 等於是在授權給自己。群組裡沒有任何面板直接歸在它底下時,
打開會被拒絕;關閉則永遠不會。

daemon 分辨 agent 與操作者,靠的是連線自己宣告的面板 id,而且只有宣告的 id 對得上一個 agent 面板時才會拒絕。
什麼都沒宣告的連線、或宣告了一個不存在的面板的連線,都被當成操作者。所以這條規則擋得住伸手去碰開關的 agent,
擋不住把自己是誰藏起來的 agent。這個限制就是[它不保護什麼](#它不保護什麼)的第一列。

旗標能留多久。它是對群組裡那些面板做的決定,不是對一個名字做的決定,所以:

- 群組最後一個直屬成員離開時,旗標就被丟掉,不論是被改分組、解除分組、關閉還是清除。還留在儀表板上的已結束
  面板算是成員。之後再被拿來用的同一個名字,是從互通關閉開始的;
- 它跟著單純的改名走;一個巢狀群組解散進一個自己沒有面板的父層時,旗標也會帶過去;
- 合併永遠不會把它打開:把一個開著互通的群組改名或解散進一個本來就有面板的群組,目的地維持它原本的樣子,
  你先前打開過就是開著,否則就是關著;
- 被搬進開著互通的群組的面板,從那一刻起就在觸及範圍內:旗標涵蓋的是群組裡當下有誰,不是你設定當時在場的
  那些面板。把面板搬進去跟旗標一樣是操作者的事,見[誰能把面板加進去](#誰能把面板加進去);
- 它跟其餘的隊伍狀態一起存檔,所以撐得過 daemon 重啟。

每一次呼叫都重新決定。開面板時不授予任何東西。這三個工具永遠在 worker 的工具清單裡,不論它的群組有沒有開互通,
而 daemon 依隊伍當下的樣子逐次授權。所以對話到一半旗標被關掉、或面板被搬走,下一次呼叫就會被拒絕,並附上原因。

### 誰能把面板加進去

開著互通的群組裡有誰,決定了誰能往誰那裡打字,所以把它擴大,跟旗標本身一樣被圍起來。conductor 連線,以及宣告的
面板是 agent 的連線,只要指令會把一個既有的面板放進它原本不在的、開著互通的群組,就會被拒絕:

```text
group "review" has peer interaction on; the operator adds panels to it
```

| 對 agent 與 conductor 拒絕                                                 | 不拒絕                                               |
| -------------------------------------------------------------------------- | ---------------------------------------------------- |
| 把 id 用 `panel.group`(`baton ctl group`、`baton_group`)歸進開著互通的群組 | 操作者,從座艙或在 agent 面板之外用 `baton ctl`       |
| 群組改名時合併到一個有面板、開著互通的群組上                               | 把面板重新歸進它本來就在的群組                       |
| 解散(`panel.ungroup`)一個父群組開著互通的群組                              | 搬進沒開互通的群組,或搬出開著互通的群組              |
|                                                                            | 替開著互通的群組本身改名                             |
|                                                                            | 伺服器自己的歸檔:依分支名歸入的 worktree spawn、外掛 |

最後一列是一個真的開口,不是註腳:伺服器自己歸檔的新面板不在柵欄內,所以分支名剛好等於某個開著互通的群組名的
worktree spawn,不論是誰要求的,都會落進那個群組。

被拒絕的是整道指令,不是只有出問題的那幾個 id:做了一半的重新分組,比完全沒做更難看懂。對一支由 conductor 帶的
隊伍,後果值得直接講:conductor 開了一個面板、再用 `baton_group` 分組,沒辦法把它加進開著互通的群組。由你來加,
或者你先把互通關掉、讓 conductor 把群組建好、再打開。跟旗標自己的柵欄一樣,它看的是連線宣告的身分,也有同樣的
[限制](#它不保護什麼)。

### 一次送出做了什麼

`baton_tell` 是往一台真的終端機打字,送到的內容取決於誰在聽:

| 目標                  | 打進去的內容                                                               |
| --------------------- | -------------------------------------------------------------------------- |
| agent 面板            | 一行字:`[from <title> (<id>)]`、一個空白、再接文字,也就是送出者的標題與 id |
| shell 或 command 面板 | 文字原封不動,沒有前綴;shell 會把它當指令執行                               |

- 每一次送出都以 Enter 結尾,也就是目標 profile 的 [`submit`](SPEC.zh-TW.md#面板panels) 鍵,
  沒設就用它 CLI 的內建預設值(grok 與 codex 是 `"\r"`),都沒有才是 `"\n"`。沒有選項可以把文字留在同群組面板的輸入列上不
  送出,送出者也不能挑按鍵。
- agent 先收到文字、短暫停頓,再收到單獨寫入的 Enter,這樣忙碌中的 TUI 才不會把兩者當成貼上。如果停頓期間目標的
  行程變了,Enter 會被丟掉,這次送出失敗:什麼都沒有送出。shell 則是文字與 Enter 一次寫入。
- 處於 bracketed paste 模式的 agent,會把文字當成一次貼上收到。目標的 TUI 開啟了這個模式時(codex、claude 與 grok
  都會開),文字會包在 `ESC[200~` … `ESC[201~` 裡送達,文字裡的標記會先剝掉,Enter 則在停頓之後跟上——這樣 codex
  就不會把 Enter 當成換行,讓長訊息留著不送。見[面板](SPEC.zh-TW.md#面板panels)。
- 回覆說的是 baton 打了什麼,不是面板做了什麼。按下 Enter 的送出會回
  `typed <n> bytes into <title> (<id>) and pressed Enter; not confirmed the panel took it`;要知道面板有沒有收下,
  用 `baton_peek` 讀回來看。
- agent 收到的是一行。Tab 與換行(CR、LF、`U+0085`、`U+2028`、`U+2029`)摺成一個空白,頭尾修掉。其他任何控制
  字元或看不見的字元(跳脫序列、`Ctrl-C`、雙向覆寫、零寬字元、`U+FFFD`)以及不合法的 UTF-8 一律拒絕,而不是
  默默剝掉:peer 是對 agent 說話,不是替它按鍵。前綴裡的送出者標題與面板 id 各自會被清成一行,並截在 64 個 rune。
- shell 收到的是送出時的位元組,控制字元也在內。往 shell 打字本來就是目的。
- 大小。一次送出最多 4096 個位元組,以原文計算;前綴不算在內。
- 速率。每個送出者可以連送三次,之後每兩秒一次。超過上限的送出會被拒絕而不是排隊,拒絕訊息會說要等多久。
- 在等人的面板一概不收。送給處於 `attention` 的面板會被拒絕:agent 不該替另一個 agent 回答權限詢問,
  收件匣仍然是你的。
- 它不是派工。目標身上不會記下任務,卡片上不會出現簡報,也不會跑任何 task hook。

### 線路上

| 動詞          | 指令欄位                       | 回覆                                                                                                                                             |
| ------------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| `group.peers` | `group`、`enable`(`true` 為開) | 廣播給每個客戶端的 `panels` 快照;該群組的項目帶著 `peers`                                                                                        |
| `peer.list`   | —                              | `type: "peers"`,`panels` 裡是呼叫者碰得到的同群組面板                                                                                            |
| `peer.tail`   | `id`、`count`(位元組)          | `type: "tail"`,帶 `id` 與 `data`                                                                                                                 |
| `peer.send`   | `id`、`data`                   | `type: "notice"`,帶 `id`(目標)與 `notice`(`typed <n> bytes into <title> (<id>) and pressed Enter; not confirmed the panel took it`),只回給送出者 |

`peer.*` 動詞的呼叫者,就是那條連線在 `hello` 時宣告的 `self`,沒有欄位可以指名別人。`peer.tail` 回的是目標輸出
未經處理的尾段,跳脫序列也在內:`count` 上限是 8192 個位元組,`0` 表示 daemon 自己的預設視窗。要把它交給模型的
客戶端得先清過,`baton_peek` 就是這麼做的。`peer.send` 會拒絕非空的 `submit`。拒絕是 `type: "error"`,
而每一個 `peer.*` 的拒絕都以 `peer:` 開頭並說出原因:

| 拒絕訊息                                                                                       | 什麼時候                                  |
| ---------------------------------------------------------------------------------------------- | ----------------------------------------- |
| `peer: this connection declared no panel of its own`                                           | 呼叫者沒有宣告 `self`                     |
| `peer: "<id>" is not a live panel`                                                             | 呼叫者的 `self` 不存在或已結束            |
| `peer: the conductor and the global shell are not peers`                                       | 呼叫者自己的面板就是這兩者之一            |
| `peer: conductor role: the peer verbs are for the panels of a group`                           | 連線宣告了 conductor 角色                 |
| `peer: panel "<id>" is in no group`                                                            | 呼叫者不在任何群組裡                      |
| `peer: peer interaction is off for group "<group>"; the operator switches it on`               | 呼叫者的群組沒開互通                      |
| `peer: a target panel id is required`                                                          | `peer.tail` / `peer.send` 沒帶 `id`       |
| `peer: no panel with id "<id>"`                                                                | 目標不存在                                |
| `peer: "<id>" is your own panel`                                                               | 目標就是呼叫者                            |
| `peer: panel "<id>" is not in your group "<group>"`                                            | 目標在別的群組、子群組,或不在任何群組     |
| `peer: panel "<id>" is the conductor, which is never a peer`                                   | 目標是 conductor                          |
| `peer: panel "<id>" is the global shell, which is never a peer`                                | 目標是全域 shell                          |
| `peer: panel "<id>" has exited`                                                                | 目標的行程已經結束                        |
| `peer: peer.send takes no submit sequence; every send ends in Enter`                           | `peer.send` 帶了 `submit`                 |
| `peer: peer.send needs something to send`                                                      | 沒有位元組,或送給 agent 的只有空白        |
| `peer: peer.send carries <n> bytes, over the 4096-byte cap`                                    | 文字太長                                  |
| `peer: text sent to an agent must be valid UTF-8`                                              | 送給 agent 的不是合法 UTF-8               |
| `peer: control bytes are not sent to an agent; send plain text`                                | 送給 agent 的內容有控制字元或看不見的字元 |
| `peer: panel "<id>" is waiting for a human; it takes no peer input until the operator answers` | 目標處於 `attention`                      |
| `peer: sending too fast; retry in <duration>`                                                  | 送出者超過速率上限                        |

速率檢查排在最後,所以因為其他原因被拒絕的送出,不會花掉送出者任何額度。

有一個錯誤發生在文字已經打進去之後,所以不帶 `peer:` 前綴,也照樣算進速率額度:

| 錯誤                                                                                               | 什麼時候                                       |
| -------------------------------------------------------------------------------------------------- | ---------------------------------------------- |
| `typed <n> bytes into <title> (<id>), but its process changed before Enter; nothing was submitted` | 目標 agent 的行程在文字與 Enter 之間結束或重啟 |

`group.peers` 有自己的拒絕,不帶那個前綴。群組名稱最先檢查,打開與關閉都一樣:

| 拒絕訊息                                                                                 | 什麼時候                       |
| ---------------------------------------------------------------------------------------- | ------------------------------ |
| `group.peers needs a group`                                                              | 群組是空的                     |
| `invalid group path "<group>"`                                                           | 名稱不是合法的斜線路徑         |
| `the name is <n> runes long, and the limit is 128`                                       | 名稱太長                       |
| `the name "<group>" contains a control character`                                        | 名稱裡有控制字元或看不見的字元 |
| `group.peers: an agent panel may not change peer interaction; it is an operator setting` | 呼叫者宣告的面板是 agent       |
| `group.peers: no panels in group "<group>"`                                              | 要打開的群組沒有直屬成員       |
| `conductor role: group peer interaction is an operator setting`                          | 連線宣告了 conductor 角色      |

要求的狀態跟群組現在的狀態一樣,不算錯誤,也什麼都不會變:不會觸發 `group.change`,也不會寫記錄。

### 看得見它

開著互通的群組,不論畫的是它還是它的面板,都標著 `⇄`:儀表板上該群組的那一列(收合時取代 `▸`;展開時 `▾` 改成青色)與卡片、群組分割的標題
(`⇄ peers`)、每個成員磚的開頭,以及放大某個成員時的頁腳條(`⇄ peers`,頁腳太窄時只剩符號)。所以你往一個
面板打字的時候,看得到它的同群組面板也能往裡面打。在分組視角下,列、卡片與分割標題是桶而不是群組,不帶標記;
窄到放不下的磚也不帶。每一次放行的送出會在 daemon 記錄檔寫一行(`peer send`,帶 `from`、`to`、`group`、
`bytes` 與 `submitted`,Enter 被丟掉時為 `false`),並觸發帶著同樣五個欄位的 `peer.send`
[外掛事件](PLUGIN.zh-TW.md);切換旗標會記下 `group peers set`,並觸發帶著 `group` 與 `peers` 的 `group.change`。

### 它不保護什麼

互通是你刻意打開的便利,有下面這些限制。上面那些檢查沒有把其中任何一條藏起來。

| 限制                              | 意思                                                                                                                                                                                                                                                                                                                                                                                 |
| --------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 呼叫者是誰是宣告的,沒有驗證       | 旗標與「誰能加入開著互通的群組」這兩條只有操作者的規則,以及每個 `peer.*` 動詞的呼叫者身分,靠的都是連線依 `BATON_PANEL_ID` 宣告的 `self`。agent 把那個變數拿掉或改掉再執行 `baton ctl group peers <group> on`,不會被拒絕,也就能把自己的群組打開,把面板歸進開著互通的群組也是一樣;宣告成另一個面板的 id,就是以那個面板的身分呼叫 peer 動詞。這是防意外的護欄,跟 conductor 的柵欄一樣。 |
| peer 會借用同群組面板的權限       | agent 可以往一個權限比較寬的同群組面板打字(工具白名單比較大、目錄不同、或者是一個 shell),讓它做送出者自己做不到的事。把你願意讓它們共用觸及範圍的面板放在同一組。                                                                                                                                                                                                                    |
| 它是護欄,不是牢籠                 | 群組檢查、上限與前綴只套用在 `peer.*` 動詞上,別的都不管。worker 直接用 `baton ctl send` 打進隊伍裡任何一個面板,跟以前一樣完全沒被擋,而旗標自己的柵欄就是上一列那道軟的;真正拿掉 socket 的仍然是 `isolate: docker`。                                                                                                                                                                  |
| 送出可能落在你打字的中間          | peer 的那一行,是打進你正在打字的同一台終端機,可能跟你的輸入交錯。你接著面板的時候,沒有東西會把它擋下來。                                                                                                                                                                                                                                                                             |
| `[from …]` 前綴只是文字           | 任何能往面板打字的東西,都能打出同樣的字,而 shell 目標根本沒有前綴。它告訴 agent 是誰在說話,不證明任何事。                                                                                                                                                                                                                                                                            |
| 輸出裡印了什麼就帶著什麼          | 同群組面板的尾段可能有它印出來的機密(token、整份 `.env`),讀了就等於放進另一個 agent 的 context。被限制的只有大小。                                                                                                                                                                                                                                                                   |
| `attention` 的拒絕是盡力而為      | 面板的狀態是在監看器的 tick 上設定的,所以送出仍可能落在權限詢問剛畫出來、而 tick 還沒注意到的那個空檔。                                                                                                                                                                                                                                                                              |
| 速率上限管的是送出者,不是一段對話 | 兩個互相回話的 agent,各自都還在自己的上限之內。A→B→A 的迴圈會被放慢到每一邊每兩秒一則,不會被停下來。                                                                                                                                                                                                                                                                                 |
| 狀態檔是被信任的                  | 旗標存在 daemon 的狀態檔(`baton.state.json`)裡。有人手動在那裡寫上 `"peers": true`,下次啟動時只要那個群組裡有面板就會照單全收,不會問任何問題。那個檔案是你自己的,而寫得了它的東西本來就能駕駛整支隊伍。                                                                                                                                                                              |
| 這些工具需要面板的身分            | 呼叫者是 MCP 伺服器行程繼承到的 `BATON_PANEL_ID`。會用清空的環境啟動 MCP 伺服器的 agent CLI 等於沒宣告面板,每個 peer 工具都會被拒絕。                                                                                                                                                                                                                                                |

shell 面板拿不到 `BATON_PANEL_ID`(見[下文](#直接對接線路)),所以實際上 shell 或 command 面板是被 peer 讀、被 peer
打字的對象,自己不會去呼叫 peer 動詞。另外,這些工具是跟著 worker 的 MCP 設定走的:設了
`panel.agent-mcp: false`,agent 就完全拿不到它們。

## 直接對接線路

兩種介面都只是 socket 之上的輕薄包裝——偏好原始 JSON-RPC 的 agent 可以直接講。控制客戶端在 `hello`
握手時宣告自己的身分:

| 欄位   | 含義                                                  |
| ------ | ----------------------------------------------------- |
| `role` | `"conductor"` 表示要被圍上柵欄;留空(座艙)則為全權。   |
| `self` | 客戶端自己的面板 id——伺服器會拒絕讓它對這個面板動手。 |

`panel.attention` 帶著 `reason`(必填)與一個可以留空、表示這條連線自己的 `self` 的 `id`;`panel.resolve` 帶著
同一個 `id`。兩者都以一個錯誤、或以這次變更產生的 `panels` 快照回覆,而面板宣告的理由會經由
`proto.Panel.reason` 回來——如上文所述,那是已經清洗過的。面板在 `attention` 期間,快照也在 `proto.Panel.source`
上說是誰升起它的:`declared`、`tail`(正規表達式)或 `classifier`。

`attention.mark` 帶著一個 `id`(必填),並在 `panel.classifier.record` 開著時把那個面板的尾巴寫成分類器的一筆
正例;它不改任何狀態,並以一則 `notice` 回覆,說一筆資料列有沒有落地。`classifier.status` 什麼都不帶,以一則
`classifier` 訊息回覆:有沒有設定區塊、socket 與 scope、socket 回應時 mold 自己的狀態(連不上時則是撥號錯誤),以及
每個檔案裡有幾筆資料列。它跑的那次探測會把一個被 daemon 放棄的分類器重新上膛。兩者都寫在
[ATTENTION.zh-TW.md](./ATTENTION.zh-TW.md#分類器站在正規表達式旁邊)。

一次 dispatch 多帶兩個欄位:`prompt`(那份簡報)以及一個選用的 `submit` 覆寫值(接在後面用來送出的按鍵;
沒設就用目標 profile 的 [`submit`](SPEC.zh-TW.md#面板panels),再來是它 CLI 的內建預設值(grok 與 codex 是 `"\r"`),
都沒有才是換行),用在
`panel.dispatch` / `panel.dispatch-group`;`task.enqueue` / `task.cancel` / `task.promote` / `task.demote` /
`task.drain` / `task.list` 驅動待辦,並以一份 `tasks` 快照回覆。一次隨需開新的 `task.enqueue` 會帶著
worker 的 `path` / `args` / `dir`,以及一個 `ephemeral` 完成即關的旗標。

Baton 把接線注入 agent 面板的行程,`baton ctl` 與 `baton mcp` 兩者都會自動讀取:

| 變數             | 是                                       | 注入給                       |
| ---------------- | ---------------------------------------- | ---------------------------- |
| `BATON_SOCK`     | 要撥接的控制 socket                      | **每一個 agent 面板**        |
| `BATON_PANEL_ID` | 該面板自己的 id——它宣告的 `self`         | **每一個 agent 面板**        |
| `BATON_ROLE`     | `conductor`——在 hello 時要宣告的限定角色 | **只有 conductor**(柵欄所在) |

每一個 agent 面板都會被告知自己是哪個面板,因為一個叫不出自己名字的控制客戶端,就沒辦法說任何關於自己的事——而那
正是 `attention` 與 `resolve` 的全部重點。被告知並不授予任何權限:空的 `BATON_ROLE` 就是 agent 面板一直以來擁有
的那條不受限的普通連線,所以一個 worker 的觸及範圍,跟注入 id 之前一模一樣。只有 conductor 會拿到那個角色,因為
只有 conductor 被它圍住。

**shell** 面板刻意兩者都不給。shell 是一個啟動器——人在裡面跑的每個程式都會繼承那個標記——而坐在 shell 前面的人
早就有了 agent 所缺的東西:座艙會指出那個面板,而 `--id` 只差一個旗標。`BATON_SOCK` 仍然到得了它,但那是繼承而
不是注入:常駐程式把它釘進自己的環境,而每個面板都是從那裡起跑的。

## 護欄

conductor 角色由伺服器端強制執行,早在任何指令生效之前。它以那道**僅限本 uid 的 socket** 上自行宣告的角色
為依據——這是一道防範 agent 意外的護欄,而非安全邊界(你這個使用者的任何本機行程,永遠都能直接對 socket 說話)。

| conductor 可以做                       | conductor 不可以做                          |
| -------------------------------------- | ------------------------------------------- |
| list、spawn、group、rename、pin、move  | 關閉、送訊號,或送輸入到**它自己的**面板     |
| 對**其他**面板送訊號與送輸入           | **把任務 dispatch 給它自己的**面板          |
| dispatch 給其他面板、把任務排入佇列    | **清空佇列**——把待辦清光是操作者專屬        |
| 列出 baton 開過的 worktree             | 清掃它們——把樹從磁碟上刪掉是操作者專屬      |
| 讀面板的標題、狀態與遙測               | **把面板記錄到檔案**、或讀回記錄檔——見下    |
| 問 `classifier.status`                 | 把畫面標為等待——收件匣的動詞,見下           |
| 重新排序已排入的任務(promote / demote) |                                             |
| **舉起它自己的手**、以及把手放下       | 舉起或放下**另一個面板的**手                |
| 關閉其他面板、清除已結束               | 重載或停掉伺服器                            |
|                                        | 開新的速度超過速率上限,或超過隊伍上限(64)   |
|                                        | 在產生面板時**指名 agent profile**——見下    |
|                                        | 切換群組的互通旗標,或使用任何 `peer.*` 動詞 |
|                                        | 把面板加進開著互通的群組,見下               |

worktree spawn 也是 spawn,所以它動用的是同一個錢包裡的同兩道上限:隊伍上限與速率上限計算 `spawn --worktree`
的方式和計算 `spawn` 完全一樣,conductor 沒辦法靠換一個動詞繞過另一個。

**隊伍上限不是一條 conductor 規則**——它是那張表裡唯一不是的一行。它數的是面板,不是發問的人,所以座艙自己按下
`p`,在第六十四個上會被拒絕,和 conductor 的 `spawn` 一模一樣,外掛的 `baton.spawn` 也一樣。一道有某隻手能繞過
的上限就不成其為上限:面板一旦被用掉就是被用掉了,不管是誰開的,而下一個來問的 agent 會為了一件不是它做的事被
拒絕。速率上限才是關於誰在問的那一半——它的存在是為了攔住迴圈——而操作者從來不必付它,因為坐在鍵盤前的人不會
跑迴圈。

一次撞到隊伍上限的 spawn 會告訴你怎麼辦:關掉一個面板,或者把已結束的清掉。清掉通常是比較便宜的那一個,因為
已結束的面板在被丟掉之前仍然佔著它的位子——一支看起來只用了一半的隊伍,可能正好貼在上限上。

**排入工作不等於開面板,待辦也一樣不是繞過速率上限的路。** 會自己備妥 agent 的工作(`enqueue` 帶 agent 指令)
一律放行——花一分鐘把待辦填滿本來就是待辦的用途,而 conductor 排入時並沒有在開面板。真正在開面板的是排程器,
它晚一步、在自己的 tick 上動作,而付帳的也是它,動用的是 `spawn` 那同一個錢包。它**每個 tick 只備妥一個新
agent**,所以十筆待辦是花十秒鐘陸續排掉,而不是一次全開;其餘的工作是排隊等下一輪,而不是被拒絕。共用同一個
錢包買到的是另一半:剛被要求慢下來的 conductor,沒辦法在同一瞬間讓佇列替它生出一個面板;而排程器剛備妥的那
一個面板,也會讓 conductor 下一次 `spawn` 排在它後面。

所以 conductor 能填滿待辦並從中 dispatch,卻無法把它抹除;而佇列也給不了它繞過自我柵欄的路子:它排入的一份簡報,
會被排程器抽取分派給 _其他_ 空閒的 agent,絕不會回到它自己身上。

那道柵欄擋的是 conductor 對自己做**破壞性**的事——關閉、送訊號、灌輸入。說「我需要一個決定」是相反的事情,所以
`panel.attention` 與 `panel.resolve` 是唯一一對反過來圍的動詞:conductor 可以舉起與放下**它自己的**手,而且只有
它自己的。允許它這麼做,是因為 conductor 是伺服器永遠握有身分的那一個 agent,而拒絕給它一個「為 agent 而存在」
的動詞,對一個為 agent 打造的控制平面來說是件很奇怪的事。限制它只能對自己,則是因為一份宣告會讓它的面板離開排程器
的空閒池、直到有東西把它收回——一個能對整支隊伍舉手的 conductor,就是一個能靠著迴圈呼叫、一次一個地把所有其他
agent 的待辦凍住的 conductor。

面板記錄(`C-t l` / `C-t L`,[LOGGING.zh-TW.md](LOGGING.zh-TW.md))兩個方向都直接拒絕。`panel.log` 是在要求
daemon **以你的身分、在它自己的主機上寫檔案**,而那正是遠端動作早就被圍起來的那種請求;`panel.logview` 則會把
另一個面板的逐字稿交給 agent 慢慢讀,而那正是收件匣的 `panel.tail` 柵欄要擋住的那個面。日後要打開其中任何一個,
都只是刪掉一行,而介面上的空間就是為此保留的。`attention.mark` 跟 `panel.tail`、`panel.ack` 圍在同一道柵欄裡,因為
它是收件匣的動詞,而收件匣是操作者的;`classifier.status` 對 conductor 開放,跟 `worktree.list` 一樣,因為「這支隊伍
的注意力是不是只靠正規表達式」是一個驅動隊伍的 agent 可以問的、關於隊伍的事實——而靠發問來把分類器重新上膛,不是
操作者專屬的動作。

[群組互通](#群組互通)並沒有把那個面對 conductor 打開。`peer.tail` 跟 `panel.tail` 是同一種讀取,所以三個
`peer.*` 動詞在檢查任何其他條件之前,就先依角色被拒絕:conductor 不是任何群組的面板,而且它本來就透過自己那張表駕駛
每一個面板。`group.peers` 跟它們一起被拒絕,開與關都一樣。哪些 agent 可以讀哪些終端機、往哪些終端機打字,是
操作者的決定;conductor 把你打開的通道關上,是同一個決定被不對的一方做了。互通多出來的,是你指名的那一個群組裡
worker 與 worker 之間的觸及範圍,它自己的限制列在[介紹它的地方](#它不保護什麼)。

conductor 也不能把它擴大。`panel.group` 在其他地方對它都是開放的,在這裡也開放的話,conductor 就能把隊伍裡任何
一個面板拉進一個已經打開的群組,等於把操作者的決定一個面板一個面板地做掉。所以把面板歸進開著互通的群組、把群組
改名合併到上面、把子群組解散進去,都會以 `group "<group>" has peer interaction on; the operator adds panels to it`
被拒絕,而且是整道指令一起。因此 conductor 建了一個 worker 再用 `baton_group` 歸檔時,沒辦法把它放進開著互通的
群組:由你來加,或在 conductor 建群組的期間先把互通關掉。agent 面板的連線也受同一條規則約束;完整清單在
[誰能把面板加進去](#誰能把面板加進去)。

由 conductor 發動的產生會被**清掉 profile 名字**,所以它建立的面板一律解析到全隊的[資源上限](LIMITS.zh-TW.md),
而不是任何 profile 自己的那組。名字正是面板上限解析的依據,所以「能指名 profile 的 agent」就等於「能替自己指出
比全隊更寬鬆上限的 agent」。

Score 編輯器(`n s`,[SCORE.zh-TW.md](SCORE.zh-TW.md))基於同樣的理由被拒絕,而這裡的區別值得說精確:被圍起來的
**不是記憶**。`score.submit` 是刻意不設閘的——記憶本來就由 agent 和操作者一起餵——讀取類的動作也對任何人回答。
被圍起來的是**編輯器**:一個在 daemon 自己的主機上、以你的身分執行的互動式程式,這正是 `panel.log` 的形狀。
它還多了一道 `panel.log` 沒有的邊:回覆交回來的是一個不等於 conductor 自己的暫時面板 id,所以自我圍籬蓋不到它,
`panel.input` 就能驅動那個編輯器——而值得設進 `$EDITOR` 的編輯器沒有一個不能開 shell。這並沒有拿走什麼:
想改記憶的 agent 有 `score.submit`,想修正它的 conductor 有那三個 refine 動詞。

git 選單的 **commit** 也一起,而且只有 commit。`panel.git commit` 會解析成 `sh -c "git add -A && git commit"` 並注入
`GIT_EDITOR`——同一台主機上的同一個活編輯器——而且和 score 編輯器一樣,它回覆的是一個不等於 conductor 自己的暫時
面板 id,所以自我圍籬蓋不到它。其他每一個 git 動作都維持開放,因為這裡畫的線是**在主機上開互動式程式**,不是
**碰到 git**:status、log、diff、add、push、branch 與 worktree-list 回覆的是擷取下來的文字、不留下任何東西,而
conductor 讀得到 repo 的狀態正是這個角色存在的意義。`worktree-add` 又是第三種情況——一個披著 git op 名字的
spawn——所以它是**計入 spawn 上限**而不是被拒絕,這正是讓 conductor 仍然能隔離工作、但不能無節制地做的機制。

這一切都不是權限提升,也不該被那樣解讀。conductor 本來就能透過 `panel.create` 和 `panel.input`、在 spawn 上限之下
拿到主機上的 shell,因為驅動艦隊正是這個角色的本分。`commit` 的問題在於它既沒有被計量,也和旁邊那兩個動詞不一致。

一個純座艙連線不宣告任何角色,也永遠不會被圍上柵欄。agent 面板的連線同樣不宣告任何角色,而它因為是 agent
才被拒絕的事有兩件:`group.peers`,以及把面板加進開著互通的群組。
