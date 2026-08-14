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
	"help.title.dashboard": "儀表板",
	"help.title.zoom":      "放大檢視",
	"help.title.group":     "群組檢視",
	"help.title.keys":      "按鍵",
	"help.legend.back":     "返回",
	"help.legend.edit":     "編輯",
	"help.legend.scroll":   "↑↓ 捲動",

	// Rows shared by more than one view's key list.
	"help.common.keys":      "這份按鍵清單",
	"help.common.proc-tree": "行程樹 · daemon 底下的作業系統行程",
	"help.common.edit-map":  "編輯按鍵對應",
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
	"help.group.back":          "回到儀表板",
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
	"bind.new-panel-form": "新面板（自選要執行的指令）",
	"bind.new-agent":      "在指定工作目錄開一個 agent 面板",
	"bind.conductor":      "開啟 conductor — 指揮整群面板的 agent",
	"bind.global-shell":   "開啟全域 shell — 一個按鍵就到的主機 shell",
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

	"bind.mark":      "標記面板以便分組",
	"bind.group":     "把已標記的面板組成群組",
	"bind.add":       "把已標記的面板加入選取的群組",
	"bind.ungroup":   "解散選取的工作項目",
	"bind.rename":    "重新命名面板或群組",
	"bind.favourite": "收藏面板或群組（排到最前面）",

	"bind.help":         "檢視這個畫面的按鍵",
	"bind.usage-footer": "切換帳號用量／費用狀態列",
	"bind.key-map":      "編輯按鍵對應（前綴鍵）",
	"bind.panel-config": "設定面板預設值（前綴鍵）",
	"bind.scroll":       "捲動模式 — 逐行／逐頁（前綴鍵）",
	"bind.dashboard":    "跳到儀表板（前綴鍵）",
	"bind.proc-tree":    "行程樹 — daemon 底下的作業系統行程（前綴鍵）",
	"bind.back":         "回上一層：放大→群組→儀表板（放大檢視中按 C-t b）",
	"bind.commands":     "開啟外掛指令選單（前綴鍵）",
	"bind.scratch":      "切換浮動的暫存 shell（前綴鍵）",

	"bind.restart": "強制重啟 server",
	"bind.reload":  "重新載入設定（後端＋操作介面）",
	"bind.detach":  "離開（server 繼續執行）",
}
