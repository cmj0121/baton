# Baton — Daemon,以及它沒起來的時候

[English](DAEMON.md) · **繁體中文**

> Baton 做的每一件事都發生在同一個背景行程裡。座艙是它的客戶端,`baton ctl` 是它的客戶端,`baton mcp` 也是它的
> 客戶端。這一頁講的是那個行程的開機:它照什麼順序起來、起來的過程中它會說什麼,以及它沒起來的那個早上你該
> 怎麼辦。

一個控制 socket 一個 daemon,一個使用者一個 socket,所以你開的每一個座艙——不管從哪個終端機啟動——接上的都是
同一支隊伍。要跑第二支,用 `BATON_SOCK`。

## 什麼東西照什麼順序起來

在沒有 daemon 在跑的時候執行 `baton`,它會把自己重新執行成 daemon,然後等五秒,等一個會回答的 socket。在那個
時間窗裡,daemon 會照這個順序做這些事:

| 步驟                                         | 卡在這裡的話                      |
| -------------------------------------------- | --------------------------------- |
| 1. 用一個 advisory lock 宣告 session         | —                                 |
| 2. 寫 PID 檔                                 | —                                 |
| 3. 讀 `$HOME/.baton/config`                  | 根本不會有 socket 被建出來        |
| 4. 掃掉舊的 conductor workspace              | 根本不會有 socket 被建出來        |
| 5. 從 `score.dir` 開啟隊伍記憶               | 上限十秒,之後隊伍會在沒有記憶下跑 |
| 6. 綁 socket、把它鎖成只有自己能連、開始服務 | —                                 |

第 3 到第 5 步讀的是你的檔案系統,而其中任何一步都可能在一個不再回答的掛載點上永遠等下去。它們發生在綁定
socket 之前,而那正是重點:一個消失了的 `$HOME` 或 `score.dir`,留下的是根本沒有 socket,而不是一個會接受連線
然後永遠不回答的 socket。客戶端得到的是幾毫秒內的連線錯誤,而不是掛在一條已經被接受、日誌裡卻什麼都沒有的
連線上。

它還不是「所有的讀取」。第二輪設定讀取、座艙外觀(`TUI.yaml`)與 Lua 外掛仍然在 socket 存在之後才讀,因為
它們設定的正是那個要等 listener 才存在的 server。

其中兩步會在做之前先說一聲,用 `INF` 寫在 `$HOME/.baton/baton.log` 裡:

```txt
INF boot: reading the config path=/home/you/.baton/config
INF boot: opening the fleet memory dir=/home/you/.baton within=10s
```

它們存在只有一個理由:當一次開機卡住時,它們就是這個記錄檔的最後一行,所以檔案指得出 daemon 停在哪個東西上,
而不是一片空白。

## Readiness 探測

一個等 socket 的 supervisor——systemd unit、launchd job、容器 healthcheck——現在拿到的是它真正想問的那個答案:
socket 存在,就代表 daemon 已經可以服務。

它以前會太早通過。對著一份 456 MB 的隊伍記憶量到:對 socket 存在性的探測在 90 ms 就通過了,而它背後的座艙
還要再掛大約 6.6 秒。這是被修正掉了——而它同時也是升級時要先想過的一個行為改變。一個照著舊的假通過調出來的
重啟迴圈,現在會在原本看到成功的地方看到一次真正的失敗;而在一次緩慢的開啟做到一半時重啟 daemon,等於那次
開啟永遠開不完:在一個大 store 上,那也正是本來會把它壓縮掉、讓之後每一次開機都變快的那次開機。給探測比
一次開機所需更寬的餘裕。

## 「baton server did not come up」

```txt
baton server did not come up; see /home/you/.baton/baton.log — its last line names what the daemon
was reading. If it is still wedged there, `baton --force` stops it and starts again
```

`baton` 等滿了它的五秒,而沒有 socket 出現。它是兩件事的其中一件,而記錄檔最後那行 `boot:` 就是分辨它們的東西。

- 一次只是比較慢的開機,這是常見的那一種。Daemon 會在那則訊息後面起來並正常服務;再跑一次 `baton` 就會接上。
  通常做出這件事的是一份大的隊伍記憶——見 **[SCORE.md](SCORE.zh-TW.md#升級之後第一次開機遇上大-store)**。
- 一次卡在上面那些讀取裡的開機。這一種不會自己好:卡住的那個 daemon 還握著 session 宣告,所以之後每一個
  `baton` 都會把宣告輸給它,然後一聲不響地離開。

`baton --force` 就是第二種的出口。它會停掉握著這個 session 的那個 daemon——包括一個從來沒走到 socket 的——等它
消失、把它留下的東西收乾淨,然後啟動一個新的。

它是靠 session 宣告決定的,不是靠 PID 檔。PID 檔活得比它指名的行程久,而作業系統會重用 pid,所以光憑那個檔案
就送訊號出去,可能會把一個 `SIGTERM` 送給一個毫不相干的程式。那個宣告是一把 kernel 的鎖,持有者一死就會被
放掉——包括被 `SIGKILL` 殺掉——所以它回答的是唯一值得問的那個問題:此刻有沒有一個屬於這個 socket 的 daemon
還活著。答案是沒有的時候,那些陳舊的檔案會被收掉,而不會有任何訊號被送出去。

`--force` 做不到的,是修好 daemon 當時正在讀的那個東西。如果卡住的原因是一個死掉的掛載點,新的 daemon 會走到
同一個讀取、停在同一個地方。先把那條路徑救回來或卸載掉。如果記錄檔最後一行指的是隊伍記憶,`score.dir` 也可以
指到本機的地方——而 daemon 會給那次開啟十秒,然後選擇在沒有記憶的情況下服務整支隊伍,而不是乾脆不服務。

## 一個 daemon 擁有的檔案

Socket 放在你的 runtime 目錄裡,旁邊那四個檔案的名字都是從它推出來的——所以 `BATON_SOCK` 底下的第二支隊伍會有
自己的一套。最後兩個是每個使用者一份而不是每個 socket 一份,同一個 `$HOME` 底下的兩支隊伍會共用它們。

| 檔案                     | 裝著什麼                                                 |
| ------------------------ | -------------------------------------------------------- |
| `baton.sock`             | 控制 socket,權限被鎖成只有擁有者能連                     |
| `baton.lock`             | session 宣告——讓「一個 socket 一個 daemon」成立          |
| `baton.pid`              | 跑著的 daemon 的 pid,寫在綁定之前,所以卡住的那個也停得掉 |
| `baton.state.json`       | 持久化的隊伍與版面                                       |
| `baton.queue/`           | 任務待辦,一個任務一個檔                                  |
| `$HOME/.baton/config`    | 第 3 步讀的那份設定                                      |
| `$HOME/.baton/baton.log` | daemon 自己的記錄檔——這一頁的每一則訊息都寫在這裡        |

## 相關

- **[SCORE.md](SCORE.zh-TW.md)** — 第 5 步開啟的那份隊伍記憶:什麼會讓它變慢,以及什麼框住它。
- **[CONTROL.md](CONTROL.zh-TW.md)** — socket 另一端的那些客戶端:`baton ctl`、`baton mcp`、conductor。
- **[RESTART.md](RESTART.zh-TW.md)** — 另一種重啟:一個行程結束掉的面板適用的政策。
