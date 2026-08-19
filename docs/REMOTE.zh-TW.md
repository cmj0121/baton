# Baton — 透過 SSH 遠端連線

[English](REMOTE.md) · **繁體中文**

你的 fleet 跑在建置機上,人卻在筆電前。`baton --remote` 會把**同一個 cockpit** 接上去——同一份執行檔、同一組按鍵、
同樣的畫面——走的是你本來就用來連那台機器的 ssh。

```sh
baton --remote          # 一張表單:位址,然後 passkey,接著就接上了
```

沒有網頁前端,沒有手機 app,也沒有第二個工具。Baton 的架構一直畫著「可抽換的前端接在 socket 上」;遠端連線就是讓
那張圖成真的那一塊,而且它仍然活在終端機裡,這正是整個專案的重點。

## 運作方式

傳輸層就是 `ssh(1)`,沒有別的。

```text
  筆電                                          建置機
  ┌────────────────┐                        ┌──────────────────────────────┐
  │ baton --remote │                        │ baton --stdio                │
  │  位址          │  ssh -p 22 host …      │  找到或啟動 daemon           │
  │  passkey       │───────────────────────►│  與 session socket 雙向轉送  │
  └───────┬────────┘   stdin / stdout       └───────────────┬──────────────┘
          │                                                 │
          │  hello { role: "remote", passkey, source }       │
          ▼                                                 ▼
      cockpit  ◄──────── 原本的協定 ─────────────────────  fleet
```

`baton --remote` 執行的是 `ssh <位址> baton --stdio`。遠端那頭把這條管線接到它自己的 unix socket 上,用跟本機
attach 完全相同的方式找到或啟動 daemon,兩端接著就講原本那套協定。

也就是說 baton **不開任何監聽埠、不帶 TLS、也不自己發明金鑰交換**。這條串流承載每個面板完整的終端機輸入與輸出,
而保護它的,正是本來就在保護你 shell 的那套東西——你的金鑰、你的 `known_hosts`、你的 `~/.ssh/config`。跳板機、
每台機器各自的金鑰、別名,全部照常運作,因為那個檔案是 ssh 讀的,baton 一行都沒有重寫。

`--stdio` 不是給人打的。它是遠端那側的橋接程式,寫在這裡只是為了讓你在 `ps` 裡看到它時不覺得莫名其妙。

## 怎麼打開

遠端連線**預設是關的**,在你明確表態之前都保持關閉。有兩條路:

```yaml
# $HOME/.baton/config
settings:
  remote: true # 接受透過 ssh 橋接連上來的 cockpit
```

……或是在 cockpit 裡按 **`C-t @`**,再按 `e`。這個鍵刻意放在前綴鍵後面:它會把這台機器打開給別人,這種鍵不該擺在
方向鍵旁邊一根手指的距離。用 `@` 而不是字母,是因為前綴鍵後面的鍵會蓋掉同一個鍵上的命令;而 `@` 正好就是這個畫面
列出來的東西:`user@host`。

`settings.remote` 是以**變化**而非以數值來讀的。如果你在 cockpit 裡把遠端關掉,之後又為了別的設定重新載入設定檔,
它會保持關閉——重新載入不會推翻你在那之後做的決定。

## Passkey

打開遠端連線時會產生一組 8 個字元的 passkey。它只留在記憶體裡,**永遠不寫進磁碟**,所以 daemon 一重啟就一定是新
的一組。從橋接連上來的 cockpit 會在 `hello` 時送出它;沒有帶上當前那組,連線會被拒絕、計入速率限制,並寫進紀錄。

要把這組 passkey 買到的東西講清楚,因為最難的那一段傳輸層已經決定了。管線遠端那頭本來就是用你的 uid 在跑——而
那個 uid 本來就能在那台機器上執行 `baton`。所以 passkey 是:

- ✅ 你**刻意**在這段期間打開遠端連線的憑據
- ✅ 一個**撤銷用的把手**——換掉它,新的連線就得用新的碼
- ❌ **不是**用來擋住「已經能用你的身分 ssh 進去的人」的驗證邊界

這跟 [SECURITY.md](../SECURITY.md) 為 conductor 圍欄與資源上限畫的是同一條線:防意外的護欄,不是沙箱。

有一件事它倒是實實在在買到了:passkey 是在表單裡輸入而不是當旗標傳,所以它**從不進入 argv**,不會留在 shell 歷史
裡,也不會出現在那台用戶端機器上其他行程看得到的 `ps` 中。

## 遠端畫面 — `C-t @`

```text
 REMOTE   enabled · passkey K7m2QxP9

   SOURCE                 ROLE      ATTACHED
 ▸ local ←                cockpit   2h 14m
   cmj@laptop.lan         remote    6m
   cmj@phone              remote    1m

 ↑↓ select · k kick · n new passkey · x disable · esc close
```

| 按鍵    | 作用                                                |
| ------- | --------------------------------------------------- |
| `↑` `↓` | 移動游標(`k` 已經是踢人,所以它不能同時是「上一列」) |
| `e`     | 打開遠端連線——只在它關著的時候有用                  |
| `k`     | 踢掉選取的連線;對面的 cockpit 會被告知原因          |
| `n`     | 換一組 passkey——既有連線留著,新的連線得用新的碼     |
| `x`     | 關閉遠端連線,並斷開所有遠端連線                     |
| `r`     | 重新跟 fleet 要一次清單                             |
| `esc`   | 關閉                                                |

這份清單是**推播**的,不是輪詢:每一次連上、離開、被踢,兩側開著的畫面都會即時更新。

`ATTACHED` 從連線送出 hello 的那一刻起算。`SOURCE` 是對面自己報上的名字——cockpit 所在機器的 `user@hostname`——
所以它是用來辨認一條連線的標籤,跟 role 一樣是自己宣告的,從來就不是伺服器被要求去信任的身分。

### 一個刻意留下的不對稱

`k` 從**兩側**都能用。踢人是你處理一條意料之外連線的手段,如果得先走到另一台機器前面才做得到,這個功能就沒用了。

`e`、`n`、`x` 則**只限本機**——伺服器會拒絕從遠端連線發出的這些動作,畫面也不會再把它們列出來。任何持有一條有效
遠端連線的人,都已經證明他知道當前那組碼;再讓他去產生下一組,等於把一段期間變成永久。基於同樣的理由,遠端
cockpit 根本不會被告知 passkey:它只看得到 `enabled`,以及該去哪裡讀那組碼。

## 遠端 cockpit 能做什麼

本機能做的它都能做。遠端連線存在的意義就是要好用,一個只能看不能碰的 cockpit 不值得為它拉這條管線。

它在 `hello` 時宣告 `role: "remote"`,這既是它出現在清單裡的原因,也替日後不動協定就引入更受限的角色留了空間。

要記得從頭到尾重要的都是 fleet 那台機器:挑選器裡列出的 agent 後端是**那台主機**跑得動的、面板生在那裡、`C-t l`
的紀錄寫在那裡,狀態列的 CPU 與記憶體也是它的。

## 失敗時會說什麼

連線表單會照實說出哪裡出錯,並保留你打過的位址,所以 passkey 打錯一個字只差一次重打:

| 你會看到                                     | 實際發生的事                                    |
| -------------------------------------------- | ----------------------------------------------- |
| `No route to host` / `Permission denied`     | ssh 自己的說法——網路或金鑰的問題,還沒輪到 baton |
| `is baton on the remote PATH?`               | `ssh host cmd` 跑的是非互動式 shell,見下        |
| `remote access is not enabled on this fleet` | 那邊根本沒有人把它打開                          |
| `wrong passkey`                              | 碼換過了,或是打錯了                             |
| `too many failed attempts`                   | 一分鐘內五次錯誤;這扇門會關著把那一分鐘走完     |

### `baton: command not found`

這是最可能遇到的第一個失敗,而且不是 baton 的錯:`ssh host cmd` 跑的是**非互動式**登入 shell,它的 `PATH` 常常沒
有 `~/.local/bin` 或 `~/go/bin`。直接把遠端那側的指令寫清楚:

```yaml
# $HOME/.baton/config —— 寫在你「撥出去」的那台機器上
settings:
  remote-command: /home/cmj/go/bin/baton --stdio
```

## 連線中斷時

如果線路斷掉或 ssh 死了,cockpit 會回報並結束。**fleet 完全不受影響**——daemon 繼續跑,每個面板保有它的行程與捲
動內容,再執行一次 `baton --remote` 就回到原本的地方。第一版沒有做退避重連;之後要加也不需要動協定。

被 fleet 主動斷開的 cockpit——被踢,或是遠端連線在它腳下被關掉——會在 socket 消失前先被告知**原因**,並在離開時把
那個原因印出來。

## 不做的事

- 不開監聽用的 TCP 埠、不用 TLS、不把 passkey 當成加密。
- 沒有網頁或手機前端。
- 沒有多使用者帳號,也沒有逐連線的 ACL。
- 不是安全邊界。見 [SECURITY.md](../SECURITY.md)。

## 設定一覽

```yaml
# $HOME/.baton/config
settings:
  remote: false # 是否接受透過 ssh 橋接連上來的 cockpit(預設 false)
  remote-command: baton --stdio # `baton --remote` 要 ssh 在遠端執行的指令
```

`remote` 由 **fleet** 所在的機器讀取;`remote-command` 由你**撥出去**的那台機器讀取。
