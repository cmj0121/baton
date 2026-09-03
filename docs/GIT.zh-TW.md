# Baton — Git

[English](GIT.md) · **繁體中文**

> 不用離開你正在照看的 agent,就能做常見的 git 工作。**git 選單**是一個以按鍵操作的
> 彈出選單,在放大進某個 agent 面板時以領導鍵 **`C-t G`** 開啟,會針對該 agent 的工作目錄
> 執行 git。

它在設計上**只在放大時可用**——你操作的就是你正在看的那一個 agent——而且**只對 agent 開放**:
shell、非 repo,或暫時性的(diff／git)畫面永遠不會開啟它。它建立在 [diff](./SPEC.zh-TW.md)
功能的機制之上:多數操作會把輸出擷取進一個**可捲動的彈出視窗**,疊在座艙之上,是 diff 彈出視窗的
文字版手足。

## 選單

在放大狀態下按 `C-t G`,會為放大中的 agent 開啟選單。用操作的鍵帽選取,或用 `↑↓`(`j`/`k`)
再按 `enter`;`esc` 取消。`push` 與 `remove` 會先問 `y/n`。

| 鍵  | 操作        | 執行                                               | 結果                            |
| --- | ----------- | -------------------------------------------------- | ------------------------------- |
| `d` | diff        | 工作樹對 `HEAD`,含未追蹤檔                         | 主從式彈出視窗(即 diff)         |
| `l` | log         | `git log --oneline --graph --decorate -n 200`      | 文字彈出視窗                    |
| `s` | status      | `git status`                                       | 文字彈出視窗                    |
| `a` | stage all   | `git add -A`                                       | 文字彈出視窗                    |
| `c` | commit      | `git add -A && git commit`(開啟 `$EDITOR`)         | 暫時性 PTY 面板                 |
| `p` | push        | `git push`——**先確認**                             | 文字彈出視窗                    |
| `b` | branch      | `git checkout -b <name>`                           | 文字彈出視窗                    |
| `w` | worktree    | `git worktree add -b <branch> <path>` + 一個 agent | 新的分組 agent(一個 fleet 項目) |
| `W` | worktrees   | `git worktree list`                                | 文字彈出視窗                    |
| `x` | rm worktree | `git worktree remove <path>`——**先確認**           | 一則狀態通知                    |

**文字彈出視窗**會把該操作擷取到的輸出疊在目前畫面上顯示:伺服器在該 agent 的工作目錄裡一次性
執行該命令、回收它,再把文字回覆回來——儀表板上不會生出任何東西,也不會保存任何東西。`j`/`k`
與翻頁鍵可捲動;`esc` 關閉並還原你原本所在的畫面。非零的離開狀態(被拒絕的 push、失敗的 branch)
仍會開啟彈出視窗、標頭染色,讓你看到 git 自己的訊息。這些擷取會帶著 `GIT_TERMINAL_PROMPT=0`
以及 30 秒上限執行,所以會要求輸入憑證的 push 會快速失敗,而不是卡住。

**`commit`** 是唯一的例外:它會開啟 `$EDITOR`,而後者需要一個真正的終端機,所以它保留了**暫時性
PTY 面板**——伺服器把它當成一個短生命週期的 PTY 生出來,絕不落在儀表板上、也絕不保存,座艙會直接
以自動放大的方式落入其中。用一般的放大退出方式把它關掉(`C-t b` 返回、`C-t d` 儀表板、`C-t q`
卸離)——那會把它拆除。一條連線最多同時保留 8 個暫時性面板(diff 明確的 `diff-command` 與 commit
共用這個上限);超過之後,該操作會回報 `too many open panels (max 8) — close one first`。

## Commit——你的編輯器,就在面板裡

`commit` 會暫存所有東西並執行 `git commit`,後者會在**暫時性面板的 PTY 裡**開啟你的編輯器——vim、
nano,不管你用哪個,表現得跟在終端機裡一模一樣。寫好訊息、存檔、離開;commit 完成,面板顯示結果。
乾淨的工作樹會以 `nothing to commit` 拒絕。

編輯器依此順序解析:**`panel.editor`** 設定,否則走 git 自己的鏈(`$GIT_EDITOR` →
`git config core.editor` → `$EDITOR` → `vi`)。所以如果 git 在命令列上本來就會開你想要的編輯器,
baton 不需要任何額外設定。

## Worktree——為平行 agent 而生的隔離

選單底下只有**一條伺服器路徑**,它只要三樣東西:一個 **repo**、一個**分支**,以及一份
**agent spec**(指令、參數、profile)。

```text
repo + 分支 + spec → git worktree add -b → 在該樹裡生出一個 agent → 分組到該分支之下
```

`C-t G` `w` 是這條路徑的**其中一個呼叫者**,而不是路徑本身。它從你放大進去的那個 agent
解析出 repo 與 spec,再把兩者交出去;在那之後,整段程序都不需要 repo 裡有一個活著的 agent。
不是 git repo 的路徑會以 `not a git repository: …` 被拒絕,而且不會退回成一般的生成。

還有**第二個呼叫者**,那也正是這條路徑不收面板 id 的原因。主控台上的 `n w` 從一個目錄、而不是從
一個 agent 出發,跑的是同一段程序——見下方的[兩個入口](#兩個入口)。

- **`w`(worktree + agent)**會詢問一個分支名稱,接著用放大中 agent 的 repo,以及它的指令、
  參數與 profile 去跑上面那條路徑——所以新的樹會拿到同一種 agent、落在同一組資源上限之下,
  並**分組在該分支之下**,好讓它一次就落成一個工作項目。這就是你把一個 agent 展開到隔離分支上、
  又不讓它去踩你所在那棵樹的方式。這棵樹會放在設定了的 **`panel.worktree-dir`** 底下,否則放在
  一個手足位置 `"<repo>-worktrees/<branch>"`(分支名裡的斜線會變成破折號)。
- **`W`(worktrees)**在文字彈出視窗裡列出此 repo 的各個 worktree。
- **`x`(rm worktree)**會詢問一個路徑、確認,接著 `git worktree remove` 掉它。它在**沒有
  `--force`** 的情況下執行,所以 git 會拒絕移除帶有未提交變更或帶鎖的 worktree——這是安全的預設,
  以錯誤的形式呈現出來。它針對的是你輸入的路徑,絕不是現行 agent 自己的工作目錄,所以你不會不小心把
  一棵樹從正在運作的 agent 腳下抽走。

### 兩個入口

同樣一棵樹、一個 agent、一個群組,有兩個動詞可以到達;要用哪一個,取決於你此刻是不是已經在看某個
agent。

| 動詞        | 在哪裡       | repo 來自           | agent profile        |
| ----------- | ------------ | ------------------- | -------------------- |
| `n w`       | 主控台       | 你自己挑的目錄      | **艦隊預設**         |
| `C-t G` `w` | 放大的 agent | 該 agent 的工作目錄 | **該 agent** 的 spec |

**`n w`** 是在沒有東西可以展開時、直接從隔離開始的方式。它先問版本庫——一個可輸入的路徑欄位,
有 `tab` 補完、`C-b` 刪掉一個路徑片段,以及 `C-o` 叫出目錄選擇器,和 `A` 用的完全是同一個欄位——
接著才問分支,用的是 `C-t G` `w` 打開的同一個欄位。它沒有來源面板,所以新的 agent 用的是**艦隊
預設**的 profile(也就是你什麼都不挑時 `A` 會生出來的那一種),絕不是主控台游標剛好停在誰身上的
複製品。

不是 git 版本庫的目錄會被**拒絕**:不生成、不建立任何目錄,也不會退回成一般的 `A`。同一個目錄裡的
`A` 依然做它一向做的事——把 agent 開在版本庫**裡面**,不長出任何樹。

**`C-t G` `w`** 則是另一端:你正在看著一個 agent,而你想要另一個像它的 agent 待在自己的分支上。
它會複製那個 agent 的指令、參數與 profile,所以新的樹會拿到同一種 agent、落在同一組資源上限之下。

不論是哪一個動詞開出來的,關掉新面板都會讓樹留在原地。兩個動詞都不移除任何東西;git 選單裡的 `x`
是一棵樹唯一的退場方式。

### baton 開的樹,與你自己開的樹

如果樹建好了、但 **agent 沒能啟動**,這棵樹會**留在原地**——用 `x` 把它退場,而不是讓 baton
去猜。

為了讓 baton 分得出這種樹和你自己開的樹,這條路徑開出來的每一棵樹都會被記到 fleet 快照旁邊的
一個檔案裡:`<socket>.worktrees.json`,由控制 socket 推導出來、由機器寫入、絕不手動編輯,
跟 `<socket>.state.json` 完全一樣。**不會有任何東西被寫進 worktree 本身**——寫在那裡的標記檔
會擺在該樹裡工作的 agent 面前,而它很可能會把它 commit 進去。你用一般 `git worktree add` 開的樹
永遠不會出現在這份紀錄裡。

它是獨立於 fleet 快照的檔案,因為兩者的生命週期不同:關閉該 agent、清除、重啟常駐程式,都會讓
樹留在原地,所以這份紀錄比快照所描述的那個 fleet 活得更久。只有在快照本身有被寫入時它才會被寫入;
而一棵樹要退場,只有這裡的 `x` 或
[`baton ctl worktree sweep`](#看見殘留以及清掉它) 兩條路——兩者也都依然沒有 `--force`。

用 `x` 讓一棵樹退場時,也會把它從紀錄裡**拿掉**,所以這個檔案記的是 baton 現在擁有的樹,而不是
它曾經開過的每一棵。這是整理,不是保證:在你自己的終端機裡跑 `git worktree remove`,或是直接把
目錄刪掉,都不會經過 baton,所以紀錄裡仍可能留著一個指向已不存在的樹的路徑。

### 看見殘留,以及清掉它

關掉面板會把樹留在原地,這是對的預設,同時也是會愈積愈多的預設。有兩個指令把那份紀錄讀回來:

```sh
baton ctl worktree list          # baton 開過的每一棵樹,以及它們的下場
baton ctl worktree sweep         # 移除其中的孤兒樹;會先問過你
```

`list` 會拿每一筆有記錄的路徑,對照當下的隊伍分類:

| 狀態        | 意思                                    |
| ----------- | --------------------------------------- |
| `live`      | 有一個還在跑的面板正在裡面工作——別碰它  |
| `dead-slot` | 只有一個已結束的面板指著它,但那張卡還在 |
| `orphan`    | 隊伍裡沒有任何東西指著它——這就是殘留    |

dead-slot 不是孤兒。面板的行程沒了,但它的卡還在:上面還留著 agent 的逐字稿,重新啟動也還是指向
那棵樹。把那個位子清掉(cockpit 裡的 `x x`,或 `panel.purge`),樹才成為孤兒。要注意 `close` 會跳過
這個階段——關掉一個面板會連同它的 spawn spec 一起丟掉,所以那棵樹當下就沒人認領了。

`sweep` 只處理孤兒,走的是 `x` 鍵用的同一個 `git worktree remove`,也同樣沒有 `--force`:一棵還留著
未提交成果的樹、或是被鎖住的樹,會被跳過並且點名,而不是讓整輪清掃失敗;它也會留在紀錄裡,等你
處理完之後,下一次清掃可以把事情做完。至於目錄已經不在的孤兒,已經沒有東西可以移除了,所以只把
紀錄拿掉——git 自己那筆過時的登記,是 `git worktree prune` 的工作。

它在終端機上會先確認,在指令稿裡則需要明講的 `--yes`,所以一個不小心被發現的指令沒辦法清空一顆
磁碟。它也不會透過 MCP 提供,而且 daemon 對 conductor 連線一律拒絕:開 worktree 是 agent 的事,
讓它們退場是你的事。

紀錄以外的東西一律不會被碰到。你用 `git worktree add` 自己開的樹不在任何紀錄裡,所以不會出現在
清單上,而讀那份清單的清掃也就搆不著它——就算它正好放在 `panel.worktree-dir` 底下,或是和 baton
自己的樹並排在 `<repo>-worktrees` 裡也一樣。而在關掉持久化時根本沒有紀錄,那就代表什麼都不掃:
沒有狀態檔要讀成「baton 一棵都沒開過」,絕不是「這些樹都沒人認領」。

## 安全

這組操作是**只增不減**的:讀取(diff/log/status/worktrees)、暫存、commit、branch、push、
worktree-add。**沒有 `reset`、沒有 `clean`、沒有 `checkout` 式的丟棄,任何地方也都沒有 `--force`**,
所以一次誤觸絕不會摧毀成果。兩個會向外伸手或移除東西的操作——**push** 與 **worktree-remove**——
各自都會先問 `y/n`。git 自己的拒絕(沒有 upstream、髒的 worktree、重複的分支)會原封不動地
出現在彈出視窗或狀態列裡。

## 設定

這三項設定都放在 `$HOME/.baton/config` 的 `panel:` 底下,並可用 `C-t R`(或對常駐程式送
`SIGHUP`)**熱重載**——不必重啟、不會丟失面板:

```yaml
panel:
  editor: nvim # commit editor (GIT_EDITOR); empty = git's own chain
  worktree-dir: ~/src/.worktrees # base for new worktrees; empty = a sibling of the repo
  diff-command: git diff HEAD | delta # the diff op's command; empty = git diff.tool then built-in
```

## 深入內部

選單送出一個命令 `panel.git`,帶著操作(`git`)、目標 agent(`id`),以及——在適用時——一個分支
(`name`)或一個 worktree 路徑(`dir`)。伺服器在 [`internal/gitops`](../internal/gitops)
(`gitdiff` 的手足)裡把該操作解析成一個具體命令,接著:

- 一個**非互動輸出操作**(log/status/add/push/branch/worktrees)由 `gitops.Capture` 擷取,
  並以一則 `gitout` 訊息回覆,座艙把它顯示在文字彈出視窗裡——沒有 PTY、什麼都不保存;
- **commit** 保留暫時性 PTY 面板(它驅動 `$EDITOR`),回覆之後座艙會自動放大進去(就是明確的
  `diff-command` 所用的 `openEphemeral` 引擎);
- **worktree-add** 解析出 repo 與 spec,再呼叫那條共用的 repo + 分支 + spec 路徑,由它建立這棵
  樹、把樹記下來、生出並分組該 agent,再廣播整個 fleet——兩個動詞都不留這段程序的任何副本。兩者
  唯一的差別就在**怎麼解析**:有 `id` 時它指名一個面板,repo 與 spec 都從那裡來;而**空的 `id`**
  是主控台那一式,由 `dir` 指名 repo、`path`/`args`/`profile` 帶著座艙從艦隊預設解析出來的 spec。
  兩個動詞送的是同一個命令,所以沒有新增第二個 wire action,協定版本也沒有動;只認得第一式的舊
  常駐程式,對第二式會回 `no panel with id ""`——是拒絕,不是誤讀。對 conductor 連線,無目標那一式
  會被拒絕:讓它自己指定要跑的指令,等於給了 `panel.create` 的能力卻沒有 `panel.create` 的數量上限
  與速率上限,所以 conductor 只留下複製既有 agent 的那一式;
- **worktree-remove** 同步執行,並以一則通知確認。

「只對 agent 開放」與「需在 git 工作樹內」這兩道柵欄由伺服器端強制執行——座艙也會把關,但常駐程式
才是真相的來源。
