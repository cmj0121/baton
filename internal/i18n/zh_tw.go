package i18n

// zhTW is the Traditional Chinese catalog. It covers the cockpit's help
// surfaces — the ? key list and the key-bindings screen that shares its
// descriptions — and nothing else yet; any key it does not carry falls back to
// the English at the call site (see T).
//
// Keys are grouped by surface. Terms a user types or reads elsewhere in the
// system — key names, shell, agent, signal, server, git — stay in English on
// purpose: a translated key name is a key nobody can press.
var zhTW = map[string]string{
	// --- purpose sections, shared by the key list and the key map -------------
	"cat.navigation": "導覽",
	"cat.panels":     "面板",
	"cat.work-items": "工作項目",
	"cat.view":       "檢視",
	"cat.session":    "工作階段",

	// --- the ? key list ------------------------------------------------------
	"help.title.dashboard":     "儀表板",
	"help.title.zoom":          "放大檢視",
	"help.title.group":         "群組檢視",
	"help.title.keys":          "按鍵",
	"help.legend.back":         "返回",
	"help.legend.edit":         "編輯",
	"help.legend.tab":          "分頁",
	"help.legend.scroll.short": "捲動",
	"help.landing":             "這個中繼鍵底下有 %d 個鍵",
	"help.landing.one":         "這個中繼鍵底下有 %d 個鍵",
	"help.legend.scroll":       "↑↓ 捲動",

	// Rows shared by more than one view's key list.
	"help.common.keys":      "這份按鍵清單",
	"help.common.proc-tree": "行程樹 · daemon 底下的作業系統行程",
	"help.common.remote":    "遠端連線 · passkey 與目前的連線清單",
	"help.common.edit-map":  "編輯按鍵對應",
	"help.common.log":       "把這個面板的輸出寫入檔案 · 讀回來看",
	"help.common.reload":    "重新載入設定（後端＋操作介面）",
	"help.common.detach":    "離開（server 繼續執行）",

	// Dashboard-only rows; the rest of that list comes from the bindings below.
	"help.dash.move":    "移動",
	"help.dash.reorder": "重排選取的項目",
	"help.dash.open":    "開啟／放大",
	"help.dash.clear":   "清除選取",

	// Zoom.
	"help.zoom.type":      "直接操作程式（含 PgUp/PgDn）",
	"help.zoom.scroll":    "捲動模式 · ↑↓ 逐行，b/空白 逐頁，esc 離開",
	"help.zoom.search":    "搜尋捲動歷史 · n 往舊，N 往新",
	"help.zoom.literal":   "送出一個實際的 ",
	"help.zoom.commands":  "任何儀表板按鍵,前面加上領導鍵即可",
	"help.zoom.git":       "git 選單 · diff、log、commit、push、worktree(agent 面板)",
	"help.zoom.signal":    "送 signal 給這個面板",
	"help.zoom.back":      "回上一層（群組分割／儀表板）",
	"help.zoom.dashboard": "直接回儀表板",

	// Group split.
	"help.group.focus":         "聚焦下一個／上一個面板",
	"help.group.interact":      "互動：直接在原地輸入到聚焦的面板",
	"help.group.zoom":          "放大聚焦的面板",
	"help.group.tiles":         "顯示更多／更少即時圖磚",
	"help.group.layout":        "切換圖磚版面",
	"help.group.resize":        "調整大小模式 · 方向鍵放大／縮小聚焦的圖磚",
	"help.group.reorder":       "重排聚焦的面板",
	"help.group.scroll":        "捲動模式 · 聚焦的面板（↑↓ 逐行，b/空白 逐頁）",
	"help.group.search":        "搜尋聚焦的面板 · n 往舊，N 往新",
	"help.group.pin":           "釘選／取消釘選聚焦的面板到即時圖磚",
	"help.group.signal":        "送 signal 給聚焦的面板 · 整個群組",
	"help.group.remove":        "把聚焦的面板移出群組",
	"help.group.back":          "回上一層",
	"help.group.stop-interact": "結束互動（互動模式中）",
	"help.group.dashboard":     "儀表板（任何畫面都適用）",

	// --- the key-bindings screen ---------------------------------------------
	"keymap.title":    "按鍵設定",
	"keymap.prefix":   "prefix · 前綴（leader）鍵",
	"keymap.settings": "設定",

	"keymap.legend.move":    "移動",
	"keymap.legend.section": "區段",
	"keymap.legend.edit":    "編輯",
	"keymap.legend.run":     "執行",
	"keymap.legend.back":    "返回",

	"setting.confirm-close": "關閉面板前先確認",
	"setting.bell":          "面板需要你時響鈴",
	"setting.mouse":         "啟用滑鼠（滾輪捲動＋選取）",
	"setting.language":      "介面語言",

	// --- the footer's standing invitation to the key list ---------------------
	"footer.keys": "按鍵",

	// --- binding descriptions, keyed by the binding's stable name -------------
	"bind.new-panel":      "開一個新的 shell 面板",
	"bind.new-panel-here": "在聚焦面板所在的目錄開一個 shell 面板",
	"bind.new-panel-form": "開一個 shell 面板,或用你指定的程式開 command 面板",
	"bind.new-agent":      "在指定工作目錄開 agent,或隔離到新分支",
	"bind.conductor":      "開啟 conductor — 指揮整群面板的 agent",
	"bind.global-shell":   "開啟全域 shell — 一個按鍵就到的主機 shell",
	"bind.new-worktree":   "在新分支的新 worktree 裡開一個隔離的 agent",
	"bind.score-edit":     "用 $EDITOR 開啟艦隊記憶 (score.md)",
	"bind.close":          "關閉選取的面板",
	"bind.respawn":        "重新執行選取範圍內已結束的面板",
	"bind.purge-exited":   "清除所有已結束的面板",
	"bind.signal":         "送 signal 給面板",
	"bind.search":         "尋找面板 · 搜尋捲動歷史（放大檢視）",
	"bind.fleet-search":   "在每個面板的輸出中搜尋關鍵字",
	"bind.diff":           "顯示工作區的 diff（agent 面板）",
	"bind.dispatch":       "派送任務給 agent 面板",
	"bind.enqueue":        "把任務排進佇列交給空閒 agent（有選取則限該工作項目）",
	"bind.queue":          "管理任務佇列（列出 · 重排 · 取消 · 排空）",
	"bind.log":            "開始／停止把面板輸出寫入檔案（需 prefix）",
	"bind.log-view":       "以臨時面板開啟該記錄檔並持續追蹤（需 prefix）",

	"bind.mark":      "標記面板以便分組",
	"bind.group":     "把已標記的面板組成群組",
	"bind.add":       "把已標記的面板加入選取的群組",
	"bind.ungroup":   "解散選取的工作項目",
	"bind.rename":    "重新命名面板或群組",
	"bind.favourite": "收藏面板或群組（排到最前面）",
	"bind.move":      "把一列拿起來 — 方向鍵搬動、enter 放下",
	"bind.expand":    "展開或收合這一列底下的巢狀內容",

	"bind.help":         "檢視這個畫面的按鍵",
	"bind.usage-footer": "切換用量狀態列：關閉、計費窗口、目前面板、額度",
	"bind.usage-view":   "帳號用量 — 完整額度進度條，以及誰在消耗",
	"bind.keycast":      "切換狀態列上的按鍵提示",
	"bind.preview":      "切換樹狀圖旁的詳細窗格",
	"bind.layout":       "儀表板的卡片或樹狀 — 小艦隊也能切成樹狀",
	"bind.group-by":     "切換分組視角:工作項目、目錄、profile、狀態",
	"bind.key-map":      "編輯按鍵對應（前綴鍵）",
	"bind.panel-config": "設定面板預設值（前綴鍵）",
	"bind.scroll":       "捲動模式 — 逐行／逐頁（前綴鍵）",
	"bind.dashboard":    "跳到儀表板（前綴鍵）",
	"bind.proc-tree":    "行程樹 — daemon 底下的作業系統行程（前綴鍵）",
	"bind.remote":       "遠端連線 — passkey 與目前的連線清單（前綴鍵）",
	"bind.inbox":        "待辦匣 — 逐一處理需要人介入的面板（前綴鍵）",
	"bind.back":         "回上一層：放大→群組→儀表板（放大檢視中按 C-t b）",
	"bind.commands":     "開啟外掛指令選單（前綴鍵）",

	"bind.restart": "強制重啟 server（前綴鍵）",
	"bind.reload":  "重新載入設定（後端＋操作介面）",
	"bind.detach":  "離開（server 繼續執行）",

	// The usage footer's views, and what the per-panel view says when it has
	// nothing to attribute.
	"usage.mode.off":           "關閉",
	"usage.mode.window":        "計費窗口",
	"usage.mode.panel":         "目前面板",
	"usage.mode.limits":        "額度",
	"usage.panel.unattributed": "無法歸屬",
	"usage.panel.of-window":    "／本窗口",

	// The account-usage overlay: the quota bars in full, and who is spending them.
	"usage.view.no-reading":  "還沒有額度讀數 — Claude Code 面板跑完第一輪後就會回報",
	"usage.view.ago":         "前",
	"usage.view.just-now":    "剛剛",
	"usage.view.session":     "工作階段（5h）",
	"usage.view.week":        "本週（全部）",
	"usage.view.week-opus":   "本週（Opus）",
	"usage.view.week-sonnet": "本週（Sonnet）",
	"usage.view.credit":      "額外點數",
	"usage.view.resets":      "重置於",
	"usage.view.uncapped":    "無上限",
	"usage.view.burning":     "本窗口的消耗來源",
	"usage.view.share":       "占比",
	"usage.view.tokens":      "token",
	"usage.view.of-5h":       "占 5h",

	// The vendor roll: which agent backends this machine has, and which of them
	// baton can account for.
	"usage.view.agent":         "代理",
	"usage.view.accounting":    "baton 能讀到的用量",
	"usage.view.nothing-spent": "本窗口尚無消耗",

	// The working-directory features.
	"panel.here.unknown": "那個面板的目錄不明;改在預設工作目錄開啟",

	// --- the panel-config page (the prefix + P screen) ------------------------
	//
	// The row labels of the resource-limits section are NOT here: cpus, memory,
	// memory-high, pids and nofile are the keys someone writes in their config
	// file, and the same argument that keeps a key name in English keeps those.
	// Their edit overlays are prose and are translated below.
	"panel.cfg.title":          "面板設定",
	"panel.cfg.shell":          "預設 shell",
	"panel.cfg.agent":          "預設 agent",
	"panel.cfg.replay":         "重播緩衝區",
	"panel.cfg.limits":         "資源限制",
	"panel.cfg.feedback":       "評分回饋",
	"panel.cfg.not-installed":  "已知但尚未安裝",
	"panel.cfg.system-default": "系統預設",
	"panel.cfg.server-default": "預設",
	"panel.cfg.no-cap":         "不限制",

	"panel.cfg.hint.agent":    "預設 agent 就是 %s 會開的那一個 · 在艦隊執行的機器上偵測",
	"panel.cfg.hint.replay":   "重播緩衝區決定捲動歷史的起始內容 · 重啟 server 後生效",
	"panel.cfg.hint.limits":   "限制會套用到面板底下的整棵行程樹",
	"panel.cfg.hint.feedback": "評分回饋 · 關閉只是不再主動告知,並不會停止回報",

	"panel.cfg.enforce.unknown": "尚未連上,無法得知是否真的生效",
	"panel.cfg.enforce.none":    "在這台機器上沒有生效",
	"panel.cfg.enforce.by":      "由以下機制強制執行:",
	"panel.cfg.no-profiles":     "尚未設定任何 agent profile · 設定檔中的 panel.agents 負責命名",
	"panel.cfg.install-hint":    "先安裝,然後按 %s R 重新偵測",

	"panel.cfg.status.shell":      "預設 shell · 輸入路徑（留白代表系統預設）,按 enter 儲存",
	"panel.cfg.status.replay":     "重播緩衝區 · 每個面板的 KiB 數（留白代表預設）,按 enter 儲存",
	"panel.cfg.status.restart":    "重啟後生效",
	"panel.cfg.status.new-panels": "套用於新開的面板",

	// The resource limits' edit overlays.
	"limit.cpus.title":         "CPU 限制",
	"limit.cpus.prompt":        "CPU 核心數,例如 2 或 1.5（留白代表不限制）",
	"limit.memory.title":       "記憶體限制",
	"limit.memory.prompt":      "硬上限,例如 4Gi（留白代表不限制）",
	"limit.memory-high.title":  "記憶體警戒線",
	"limit.memory-high.prompt": "在殺掉之前先節流,例如 3Gi（留白代表不限制）",
	"limit.pids.title":         "行程數限制",
	"limit.pids.prompt":        "面板行程樹裡最多幾個行程（留白代表不限制）",
	"limit.nofile.title":       "開啟檔案數",
	"limit.nofile.prompt":      "每個行程可開啟的檔案描述符數（留白代表不限制）",

	// --- the text-input overlays (one popup per input purpose) ----------------
	//
	// A prompt keeps the exact spelling of anything it is telling someone to type:
	// `git checkout -b`, a signal name, a regexp. Those are the words that have to
	// survive into a terminal unchanged.
	"input.title":  "輸入",
	"input.prompt": "值",

	"input.shell.title":          "預設 shell",
	"input.shell.prompt":         "shell 路徑（留白代表系統預設）",
	"input.replay.title":         "重播緩衝區",
	"input.replay.prompt":        "每個面板保留多少 KiB 歷史（留白代表預設）",
	"input.new-panel.title":      "開新面板",
	"input.new-panel.prompt":     "程式與參數（留白代表開 shell）",
	"input.agent-dir.title":      "開新 agent",
	"input.agent-dir.prompt":     "工作目錄（留白代表家目錄）",
	"input.group.title":          "新增群組",
	"input.group.prompt":         "工作項目名稱",
	"input.rename.title":         "重新命名",
	"input.rename.prompt":        "新名稱",
	"input.dispatch.title":       "派送任務",
	"input.dispatch.prompt":      "要交給 agent 的任務說明",
	"input.enqueue.title":        "任務排隊",
	"input.enqueue.prompt":       "排進佇列、等空閒 agent 接手的任務說明",
	"input.signal.title":         "送出 signal",
	"input.signal.prompt":        "signal 名稱或編號（例如 WINCH、TSTP、28）",
	"input.filter.title":         "尋找面板",
	"input.filter.prompt":        "依標題或群組過濾（即時）",
	"input.search.title":         "搜尋",
	"input.search.prompt":        "在捲動歷史中尋找",
	"input.fleet-search.title":   "全艦隊搜尋",
	"input.fleet-search.prompt":  "grep 每個面板的輸出（regexp）",
	"input.git-branch.title":     "新分支",
	"input.git-branch.prompt":    "分支名稱（git checkout -b）",
	"input.worktree.title":       "新 worktree",
	"input.worktree.prompt":      "分支名稱（worktree ＋ agent）",
	"input.worktree-rm.title":    "移除 worktree",
	"input.worktree-rm.prompt":   "worktree 路徑（接著再確認）",
	"input.worktree-repo.title":  "新 worktree",
	"input.worktree-repo.prompt": "要從哪個 git repository 開分支",

	// --- the pickers ----------------------------------------------------------
	//
	// A backend's name and the command behind it are what this machine has
	// installed, and a signal's wire name is the word `kill` takes. Both stay in
	// English; the prose beside them does not.
	"agent.pick.title":         "選擇 agent",
	"agent.pick.default.title": "預設 agent",
	"agent.pick.is-default":    "預設",
	"agent.pick.hint":          "在艦隊執行的那台機器上偵測到的 · 按 %s R 重新偵測",
	"agent.pick.status":        "新的 %s agent · 輸入工作目錄",

	"signal.to":         "送到",
	"signal.other":      "其他…",
	"signal.other.desc": "任何名稱或編號",
	"signal.hint":       "送到面板的行程群組 · 按 %s R 重新載入 baton 設定",
	"signal.unknown":    "不認得的 signal %q · 請輸入名稱或編號",

	"signal.desc.SIGINT":  "中斷（Ctrl-C）",
	"signal.desc.SIGTERM": "終止",
	"signal.desc.SIGKILL": "強制砍掉",
	"signal.desc.SIGHUP":  "掛斷",
	"signal.desc.SIGQUIT": "結束並產生 core",
	"signal.desc.SIGUSR1": "使用者自訂 1",
	"signal.desc.SIGUSR2": "使用者自訂 2",

	// Values and legend words shared by more than one surface.
	"legend.move":      "移動",
	"legend.edit":      "編輯",
	"legend.back":      "返回",
	"legend.save":      "儲存",
	"legend.cancel":    "取消",
	"legend.create":    "建立",
	"legend.spawn":     "開啟",
	"legend.next":      "下一步",
	"legend.send":      "送出",
	"legend.queue":     "排隊",
	"legend.apply":     "套用",
	"legend.find":      "尋找",
	"legend.search":    "搜尋",
	"legend.complete":  "自動補齊",
	"legend.del-word":  "刪一個詞",
	"value.on":         "開",
	"value.off":        "關",
	"feedback.inherit": "繼承",
	"feedback.score":   "評分回饋",
	"status.spawning":  "正在開啟",
}
