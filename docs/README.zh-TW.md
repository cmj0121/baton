# Baton

> 一個可擴充、對 agent 友善的終端機多工器。

[![CI](https://github.com/cmj0121/baton/actions/workflows/ci.yml/badge.svg)](https://github.com/cmj0121/baton/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/cmj0121/baton/branch/main/graph/badge.svg)](https://codecov.io/gh/cmj0121/baton)

[English](../README.md) · **繁體中文** · [日本語](README.ja.md) · [한국어](README.ko.md) ·
[Français](README.fr.md) · [Deutsch](README.de.md) · [Español](README.es.md)

同時跑好幾個 AI coding agent?場面很快就會失控——一堆視窗要顧、session 散落在各個分頁,也沒有一個地方
能一眼看出誰在忙、誰卡住了、誰在等你回覆。

Baton 之於 AI agent,就像 tmux 之於 shell。它給你**一個純鍵盤操作的座艙**:一塊即時儀表板,列出每一個
agent,依所屬任務分組,任何一個都只差一個按鍵。

指揮棒在你手上,agent 們負責演奏,你來指揮。🎼

![Baton 座艙示範——儀表板上的面板、放大以驅動其中一個、把兩個併成一個工作項目、開啟 conductor 與 global shell](assets/baton-demo.png)

_開面板、放大進其中一個來操作、把兩個併成一個工作項目、用 `C` 叫出 conductor、用 `H` 叫出 global shell——而 `?`
隨時都在,告訴你按鍵。_

_影片由 [`baton-demo.tape`](assets/baton-demo.tape) 產生——重製步驟寫在該 tape 檔的檔頭。片中 conductor 的
agent CLI 是替身([`demo-agent.sh`](assets/demo-agent.sh)),好讓任何機器都錄得出同一支影片;它透過 socket
驅動的隊伍則是真的。_

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

- **儀表板(Dashboard)** — 任務中樞。一塊即時網格(數量一多就變成樹狀),列出每個面板的狀態與預覽。
  你在這裡導覽、開關面板,並把它們併成工作項目。
- **群組(Group)** — 一個工作項目的即時分割畫面:它的面板並排鋪開,全部同時串流。前幾個以即時磚(tile)串流,
  其餘的收摺成單一的**摘要磚**,可再放大進去。釘選(pin)幾個讓它們常駐;用 **`i`** 就地操作聚焦的那個,
  或按 **`enter`** 進入它。
- **放大(Zoom)** — 單一面板成為你唯一的終端機。按鍵直接送進程式;領導鍵 **`C-t`** 是你採取動作或退回上一層的方式。

## 按鍵

按鍵是**分模式(modal)的**:在儀表板與群組裡,每個動作是單一按鍵;在放大或互動時,你的按鍵驅動程式,
所以一個 Baton 動作是領導鍵 **`C-t`** 再接該按鍵。按 **`?`** 看目前畫面完整、可重新綁定的清單,
按 **`C-t k`** 編輯按鍵對應。

| 位置       | 按鍵              | 作用                                         |
| ---------- | ----------------- | -------------------------------------------- |
| `C-t` 之後 | `d` / `b`         | 跳到儀表板 / 退回上一層                      |
|            | `a`               | 注意力收件匣——清掉需要人的那些               |
|            | `[`               | 進入捲動模式                                 |
|            | `R` / `S`         | 重載設定 / 強制重啟伺服器                    |
|            | `q`               | 卸離(伺服器繼續執行)                         |
| 儀表板     | `hjkl` / 方向鍵   | 移動游標                                     |
|            | `enter`           | 開啟 / 放大所選                              |
|            | `p` / `A` / `c`   | 新增 shell / agent / 挑指令面板              |
|            | `.`               | 在聚焦面板的目錄開新 shell 面板              |
|            | `C`               | 開啟 conductor(替你驅動整隊的 agent)         |
|            | `H`               | 開啟 global shell(開在 `$HOME` 的宿主 shell) |
|            | `w` / `x`         | 關閉所選 / 清除已結束                        |
|            | `r`               | 重跑焦點下已結束的面板                       |
|            | `g` / `G` / `u`   | 標記 / 把已標記的併組 / 解除群組             |
|            | `s` / `f` / `D`   | 對所選送訊號 / 尋找 / diff                   |
|            | `/`               | 搜尋每個面板的輸出(grep 整支隊伍)            |
|            | `T` / `Q`         | 派任務 / 管理任務佇列                        |
|            | `U`               | 切換用量頁尾:關閉／計費窗口／聚焦面板        |
|            | `K`               | 切換狀態列上的按鍵提示                       |
| 群組       | `tab`             | 聚焦下一個面板                               |
|            | `+` / `-`         | 多顯示 / 少顯示即時磚                        |
|            | `L`               | 輪替磚的版面配置                             |
|            | `p` / `i`         | 釘選 / 與聚焦面板互動                        |
|            | `enter`           | 放大聚焦的面板                               |
| 放大       | 打字              | 直接驅動程式                                 |
|            | `C-t f` / `C-t g` | 搜尋捲動歷史 / git 選單(agent)               |

完整的逐畫面按鍵參照,以及每個畫面背後的設計,見 **[docs/SPEC.md](SPEC.zh-TW.md)**。

## 功能

照看整支隊伍時你會用到的一切,都只差一個按鍵:

- **訊號(Signals)** — `s` 對所選、聚焦的磚、或整個群組送出任何訊號;挑選器列出常見的,`o` 可輸入任何名稱或數字。
- **尋找、搜尋、複製** — `f` 依標題或群組過濾整隊;`/` 一次 grep 每個面板的輸出,並把命中依面板分組列出——
  `enter` 放大你挑的那個,直接停在命中處;`C-t f` 以正規表示式搜尋某個面板的捲動歷史;捲動模式(`C-t [`)
  透過 OSC52 選取並複製,所以在 SSH 上也能用,不需輔助執行檔。
- **Diff** — `D`(在放大裡是 `C-t D`)彈出該 agent 面板的工作樹 diff——已暫存與未暫存一次看,含未追蹤檔——
  以主從式(master-detail)疊層呈現。
- **Git** — `C-t g` 對放大中的 agent 開啟 git 選單:diff、log、status、暫存、commit、push、分支與 worktree。
  見 **[docs/GIT.md](GIT.zh-TW.md)**。
- **Conductor 與控制** — `C` 開啟一個 conductor:替你驅動整隊的 agent。它透過 socket——經由 `baton ctl`
  或 `baton mcp` 工具——開面板、分組、送訊號、對其他面板下提示,並圍上柵欄讓它無法搞壞自己的宿主。
  在 `$HOME/.baton/CONDUCTOR.md` 設定它的目標。見 **[docs/CONTROL.md](CONTROL.zh-TW.md)**。
- **Global shell** — `H` 開啟 global shell:伺服器持有的單一純宿主 shell,固定開在 `$HOME`,永遠一個按鍵之遙。
  它和 conductor 一樣是 FLEET 標題上的一個標記而非卡片,伺服器只留一個——重啟後會以已結束的空位保留,按 `r` 重跑。
  但它不驅動任何東西:沒有受限角色、沒有受管理的工作區。(與浮動的 **scratch** shell `C-t ~` 不同,後者是短暫的,卸離即消失。)
- **任務與佇列** — `T` 把一份簡報派給某個 agent(或散發給整個工作項目),記在卡片上,待 agent 就緒時送達。
  `Q` 管理一份持久化的待辦佇列,由伺服器自有的排程器抽取分派給空閒的 agent——也就是
  **你 → conductor → 隊伍** 的流程。`task.pre` 這個 Lua hook 可以改寫或否決一份簡報;`task.change` 則監看它。
- **群組與摘要** — `+` / `-` 調整有多少成員以即時磚串流;其餘收摺成一個摘要磚。被釘選的成員永遠串流。
  `L` 輪替分割畫面的**版面配置**——均勻網格、`main-vertical`、`main-horizontal`、`stack`,
  或你自己在 `TUI.yaml` 裡定義的網格。
- **資源上限** — 限制一個面板能用多少 CPU、記憶體、行程數,而且管的是它**整棵行程樹**,讓失控的建置沒辦法把整台
  機器一起拖下水。全隊底線與 per-agent 覆寫都寫在設定檔裡,也能在 `C-t P` 裡編;`C-t R` 會把新上限套用到正在跑
  的隊伍。Linux 上以 cgroup v2 強制,而主機若無法強制,面板會直說。見 **[docs/LIMITS.md](LIMITS.zh-TW.md)**。
- **重啟策略** — 預設關閉;設定 `panel.restart: on-failure`,行程異常結束的面板就會自己回來,帶指數退避,並且在
  達到上限時大聲停下而不是無聲迴圈。正常結束、你關掉的、你送過 signal 的,都不會被動到。可以逐 agent profile
  覆寫。
- **記住工作目錄** — 面板目前的工作目錄會從 shell 自己的 OSC 7 回報學來,沒有回報時則問行程表。重跑會落在你剛才
  所在的地方,`.` 在聚焦面板的目錄開一個 shell,路徑成為卡片的辨識標記,git 選單也會跟著 agent 進到 worktree。
  見 **[docs/RESTART.md](RESTART.zh-TW.md)**。
- **外觀** — `$HOME/.baton/TUI.yaml` 重塑座艙:一組色彩**主題**與群組分割的**版面配置**,用 `C-t R` 熱重載。
  見 **[docs/TUI.md](TUI.zh-TW.md)**。
- **用量頁尾** — `U` 循環切換計費窗口的頁尾讀數:帳號的 token 用量與成本,加上距離重置的倒數
  (`⊙ 1.2M tok · ≈$12.34 API · ⏳ 2:14:31`),或聚焦面板佔該窗口的比例。它預設讀取
  Claude Code 自己的 transcript(在 Pro/Max 訂閱下即可運作),或用金鑰走 Anthropic Admin API。
  該成本是 API 等值換算,並非訂閱費用。見 **[docs/USAGE.md](USAGE.zh-TW.md)**。
- **持久化與重生** — Baton 會跨重啟記住它的隊伍;面板以停滯的已結束空位回來,`r` 依保留的規格把它們重跑。
- **重載** — `C-t R`(或對常駐程式送 `SIGHUP`)在不重啟整隊的情況下熱重載設定。
- **滑鼠** — 預設關閉,好讓終端機自己的選取仍可用;在按鍵對應裡打開它,即可用滾輪捲動與選取。
- **語言** — `?` 按鍵清單與 `C-t k` 按鍵對應可讀英文或繁體中文。設定 `settings.language`、在按鍵對應裡即時輪替,
  或乾脆讓你的 `$LANG` 決定。見 **[docs/TUI.zh-TW.md](TUI.zh-TW.md#語言)**。

## 螢幕保護

走開,讓它閒著。閒置幾分鐘後——或按下隱藏的 `C-t E`——座艙會落入一整面的 Matrix 數位雨,中央浮著
**BATON** 字樣與一個大時鐘。這純粹是前端的接管:不會送任何東西到伺服器,任何按鍵或點擊都會立刻把你的畫面帶回來。

![Baton 螢幕保護——帶著 BATON 字樣與大時鐘的 Matrix 數位雨](assets/baton-screensaver.png)

_影片由 [`baton-screensaver.tape`](assets/baton-screensaver.tape) 產生——重製步驟寫在該 tape 檔的檔頭。_

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
- **[docs/RESTART.md](RESTART.zh-TW.md)** — 重啟策略:什麼算失敗、什麼不算,退避與上限,以及為什麼沒有 `always`。
- **[docs/GIT.md](GIT.zh-TW.md)** — git 選單:每個操作、commit 編輯流程、worktree,以及設定。
- **[docs/USAGE.md](USAGE.zh-TW.md)** — 帳號用量頁尾:本機與 Admin-API 兩種來源、設定,以及注意事項。
- **[docs/PLUGIN.md](PLUGIN.zh-TW.md)** — Lua 外掛 API:`baton` 物件、事件、指令,以及設定。
- **[docs/CONTROL.md](CONTROL.zh-TW.md)** — 以 agent 驅動整隊:conductor、`baton ctl` CLI、`baton mcp`
  工具,以及各種護欄。

## DDD(Dream-Driven Development,夢想驅動開發)

本專案奉行 DDD(夢想驅動開發):每一項功能都源自我所夢想、所需要的東西。
