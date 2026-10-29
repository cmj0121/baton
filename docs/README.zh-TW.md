# Baton

> 一個可擴充、對 agent 友善的終端機多工器。

[![Release](https://img.shields.io/github/v/release/cmj0121/baton)](https://github.com/cmj0121/baton/releases/latest)
[![License](https://img.shields.io/github/license/cmj0121/baton)](../LICENSE)
[![CI](https://github.com/cmj0121/baton/actions/workflows/ci.yml/badge.svg)](https://github.com/cmj0121/baton/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/cmj0121/baton/branch/main/graph/badge.svg)](https://codecov.io/gh/cmj0121/baton)

[English](../README.md) · **繁體中文** · [日本語](README.ja.md) · [한국어](README.ko.md) ·
[Français](README.fr.md) · [Deutsch](README.de.md) · [Español](README.es.md)

同時跑好幾個 AI coding agent?場面很快就會失控——一堆視窗要顧、session 散落在各個分頁,也沒有一個地方能一眼看出
誰在忙、誰卡住了、誰在等你回覆。Baton 之於 AI agent,就像 tmux 之於 shell:**一個純鍵盤操作的座艙**,一塊即時
儀表板列出每一個 agent,依所屬任務分組,任何一個都只差一個按鍵。指揮棒在你手上,agent 們負責演奏,你來指揮。🎼

![Baton 座艙示範——先叫出鍵表、開面板、開啟 conductor、把兩個併成工作項目,再於分割畫面與 zoom 裡按同一個 ? 鍵](assets/baton-demo.png)

`?` 列出你當下所在畫面的按鍵:開面板、用 `n C` 叫出 conductor、`g g` 再 `g c` 把兩個併成工作項目——然後在分割
畫面裡再按一次 `?`、在 zoom 裡按 `C-t ?`,是三張不同的表。

## 開始使用

Baton 是單一的靜態執行檔:macOS 上用 `brew install cmj0121/tap/baton`,Linux 上則是:

```sh
curl -fsSL https://raw.githubusercontent.com/cmj0121/baton/main/scripts/install.sh | sh
```

……或用 [Go](https://go.dev) 1.26+ 執行 `go install github.com/cmj0121/baton/cmd/baton@latest`。接著執行 `baton`,然後:

1. 按 **`A`** 開一個 agent(挑一個工作目錄;`enter` 就開在這裡,`w` 則隔離到新分支)。
2. 按 **`enter`** 放大進去看它工作;**`C-t d`** 把你帶回儀表板。
3. 按 **`q`** 卸離走人——一切都繼續在跑。隨時用 `baton` 回來。

迷路了?**`?`** 會顯示你當下所在畫面的按鍵。要指揮一整隊,**`n C`** 開啟一個 conductor,依照
`$HOME/.baton/CONDUCTOR.md` 裡的常設命令行事——見 **[docs/CONTROL.md](CONTROL.zh-TW.md)**。

## 概念

**Panel(面板)** 是一個即時終端機,跑著一支 agent CLI 或一個 shell。**Work item(工作項目)** 把同一項任務的面板
併在一起。**Task(任務)** 是你派給 agent 的一份簡報,排隊等到它有空為止。**Conductor** 是一個替你驅動其餘整隊的
agent。

你透過三種畫面看見它們:**儀表板**(每個面板以卡片或即時樹狀圖呈現)、**群組**(一個工作項目的面板並排串流)與
**放大**(單一面板成為你唯一的終端機,用領導鍵 `C-t` 退出來)。在它們背後,一個無頭伺服器透過 Unix socket 掌管
每一個終端機,所以你卸離再重新接上都不會漏掉任何東西。見 **[docs/SPEC.md](SPEC.zh-TW.md)**。

## 為什麼選 Baton

tmux 給你視窗,卻不知道裡面裝的是什麼。Baton 假設每個 pane 裡都是一個 agent,其餘一切都從這裡長出來:

- **你看得到誰需要你。** 每個面板帶著一個即時狀態,停下來等人的那些會排進同一個收件匣,不必一個一個視窗切過去找。
- **工作保持成組。** 服務同一項任務的面板組成一個工作項目,兩個鍵就併好,並當成一體來驅動。
- **你派出去的是工作,不是按鍵。** 把一份簡報派給一個 agent 或整組,或讓 conductor 替你帶整隊。
- **整隊有邊界。** 上限管住失控建置的整棵行程樹,用量也能追溯到花掉它的那個面板。
- **整隊會記得。** agent 一再學到的東西會被排序,交給下一份簡報。

Baton 不想接管你的 shell——你如果活在 tmux 裡,就把它跑在 tmux 裡。

## 功能

- **注意力** — 面板依 `running`、`idle`、`done` 或 `stuck` 排序;`C-t a` 是收件匣。[ATTENTION](ATTENTION.zh-TW.md)
- **Conductor** — `n C` 透過 `baton ctl` 或 `baton mcp` 驅動整隊,並與它的宿主隔開。[CONTROL](CONTROL.zh-TW.md)
- **Agent 技能** — `baton skill install` 或 Claude Code marketplace,教 baton 之外的 agent 駕駛它。[CONTROL](CONTROL.zh-TW.md#baton-之外的-agent)
- **任務** — `T` 把簡報派給一個 agent 或一個工作項目;`Q` 是一份由排程器分派給空閒 agent 的待辦佇列。
- **Score** — agent 一再學到的東西沿著階梯往上爬,並領在每份簡報之前,存於 `score.md`。[SCORE](SCORE.zh-TW.md)
- **上限** — CPU、記憶體與行程數限制,管的是面板整棵行程樹。[LIMITS](LIMITS.zh-TW.md)
- **用量** — `v u` 顯示 token、成本與額度進度條;`v U` 顯示是哪些面板在花。[USAGE](USAGE.zh-TW.md)
- **隔離** — `isolate: docker` 讓一個 profile 的面板在容器裡跑在你的 worktree 上。[ISOLATION](ISOLATION.zh-TW.md)
- **grep 整支隊伍** — `/` 搜尋每個面板的輸出;`C-t [` 捲動並透過 OSC 52 複製,在 SSH 上也能用。
- **Agent 後端** — 偵測 `claude`、`codex`、`gemini`、`aider`、`opencode` 與 `grok`;自己的寫在 `panel.agents` 底下。
- **遠端連線** — `baton --remote` 走你的 ssh 接上另一台機器上的隊伍。[REMOTE](REMOTE.zh-TW.md)
- **外掛** — 一個 Lua 檔 `$HOME/.baton/plug-in.lua`,掛上事件並加入指令。[PLUGIN](PLUGIN.zh-TW.md)
- **該有的都有** — diff(`D`)、git 選單(`C-t G`)、訊號(`s`)、面板記錄、序列埠、重啟策略、熱重載(`C-t R`)、
  主題與分割版面。

## 常用按鍵

在儀表板上每個動作是單一按鍵;在放大時你的按鍵驅動程式,所以要先按領導鍵 `C-t`。

| 位置       | 按鍵          | 作用                                                |
| ---------- | ------------- | --------------------------------------------------- |
| 儀表板     | `jk` / `↑↓`   | 移動游標                                            |
|            | `enter`       | 開啟 / 放大所選                                     |
|            | `A`           | 新增 agent——工作目錄,然後 enter 開在這裡或 `w` 隔離 |
|            | `n C`         | 開啟 conductor(替你驅動整隊的 agent)                |
|            | `g g` / `g c` | 標記 / 把已標記的併組                               |
|            | `T` / `Q`     | 派任務 / 管理佇列                                   |
|            | `w` / `x x`   | 關閉所選 / 清除已結束                               |
| `C-t` 之後 | `d` / `b`     | 跳到儀表板 / 退回上一層                             |
|            | `a`           | 注意力收件匣——清掉需要人的那些                      |
|            | `q`           | 卸離(伺服器繼續執行)                                |

其餘按鍵都可以用 `C-t k` 重新綁定,全列在 **[docs/KEYS.md](KEYS.zh-TW.md)**。

## 文件

- **使用** — [SPEC](SPEC.zh-TW.md) · [KEYS](KEYS.zh-TW.md) · [ATTENTION](ATTENTION.zh-TW.md) · [TUI](TUI.zh-TW.md) ·
  [GIT](GIT.zh-TW.md) · [LOGGING](LOGGING.zh-TW.md)
- **隊伍** — [CONTROL](CONTROL.zh-TW.md) · [SCORE](SCORE.zh-TW.md) · [USAGE](USAGE.zh-TW.md) ·
  [LIMITS](LIMITS.zh-TW.md) · [ISOLATION](ISOLATION.zh-TW.md) · [RESTART](RESTART.zh-TW.md)
- **擴充** — [PLUGIN](PLUGIN.zh-TW.md) · [REMOTE](REMOTE.zh-TW.md) · [SERIAL](SERIAL.zh-TW.md)
- **內部** — [DAEMON](DAEMON.zh-TW.md)

## DDD(Dream-Driven Development,夢想驅動開發)

本專案奉行 DDD(夢想驅動開發):每一項功能都源自我所夢想、所需要的東西。
