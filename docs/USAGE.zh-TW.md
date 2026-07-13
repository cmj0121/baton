# 帳號用量頁尾

[English](USAGE.md) · **繁體中文**

Baton 可以在每個畫面上,以一段頁尾顯示你帳號**當日的 token 用量與成本**——
`⊙ 1.2M tok · ≈$12.34 API`。伺服器在背景輪詢它,並推送到每一個已接上的座艙;
按 **`U`** 顯示或隱藏它(此選擇會持久保存,且預設為開)。

成本刻意寫成 `≈…$ API`:它是當日 token 的 **API 等值換算**價格,而不是帳單。
見[它是什麼——又不是什麼](#它是什麼又不是什麼)。

## 資料來源

有兩種來源,因為「你的用量」會依你怎麼跑 agent 而有不同意義。Baton 以
`usage.source` 設定挑選其中一種。

| 來源    | 讀取                                                            | 適用於                                         |
| ------- | --------------------------------------------------------------- | ---------------------------------------------- |
| `local` | `~/.claude/projects` 底下 Claude Code 自己的 session transcript | 個人的 **Pro/Max 訂閱**(以及 API 金鑰使用皆可) |
| `api`   | Anthropic **Admin** 用量與成本 API                              | 一個 **Console／API 金鑰組織**                 |

**local** 來源是預設,也是訂閱可用的那一種:每次 Claude Code 執行——包括 Baton 開出的
agent 面板——都會附加一份 JSONL transcript,記錄每則訊息的 token 數,而 Baton 會加總
當日的訊息,並依各自的模型計價。它只讀取自本機午夜以來被異動過的檔案,所以就算是
數百個 session 的隊伍,也能在幾分之一秒內掃描完。設定 `CLAUDE_CONFIG_DIR` 可讓它指向
`~/.claude` 以外的位置。

**api** 來源會從 Admin API 回報你整個組織的 Console／API 金鑰帳務。它需要一把
**Admin API 金鑰**(`sk-ant-admin01-…`),Baton 從 `BATON_ANTHROPIC_ADMIN_KEY`
環境變數讀取它——絕不從設定檔讀。資料會比實際用量落後約五分鐘。

## 設定

主設定檔(`$HOME/.baton/config`):

```yaml
usage:
  source: auto # auto | local | api  (auto: api when an admin key is set, else local)
  interval: 30 # refresh seconds; 0 = default (30s local / 60s api); clamped to ≥ 10

settings:
  usage-footer: true # show the segment (also toggled live with U)
```

使用 `api` 來源時,Admin 金鑰放在環境變數裡:

```sh
export BATON_ANTHROPIC_ADMIN_KEY=sk-ant-admin01-…
```

`usage.source` 與 `usage.interval` 在常駐程式啟動時讀取;更動它們後需重啟伺服器
(`C-t S`)才會生效。`U` 這個切換則是即時的。

## 它是什麼——又不是什麼

- **成本是 API 等值換算,不是帳單。** 這個數字以官方公布的各模型費率替你的 token
  計價。在固定費率的 Pro/Max 訂閱下,它是一個「這在 API 上會花多少」的量表,而不是
  你實際被收取的金額。
- **它不會顯示剩餘額度。** 訂閱的剩餘配額沒有 API 可查,所以 Baton 回報的是你今天
  已*消耗*的量,而不是還剩下多少。
- **local 來源只涵蓋 Claude Code。** 其他 agent CLI(Copilot、……)不在 transcript
  裡,所以不會被計入。
- **api 來源需要一個組織。** Admin API 對個人帳號不開放,也不承載 Pro/Max 訂閱的
  用量;個人訂閱應使用 `local` 來源。
