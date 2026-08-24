# Baton — Control

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
  自動把它讀成專案指示)以及一個 `.mcp.json`。如此一來,這個 agent 阻力最小的路徑就是去驅動 baton,
  而不是四處翻你的程式碼。
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

### 操作者簡報 — `$HOME/.baton/CONDUCTOR.md`

內建的入門說明告訴 conductor _如何_ 驅動 baton;而 _該做什麼_ 由你來說。在 `$HOME/.baton/CONDUCTOR.md`
寫下一個目標與指引,baton 就會在每次 conductor 被開啟或重跑時,把它接在 conductor 簡報的 **Operator's brief**
標題底下——所以編輯這個檔案再重跑 conductor(對已結束的按 `n C`,或在你從放大畫面停掉一個運行中的之後),
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
| `baton ctl tree [--json]`                                          | 畫出行程樹(group → panel → OS 子行程),附 CPU%/RSS          |
| `baton ctl spawn [--agent CMD] [--arg A] [--dir D]`                | 開一個面板(有 `--agent` 就是 agent,否則是 shell);印出新 id |
| `baton ctl send <id> <text> [--no-enter]`                          | 把文字打進某個面板;除非 `--no-enter`,否則以換行送出        |
| `baton ctl attention --why <text> [--id ID]`                       | 說這個面板需要人,以及為什麼——見 [舉手](#舉手)              |
| `baton ctl resolve [--id ID]`                                      | 說那個理由已經過去了;面板離開待辦                          |
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
| `baton ctl conductor reset`                                        | 刪掉 conductor 的工作區,讓下一個從乾淨狀態開始             |

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
以及一個讀最後一行輸出有沒有問句的**啟發式**。宣告是唯一一個來自「被描述的那個東西自己」的訊號,所以它贏——狀態
見[生命週期](./SPEC.zh-TW.md#生命週期),完整優先序與一隻舉起的手會落進的那份待辦見
[ATTENTION.zh-TW.md](./ATTENTION.zh-TW.md)。具體來說:

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

## 直接對接線路

兩種介面都只是 socket 之上的輕薄包裝——偏好原始 JSON-RPC 的 agent 可以直接講。控制客戶端在 `hello`
握手時宣告自己的身分:

| 欄位   | 含義                                                  |
| ------ | ----------------------------------------------------- |
| `role` | `"conductor"` 表示要被圍上柵欄;留空(座艙)則為全權。   |
| `self` | 客戶端自己的面板 id——伺服器會拒絕讓它對這個面板動手。 |

`panel.attention` 帶著 `reason`(必填)與一個可以留空、表示這條連線自己的 `self` 的 `id`;`panel.resolve` 帶著
同一個 `id`。兩者都以一個錯誤、或以這次變更產生的 `panels` 快照回覆,而面板宣告的理由會經由
`proto.Panel.reason` 回來——如上文所述,那是已經清洗過的。

一次 dispatch 多帶兩個欄位:`prompt`(那份簡報)以及一個選用的 `submit` 覆寫值(接在後面用來送出的按鍵,
預設為換行),用在 `panel.dispatch` / `panel.dispatch-group`;`task.enqueue` / `task.cancel` / `task.promote` /
`task.demote` / `task.drain` / `task.list` 驅動待辦,並以一份 `tasks` 快照回覆。一次隨需開新的 `task.enqueue`
會帶著 worker 的 `path` / `args` / `dir`,以及一個 `ephemeral` 完成即關的旗標。

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

| conductor 可以做                       | conductor 不可以做                        |
| -------------------------------------- | ----------------------------------------- |
| list、spawn、group、rename、pin、move  | 關閉、送訊號,或送輸入到**它自己的**面板   |
| 對**其他**面板送訊號與送輸入           | **把任務 dispatch 給它自己的**面板        |
| dispatch 給其他面板、把任務排入佇列    | **清空佇列**——把待辦清光是操作者專屬      |
| 讀面板的標題、狀態與遙測               | **把面板記錄到檔案**、或讀回記錄檔——見下  |
| 重新排序已排入的任務(promote / demote) |                                           |
| **舉起它自己的手**、以及把手放下       | 舉起或放下**另一個面板的**手              |
| 關閉其他面板、清除已結束               | 重載或停掉伺服器                          |
|                                        | 開新的速度超過速率上限,或超過隊伍上限(64) |
|                                        | 在產生面板時**指名 agent profile**——見下  |

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
都只是刪掉一行,而介面上的空間就是為此保留的。

由 conductor 發動的產生會被**清掉 profile 名字**,所以它建立的面板一律解析到全隊的[資源上限](LIMITS.zh-TW.md),
而不是任何 profile 自己的那組。名字正是面板上限解析的依據,所以「能指名 profile 的 agent」就等於「能替自己指出
比全隊更寬鬆上限的 agent」。

一個純座艙連線不宣告任何角色,也永遠不會被圍上柵欄。
