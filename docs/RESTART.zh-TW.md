# 面板重啟策略

[English](RESTART.md) · **繁體中文**

行程死掉的面板可以自己回來。這個功能**預設是關的** —— Baton 從來不會啟動一個你沒
要求的行程,而一個會這麼做的策略應該由你主動打開,而不是升級之後自動繼承。

```yaml
# $HOME/.baton/config
panel:
  restart: on-failure # never(預設)| on-failure
  restart-max: 5 # 連續失敗幾次之後放棄
  restart-backoff: 2s # 兩次嘗試之間指數等待的基數
  restart-healthy: 30s # 一次執行撐過這麼久,失敗次數就歸零
```

## 難的部分是知道**什麼時候不要**重啟

重啟很容易。不要把你刻意停掉的東西又拉回來,才是一個 supervisor 能不能被忍受的關鍵。

| 發生了什麼              | 要重啟嗎 | Baton 怎麼分辨                     |
| ----------------------- | -------- | ---------------------------------- |
| ssh 斷線                | **要**   | 退出碼 255                         |
| agent CLI 崩潰          | **要**   | 任何非零退出碼                     |
| 你在 shell 裡打 `exit`  | 不要     | 退出碼 0 —— 那是做完了,不是失敗    |
| agent 完成了它的任務    | 不要     | 退出碼 0                           |
| 你用 `w` 關掉面板       | 不要     | 面板在行程被回收之前就已經不存在了 |
| 你用 `s` 送 signal 給它 | 不要     | 送出 signal 之前就先記下了這個意圖 |
| daemon 正在關閉         | 不要     | 整隊面板是被刻意殺掉的             |

最後兩項值得說清楚。被 signal 殺掉的行程和崩潰的行程,退出方式**完全相同** ——
兩者都是退出碼 `-1`,因為被 signal 終結的行程根本沒有退出狀態可以回報。所以光看
退出碼分不出「我叫它停的」和「它自己倒了」,Baton 是在你按下 `s` 的當下就記下意圖,
而不是事後用猜的。

而這個紀錄是刻意短命的。送 signal 不等於想殺掉它 —— 對 agent 送 `SIGINT` 是中斷
一個它會活下來的任務 —— 所以這個抑制只維持十秒。一小時後真的崩潰,面板還是會回來。

## 放棄的時候要大聲

crash loop 不能安安靜靜地吃掉一小時,所以等待會變長,嘗試次數也被算著。用預設值,
一個一直死的面板讀起來是:

```text
exited · restarting in 2s (1/5)
exited · restarting in 4s (2/5)
exited · restarting in 8s (3/5)
…
exited · restart limit reached after 5 failures
```

然後就停在那裡,直到你自己按 `r` 重跑。等待時間每次連續失敗就加倍,並且上限是五
分鐘:再長下去,那個「等待」讀起來就是「放棄了」,卻還宣稱自己在努力。

`restart-healthy` 是讓計數器保持誠實的東西。一次撐過那麼久的執行就是一次好的執行,
所以次數歸零 —— 一個已經跑了一整天的面板,應該重新拿到完整的額度,而不是上禮拜某個
crash loop 的尾巴。

重啟會顯示在造成它的那次結束旁邊:面板的觀看者會看到 `[restarting in 2s (1/5)]`
取代平常的 `[process exited]`,新行程起來時則是一行 `[restarted]`。這是唯一能讓人
分辨「這是新行程」還是「程式自己清了畫面」的線索。

## 逐 agent 覆寫

策略的疊加方式和資源上限完全一樣:整隊的區塊是底線,profile 只重述它要改的那部分。

```yaml
panel:
  restart: on-failure # 整隊都會重連、重試
  agents:
    claude:
      command: claude
      restart: never # …但這個如果死了,我想自己看一眼
```

四個 key 在 profile 層都可以設,不是只有 `restart`。

## 為什麼沒有 `always`

systemd 有,Baton 不提供。對一個 agent 面板來說,「永遠重啟」幾乎一定是錯的:完成
任務的 agent **本來就該**停下來,而一個分辨不出這件事的模式,會把每一次順利完成都
變成無窮迴圈。

`always` 想服務的情境 —— 斷掉的 ssh、被看管的 tunnel —— 本來就是 `on-failure` 的
情境,因為兩者都是非零退出。在設定檔裡寫 `always` 會被**指名拒絕**,而不是默默當成
`on-failure`,這樣設定檔就永遠不會說出 Baton 並不是那個意思的話。

## 設定寫錯的時候

格式錯誤的策略會被記進 log 並且**整個丟掉**,留下一個什麼都不重啟的隊伍。這是刻意
選擇的失敗方向:一個 Baton 只讀懂一半的策略,會用你沒寫過的節奏去啟動行程,那比
「不重啟、而且說明原因」更糟。

```text
WRN restart policy ignored; panels will not be restarted
    error="panel.restart \"always\" is not a mode baton offers (never, on-failure)"
```

整個區塊都可以熱重載 —— `C-t R`,或對 daemon 送 `SIGHUP` —— 和旁邊的資源上限一樣。
策略變更會套用在下一次結束,不會打擾正在跑的面板。

## 它不是什麼

- **不是通用的 supervisor。** 唯一的健康檢查是「行程還活著」。沒有 readiness probe,
  也沒有「不健康就重啟」。
- **不是工作階段重播。** 重啟後的面板是一個畫面乾淨的新行程,不是被還原的舊行程,
  它的捲動歷史從空的開始。
- **不是刻意關閉之後的後路。** `w` 是不可逆的,面板與它的 spawn spec 一起消失。
