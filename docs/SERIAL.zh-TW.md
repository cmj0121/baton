# Baton — 序列埠

[English](SERIAL.md) · **繁體中文**

`baton serial` 會打開一個序列埠,把它跟自己所在的那個面板之間的位元組雙向搬運。你原本會打
`screen /dev/cu.usbmodem2101 115200`,現在改打這個;而且它不是包一層 screen——baton 出貨就是一顆沒有 cgo 的靜態
binary,這座橋就長在裡面。

```sh
baton serial /dev/cu.usbmodem2101 115200
```

## 怎麼開一個

序列面板就是一般的指令面板。沒有新的面板種類,也沒有新的設定要寫。

| 從哪裡      | 這樣做                                                                             |
| ----------- | ---------------------------------------------------------------------------------- |
| 座艙        | `n c`,然後輸入 `baton serial /dev/cu.usbmodem2101 115200`                          |
| shell       | `baton serial /dev/cu.usbmodem2101 115200`                                         |
| `baton ctl` | `baton ctl spawn --run baton --arg serial --arg /dev/cu.usbmodem2101 --arg 115200` |

因為它就是一個指令面板,baton 也分不出它跟別的有什麼不同,所以既有的機制全部原封不動可用:它有 pid、會帶著離開
碼結束、`C-t l` 可以記錄它、重啟策略對它有效,`C-t w` 關掉它。

## 線路設定

```sh
baton serial <device> [baud] [--data-bits N] [--parity P] [--stop-bits N] [--flow F]
```

| 旗標                | 預設     | 可以是                                             |
| ------------------- | -------- | -------------------------------------------------- |
| `<device>`          | —        | 必填,例如 `/dev/cu.usbmodem2101` 或 `/dev/ttyUSB0` |
| `[baud]`            | `115200` | 見下面那張表                                       |
| `-d`, `--data-bits` | `8`      | `5`、`6`、`7`、`8`                                 |
| `-p`, `--parity`    | `none`   | `none`、`even`、`odd`                              |
| `-s`, `--stop-bits` | `1`      | `1`、`2`                                           |
| `-f`, `--flow`      | `none`   | `none`、`rtscts`、`xonxoff`                        |

預設值跟 screen 一樣——8N1、不做流量控制——所以對一個打了好幾年 `screen /dev/… 115200` 的人來說,
`baton serial /dev/… 115200` 送上線的是同一組設定。

## 該用哪個裝置名稱

在 macOS 上同一個轉接器會出現兩次:`/dev/tty.usbmodem2101` 跟 `/dev/cu.usbmodem2101`。用 `cu.` 那個。`tty.` 是撥入
用的裝置,對它做 `open(2)` 會一直等轉接器把載波偵測拉起來——碰到永遠不拉的轉接器,那就是永遠。在真的 ESP32-S3 上
量過:`tty.` 這個名字四秒後還卡在 `open` 裡,`cu.` 這個名字十四毫秒就回來了。

`baton serial` 對兩個名字都不會卡住,因為它帶了 `O_NONBLOCK`。但 `cu.` 才是「要跟對面講話」的那個名字,也是你該
打的那個。Linux 上只有一個名字——`/dev/ttyUSB0`、`/dev/ttyACM0`——所以沒有這個問題。

## 鮑率

baton 能設的鮑率,就是該平台 `termios` 有常數的那些;而兩個平台並不一致:

| 平台  | 鮑率                                                                                                                               |
| ----- | ---------------------------------------------------------------------------------------------------------------------------------- |
| macOS | 50、75、110、134、150、200、300、600、1200、1800、2400、4800、7200、9600、14400、19200、28800、38400、57600、76800、115200、230400 |
| Linux | 230400 以下同上,但沒有 7200、14400、28800、76800;另外多了 460800、500000、576000、921600、1000000、1152000、1500000、2000000       |

其他的都會在打開埠之前,連名帶號被擋下來,並且附上清單:

```text
baton serial: baud rate 12345 is not one this platform can set; supported: 50, 75, 110, …
```

這個拒絕是故意的。`termios` 拿到一個它不認得的速度並不會失敗——它會跑在某個別的速度上,而一條好線上的錯鮑率,
吐出來的東西跟壞掉的線一模一樣。省下那點方便,遠比不上別人一個下午。超出表格的鮑率在 macOS 需要 `IOSSIOSPEED`、
在 Linux 需要 `BOTHER`/`termios2`,這兩個呼叫 baton 都不做。

## 一個不肯接受設定的埠

設定線路可能「成功」了卻沒有真的發生。在一個根本沒有 RTS 也沒有 CTS 線的埠上,`TIOCSETA` 對 RTS/CTS 回報成功,
而下一秒讀回同一份 `termios`,那個位元是清掉的——這是在 ESP32-S3 的 USB-serial-JTAG 埠上量到的,而且它不是 bug,
是硬體對一個做不到的要求給出的誠實答案。

所以 baton 會把線路讀回來,驅動丟掉了什麼就拒絕什麼:

```text
baton serial: /dev/cu.usbmodem2101 does not support rtscts flow control: the driver accepted the setting and dropped it
```

把旗標拿掉,或是換一個真的有那兩條線的埠。悄悄地在你要的流量控制不存在的情況下繼續跑,正是這段程式要防的那種
失敗。

## 拔線

這才是 `baton serial` 存在、而不是直接 `--run screen` 的理由。有人踢到線,USB 轉接器就不見了;screen 會結束,
面板的捲動紀錄跟著一起走。baton 每個面板都留著 ring、記錄檔和 tail,所以這座橋比裝置活得久:

```text
baton serial: /dev/cu.usbmodem2101 open at 115200 8N1 — every byte passes through, close the panel to leave
[板子的輸出]
baton serial: /dev/cu.usbmodem2101: the port is gone — waiting for it to come back
baton serial: /dev/cu.usbmodem2101 open at 115200 8N1 — every byte passes through, close the panel to leave
[板子的輸出又回來了]
```

它每秒重試一次,一直試下去,而且用同一組設定——一個悄悄退回預設線路的重連,比不重連更糟。斷點以上的東西都還在
面板裡、還在記錄檔裡、還 grep 得到。

同一個迴圈也表示你可以在板子還沒插上去之前就先開面板。它會說為什麼打不開,一次斷線講一次,而不是一次嘗試講
一次;等裝置出現就打開它。

埠斷掉時打的字會被丟掉,不會排隊。板子一列舉出來就把一分鐘份的按鍵重播進去,是更糟的答案;而且面板早就說了埠
是斷的。

## 怎麼離開,還有 Ctrl-C

沒有跳脫鍵。`screen` 需要 `Ctrl-a k`,是因為 screen 佔著終端機;這個沒有——佔著的是 baton。所以每一個位元組都原封
不動穿過去,`Ctrl-C` 也一樣,而那正是你在跟 bootloader 或 busybox 提示字元講話時要的。像關別的面板一樣關掉它
(`C-t w`),那就是出口。

這是對 `screen /dev/… 115200` 唯一真正的改進,值得講明白:你不用再同時記兩套跳脫語彙,`Ctrl-a` 也回去當它的行首。

## 它不做的事

位元組進、位元組出。沒有本地回顯、沒有展開、沒有詮釋:

- 一個只送 `\n` 不送 `\r` 的裝置,畫面會一階一階往右下掉。`screen` 和 `picocom` 用預設的輸出對應時也是這樣,
  而這是那個裝置在告訴你一些關於它韌體的事。
- 本地不回顯任何東西。如果你看不到自己打的字,那是對面沒有回顯,這是關於對面的事實,不是關於 baton 的。
- 埠自己的位元組永遠不會被過濾。它們十之八九就是跳脫序列,而原封不動送過去正是這件事的本體。只有 baton 自己那幾
  行提示會被清洗,因為裡面的裝置名稱是打指令的人給的。

## `baton ctl dispatch` 會直接打到裝置上

序列面板是指令面板,所以它不是 agent,隊伍不會派工作給它。但 daemon 對 `panel.dispatch` 沒有任何種類上的閘門,
baton 也分不出這個面板跟別的有什麼不同——所以一個刻意打出來的

```sh
baton ctl dispatch <panel-id> "…"
```

會把那些位元組寫進埠、送出線,然後進到線另一頭的東西裡。

這裡沒有閘門,是因為閘門會是一句關於 baton 能強制什麼的謊:`--run` 接受任何 binary,而一個跑著會跟硬體講話的程式
的面板,跟一個跑著 shell 的面板是分不出來的。把序列面板的 id 當成那個裝置本身來看待:交給該去驅動那塊板子的
agent,其他的不要給。

## 延伸閱讀

- [docs/LOGGING.md](LOGGING.zh-TW.md) — 把面板輸出導進檔案,這正是拔線之後還撐得住的原因。
- [docs/CONTROL.md](CONTROL.zh-TW.md) — `baton ctl`,包含這裡用到的 `spawn --run` 跟上面那個 `dispatch`。
- [docs/RESTART.md](RESTART.zh-TW.md) — 重啟策略,對序列面板跟對別的指令面板一樣有效。
