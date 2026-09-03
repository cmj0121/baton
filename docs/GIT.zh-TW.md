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
而移除一棵樹依然只有 `x` 一途,依然沒有 `--force`。

用 `x` 讓一棵樹退場時,也會把它從紀錄裡**拿掉**,所以這個檔案記的是 baton 現在擁有的樹,而不是
它曾經開過的每一棵。這是整理,不是保證:在你自己的終端機裡跑 `git worktree remove`,或是直接把
目錄刪掉,都不會經過 baton,所以紀錄裡仍可能留著一個指向已不存在的樹的路徑。

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
- **worktree-add** 從目標面板解析出 repo 與 spec,再呼叫那條共用的 repo + 分支 + spec 路徑,
  由它建立這棵樹、把樹記下來、生出並分組該 agent,再廣播整個 fleet——選單自己不留這段程序的
  任何副本;
- **worktree-remove** 同步執行,並以一則通知確認。

「只對 agent 開放」與「需在 git 工作樹內」這兩道柵欄由伺服器端強制執行——座艙也會把關,但常駐程式
才是真相的來源。
