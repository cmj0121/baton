# Baton

> 一個可擴充、對 agent 友善的終端機多工器。

[![Release](https://img.shields.io/github/v/release/cmj0121/baton)](https://github.com/cmj0121/baton/releases/latest)
[![License](https://img.shields.io/github/license/cmj0121/baton)](../LICENSE)
[![CI](https://github.com/cmj0121/baton/actions/workflows/ci.yml/badge.svg)](https://github.com/cmj0121/baton/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/cmj0121/baton/branch/main/graph/badge.svg)](https://codecov.io/gh/cmj0121/baton)

[English](../README.md) · **繁體中文** · [日本語](README.ja.md) · [한국어](README.ko.md) ·
[Français](README.fr.md) · [Deutsch](README.de.md) · [Español](README.es.md)

同時跑好幾個 AI coding agent?場面很快就會失控——一堆視窗要顧、session 散落在各個分頁,也沒有一個地方
能一眼看出誰在忙、誰卡住了、誰在等你回覆。

Baton 之於 AI agent,就像 tmux 之於 shell。它給你**一個純鍵盤操作的座艙**:一塊即時儀表板,列出每一個
agent,依所屬任務分組,任何一個都只差一個按鍵。

指揮棒在你手上,agent 們負責演奏,你來指揮。🎼

![Baton 座艙示範——先叫出鍵表、開面板、開啟 conductor、把兩個併成工作項目,再於分割畫面與 zoom 裡按同一個 ? 鍵](assets/baton-demo.png)

_一個鍵就走完整趟:`?` 列出你當下所在畫面的按鍵。開面板、用 `n C` 叫出 conductor、`g g` 再 `g c` 把兩個併成
工作項目——然後在分割畫面裡再按一次 `?`、在 zoom 裡按 `C-t ?`,是三張不同的表。_

_影片由 [`baton-demo.tape`](assets/baton-demo.tape) 產生;片中 conductor 的 agent CLI 是替身
([`demo-agent.sh`](assets/demo-agent.sh)),好讓任何機器都錄得出同一支影片,而它透過 socket 驅動的隊伍是真的。_

## 開始使用

Baton 是單一的靜態執行檔。在 macOS 上,用 [Homebrew](https://brew.sh) 裝它:

```sh
brew install cmj0121/tap/baton
```

在 Linux 上,一行就好:

```sh
curl -fsSL https://raw.githubusercontent.com/cmj0121/baton/main/scripts/install.sh | sh
```

……或者不分平台,都能用 [Go](https://go.dev) 1.26+ 取得它:

```sh
go install github.com/cmj0121/baton/cmd/baton@latest
```

……或從 clone 出來的原始碼用 `make install` 建置。接著直接執行:

```sh
baton
```

Baton 會啟動它的背景伺服器,並把你帶到**儀表板**——你的大本營。你的第一分鐘:

1. 按 **`A`** 開一個 agent(你會替它挑一個工作目錄)。
2. 按 **`enter`** 放大進去看它工作;**`C-t d`** 把你帶回儀表板。
3. 按 **`q`** 卸離走人——一切都繼續在跑。隨時用 `baton` 回來。

迷路了?**`?`** 永遠會顯示你當下所在畫面的按鍵。

## 為什麼不直接用 tmux?

因為 tmux 根本不知道 pane 裡裝的是什麼。它給你視窗,而「哪個是哪個」得由你自己記;要發現某個 agent 一直在等你,
只能一個一個切過去看。Baton 假設 pane 裡裝的是一個 agent,其餘一切都從這裡長出來:

| 你正在做的事       | 用 tmux 手動            | Baton                                                               |
| ------------------ | ----------------------- | ------------------------------------------------------------------- |
| 找出誰在等你       | 一個一個切過去看        | 每個面板一個即時狀態,加上一個 `C-t a` 收件匣,裡面是停下來等人的那些 |
| 把相關的事放在一起 | 自己命名視窗,自己記規則 | 工作項目——一組具名的面板,兩個鍵就併好                               |
| 把工作派出去       | 自己一個一個 pane 打字  | 派一份任務給一個或整組,或讓 conductor 這個 agent 替你驅動整隊       |
| 擋住失控的建置     | 沒有                    | CPU、記憶體與行程數上限,而且管的是面板整棵行程樹                    |
| 知道整隊花了多少   | 沒有                    | 計費窗口的 token 與成本、你的額度進度條,而且能歸屬到面板            |

Baton 不是 tmux 的替代品,也不想接管你的 shell——你如果活在 tmux 裡,就把它跑在 tmux 裡。

## 概念

- **是 agent,不是 shell。** 工作的單位是一個正在跑的 agent,而不是一個要你顧的視窗。
- **是儀表板,不是視窗。** 一次看到全部的即時總覽,而不是一堆分頁。
- **無頭核心、可替換的前端。** 大腦是背景常駐程式;把它畫出來的那張臉是可以替換的。

| 概念             | 是什麼                                                                                            |
| ---------------- | ------------------------------------------------------------------------------------------------- |
| **Panel**        | 一個即時終端機——_agent_ 面板(一支 agent CLI)或 _shell_ 面板。                                     |
| **Work item**    | 一組具名、同屬一項任務的面板。                                                                    |
| **Task**         | 你派給 agent 的一份簡報——全程追蹤其生命週期,必要時排隊與排程。                                    |
| **Conductor**    | 一個替你驅動整支隊伍的 agent——透過 socket 開面板、分組、對其他面板下提示。                        |
| **Global shell** | 伺服器持有的單例純宿主 shell,固定開在 `$HOME`,永遠一個按鍵之遙——是個「主基地」,而非隊伍的驅動者。 |

## 畫面

你透過三種畫面來操作 Baton,彼此之間一個按鍵就能切換:

- **儀表板(Dashboard)** — 任務中樞。小艦隊會畫成一格格**卡片**:每個面板一張、每個工作項目一張;頂層滿六個東西之後
  就換成一棵列出每個面板的即時**樹狀圖**:工作項目是一列,它的子群組縮排在底下,面板又在那些之下。`space` 在任何
  深度都能展開或收合那一列底下的巢狀內容,`→` 展開工作項目並走進去,`←` 收合並退回外層——在卡片上這兩顆則是在卡片
  之間移動。`v l` 手動切換兩種版面。列上會隨終端機寬度依序帶上狀態、工作目錄、輸出火花線與派工內容;`v p` 可以在旁邊叫出詳細窗格。你在這裡導覽、開關面板,並把它們併成工作項目。
- **群組(Group)** — 一個工作項目的即時分割畫面:它的面板並排鋪開,全部同時串流。前幾個以即時磚(tile)串流,
  其餘的收摺成單一的**摘要磚**,可再放大進去。釘選(pin)幾個讓它們常駐;用 **`i`** 就地操作聚焦的那個,
  或按 **`enter`** 進入它。
- **放大(Zoom)** — 單一面板成為你唯一的終端機。按鍵直接送進程式;領導鍵 **`C-t`** 是你採取動作或退回上一層的方式。

## 按鍵

按鍵是**分模式(modal)的**:在儀表板與群組裡,每個動作是單一按鍵;在放大或互動時,你的按鍵驅動程式,
所以一個 Baton 動作是領導鍵 **`C-t`** 再接該按鍵。按 **`?`** 看目前畫面完整、可重新綁定的清單,
按 **`C-t k`** 編輯按鍵對應。
有四個鍵是中繼鍵,它們自己不做事,只打開一個家族——`n` 開啟、`v` 畫面、`g` 分組、`x` 是自己確認自己的連按兩下
——狀態列會列出每一個接下來能收哪些鍵。

| 位置       | 按鍵                  | 作用                                                 |
| ---------- | --------------------- | ---------------------------------------------------- |
| `C-t` 之後 | `d` / `b`             | 跳到儀表板 / 退回上一層                              |
|            | `a`                   | 注意力收件匣——清掉需要人的那些                       |
|            | `[`                   | 進入捲動模式                                         |
|            | `l` / `L`             | 把面板記錄到檔案 / 讀回該記錄檔                      |
|            | `R` / `S`             | 重載設定 / 強制重啟伺服器                            |
|            | `q`                   | 卸離(伺服器繼續執行)                                 |
| 儀表板     | `jk` / `↑↓`           | 移動游標                                             |
|            | `hl` / `←→`           | 移動一張卡片·樹狀時收合／展開工作項目                |
|            | `space`               | 展開／收合這一列底下的巢狀內容                       |
|            | `v p` / `v g`         | 詳細窗格／切換分組視角:工作項目、目錄、profile、狀態 |
|            | `v l`                 | 儀表板版面:卡片或樹狀                                |
|            | `m`                   | 抓起一列——方向鍵搬運,`enter` 放下                    |
|            | `enter`               | 開啟 / 放大所選                                      |
|            | `p` / `A` / `n c`     | 新增 shell / agent / 挑指令面板                      |
|            | `n .`                 | 在聚焦面板的目錄開新 shell 面板                      |
|            | `n C`                 | 開啟 conductor(替你驅動整隊的 agent)                 |
|            | `n h`                 | 開啟 global shell(開在 `$HOME` 的宿主 shell)         |
|            | `w` / `x x`           | 關閉所選 / 清除已結束                                |
|            | `r`                   | 重跑焦點下已結束的面板                               |
|            | `g g` / `g c` / `g u` | 標記 / 把已標記的併組 / 解除群組                     |
|            | `s` / `f` / `D`       | 對所選送訊號 / 尋找 / diff                           |
|            | `/`                   | 搜尋每個面板的輸出(grep 整支隊伍)                    |
|            | `T` / `Q`             | 派任務 / 管理任務佇列                                |
|            | `v u`                 | 切換用量頁尾:關閉／計費窗口／聚焦面板／額度          |
|            | `v U`                 | 帳號用量 — 額度進度條,以及誰在消耗                   |
|            | `v k`                 | 切換狀態列上的按鍵提示                               |
| 群組       | `tab`                 | 聚焦下一個面板                                       |
|            | `+` / `-`             | 多顯示 / 少顯示即時磚                                |
|            | `L`                   | 輪替磚的版面配置                                     |
|            | `p` / `i`             | 釘選 / 與聚焦面板互動                                |
|            | `enter`               | 放大聚焦的面板                                       |
| 放大       | 打字                  | 直接驅動程式                                         |
|            | `C-t f` / `C-t G`     | 搜尋捲動歷史 / git 選單(agent)                       |

完整的按鍵參照見 **[docs/KEYS.md](KEYS.zh-TW.md)**;每個畫面背後的設計見 **[docs/SPEC.md](SPEC.zh-TW.md)**。

## 功能

五件終端機多工器做不到的事:

- **不必輪詢的注意力** — 一支隊伍大多時候都好好的;你會盯著螢幕,是為了不好好的那幾個。一支安靜計時器替每個面板
  排序——`running`、十秒後的 `idle`、agent 做完一輪的 `done`、拖太久的 `stuck`——而 agent 也可以自己舉手,凌駕
  整道階梯。`C-t a` 在任何畫面都能打開收件匣,待辦就在那裡清掉;`settings.notify` 會在沒人看著時送出 OSC 9 桌面
  通知,會併攏,而且永遠不為 `done` 而響。見 **[docs/ATTENTION.md](ATTENTION.zh-TW.md)**。
- **一個 conductor** — `n C` 開啟一個替你驅動整隊的 agent:它透過 socket——經由 `baton ctl` 或 `baton mcp`
  工具——開面板、分組、送訊號、對其他面板下提示,並圍上柵欄讓它無法搞壞自己的宿主。在 `$HOME/.baton/CONDUCTOR.md`
  設定它的目標。見 **[docs/CONTROL.md](CONTROL.zh-TW.md)**。
- **任務與待辦佇列** — `T` 把一份簡報派給某個 agent,或散發給整個工作項目;它記在卡片上,待 agent 就緒時送達。
  `Q` 管理一份持久化的待辦佇列,由伺服器自有的排程器抽取分派給空閒的 agent。`task.pre` 這個 Lua hook 可以改寫或
  否決一份簡報;`task.change` 則監看它。
- **管到整棵行程樹的上限** — 限制一個面板能用多少 CPU、記憶體、行程數,而且管的是它整棵行程樹,讓失控的建置沒
  辦法把整台機器一起拖下水。全隊底線加上 per-agent 覆寫,`C-t R` 把新上限套用到正在跑的隊伍,Linux 上以 cgroup
  v2 強制——主機若無法強制,面板會直說。見 **[docs/LIMITS.md](LIMITS.zh-TW.md)**。
- **能歸屬到面板的用量** — `v u` 循環切換頁尾讀數:計費窗口的 token 用量與成本加上倒數
  (`⊙ 1.2M tok · ≈$12.34 API · ⏳ 2:14:31`)、聚焦面板佔該窗口的比例,或帳號的 rate-limit 進度條
  (`⊙ 5h ▓▓▓▓▓░░░ 2:14:31 · 7d ▓▓▓░░░░░ 3d4h`)。`v U` 打開完整畫面——每個額度窗口、額外點數,以及正在消耗
  它們的面板。見 **[docs/USAGE.md](USAGE.zh-TW.md)**。

另外四件,多數多工器也沒有:

- **容器隔離** — 以 agent profile 為單位選擇性開啟:`isolate: docker` 讓該 profile 的面板跑在容器裡,並把你的
  worktree 掛進去。Image 由你指定(Baton 不提供);`mount`、`network`、`env-allow` 與 `user` 決定還有什麼能過去,
  而你環境裡的東西除非點名否則一律不過去。預設關閉,而且不是對付惡意 agent 的邊界。
  見 **[docs/ISOLATION.md](ISOLATION.zh-TW.md)**。
- **grep 整支隊伍** — `/` 一次搜尋每個面板的輸出,並把命中依面板分組列出;`enter` 放大你挑的那個,直接停在命中處。
  `C-t f` 以正規表示式搜尋單一面板的捲動歷史,捲動模式(`C-t [`)透過 OSC 52 選取並複製,所以在 SSH 上也能用,
  不需輔助執行檔。
- **Agent 後端** — Baton 認得一份 agent CLI 名冊(`claude`、`codex`、`gemini`、`aider`、`opencode`、`grok`),並
  偵測隊伍執行的那台機器上實際裝了哪些。`A` 產生你挑的那一個;`C-t P` 設定整隊的預設值,並列出這台機器沒有的那些
  以及各自要去哪裡取得;裝好之後按 `C-t R` 重新偵測。要加自己的——指令、引數、上限或容器——寫在 `panel.agents` 底下。
- **遠端連線** — `baton --remote` 把同一個座艙接上另一台機器上的隊伍,走的是你本來就在用的 ssh:不開監聽埠、
  不帶 TLS、也沒有 Baton 自己發明的金鑰交換。預設關閉;`C-t @` 打開它、產生一組永遠不寫進磁碟的 passkey,並列出
  每一條連線,可以踢人、換碼或關閉。見 **[docs/REMOTE.md](REMOTE.zh-TW.md)**。

以及一個多工器該有的座艙,每一項都只差一個按鍵:

| 功能         | 按鍵            | 做什麼                                                                                  |
| ------------ | --------------- | --------------------------------------------------------------------------------------- |
| Diff         | `D`             | 該 agent 面板的工作樹 diff——已暫存與未暫存一次看,含未追蹤檔                             |
| Git          | `C-t G`         | diff、log、status、暫存、commit、push、分支與 worktree——**[docs/GIT.md](GIT.zh-TW.md)** |
| 訊號         | `s`             | 對所選、聚焦的磚、或整個群組送出任何訊號                                                |
| 尋找         | `f`             | 依標題或群組過濾整隊                                                                    |
| 群組版面     | `+` `-` `L`     | 多少成員以即時磚串流,以及分割畫面的形狀                                                 |
| Global shell | `n h`           | 伺服器持有的單一純宿主 shell,固定開在 `$HOME`,永遠一個按鍵之遙                          |
| 記住工作目錄 | `n .`           | 面板從 OSC 7 學到自己目前的目錄——**[docs/RESTART.md](RESTART.zh-TW.md)**                |
| 面板記錄     | `C-t l` `C-t L` | 把面板輸出導向檔案,再讀回來——**[docs/LOGGING.md](LOGGING.zh-TW.md)**                    |
| 持久化       | `r`             | 隊伍跨重啟留下來,成為你可以依保留規格重跑的已結束空位                                   |
| 重啟策略     | —               | `panel.restart: on-failure` 讓面板帶著退避與上限自己回來                                |
| 熱重載       | `C-t R`         | 不重啟整隊就重載設定——或對常駐程式送一個 `SIGHUP`                                       |
| 外觀         | —               | 主題與自訂分割網格寫在 `$HOME/.baton/TUI.yaml`——**[docs/TUI.md](TUI.zh-TW.md)**         |
| 螢幕保護     | —               | 座艙靜下來時的一整面數位雨——**[docs/TUI.md](TUI.zh-TW.md)**                             |
| 滑鼠         | —               | 預設關閉,好讓終端機自己的選取仍可用                                                     |
| 語言         | —               | 按鍵清單可讀英文或繁體中文——**[docs/TUI.md](TUI.zh-TW.md#語言)**                        |

## 架構

一個無頭的 **baton server**(背景常駐程式)掌管所有狀態與每一個終端機。可插拔的前端透過單一 Unix domain
socket 接上——指令往上、事件往下——所以你卸離再重新接上都不會漏掉任何東西。

完整的圖與互動模型見 **[docs/SPEC.md](SPEC.zh-TW.md)**。

## 外掛(Plugins)

單一一個 Lua 檔(`$HOME/.baton/plug-in.lua`)就能把 Baton 重塑成你的工作流:對生命週期事件做出反應
(agent 需要你時提醒你、某個完成時串起下一步)、驅動整隊、加入你自己的指令、設定組態——全部透過一個
`baton` 物件。見 **[docs/PLUGIN.md](PLUGIN.zh-TW.md)**。

## 文件

- **[docs/SPEC.md](SPEC.zh-TW.md)** — 完整規格:畫面、面板生命週期、工作項目、訊號、diff、持久化、
  逐畫面按鍵參照,以及架構圖。
- **[docs/ATTENTION.md](ATTENTION.zh-TW.md)** — 規模化的注意力:安靜梯(`done`、`stuck`、failed)、`C-t a`
  收件匣、儀表板的兩種摺疊、桌面通知,以及它們接受的每一個旋鈕。
- **[docs/TUI.md](TUI.zh-TW.md)** — 座艙外觀檔(`$HOME/.baton/TUI.yaml`):色彩主題與群組分割的版面配置
  (預設與自訂網格)。
- **[docs/LIMITS.md](LIMITS.zh-TW.md)** — 資源上限:設定寫法、兩層疊加、熱重載,以及它們實際在哪裡被強制。
- **[docs/ISOLATION.md](ISOLATION.zh-TW.md)** — 容器隔離:per-profile 設定、agent 保留了什麼、上限在容器裡怎麼被強制,以及它不是什麼邊界。
- **[docs/RESTART.md](RESTART.zh-TW.md)** — 重啟策略:什麼算失敗、什麼不算,退避與上限,以及為什麼沒有 `always`。
- **[docs/GIT.md](GIT.zh-TW.md)** — git 選單:每個操作、commit 編輯流程、worktree,以及設定。
- **[docs/LOGGING.md](LOGGING.zh-TW.md)** — 面板記錄:寫進去的是什麼、檔案落在哪裡、session 標記、輪替,
  以及它不是什麼邊界。
- **[docs/REMOTE.md](REMOTE.zh-TW.md)** — 透過 SSH 遠端連線:`--stdio` 橋接、passkey 是什麼與不是什麼、`C-t @`
  的連線清單,以及它會回報的失敗。
- **[docs/USAGE.md](USAGE.zh-TW.md)** — 帳號用量頁尾:本機與 Admin-API 兩種來源、設定,以及注意事項。
- **[docs/PLUGIN.md](PLUGIN.zh-TW.md)** — Lua 外掛 API:`baton` 物件、事件、指令,以及設定。
- **[docs/CONTROL.md](CONTROL.zh-TW.md)** — 以 agent 驅動整隊:conductor、`baton ctl` CLI、`baton mcp`
  工具,以及各種護欄。
- **[docs/SCORE.md](SCORE.zh-TW.md)** — Score,隊伍的記憶:`score.md` 這個檔案與它唯一的復原手段、階梯、
  排序權重、壓縮,以及它不是什麼邊界。

## DDD(Dream-Driven Development,夢想驅動開發)

本專案奉行 DDD(夢想驅動開發):每一項功能都源自我所夢想、所需要的東西。
