# Baton

> 拡張可能で、agent にやさしいターミナルマルチプレクサ。

[![Release](https://img.shields.io/github/v/release/cmj0121/baton)](https://github.com/cmj0121/baton/releases/latest)
[![License](https://img.shields.io/github/license/cmj0121/baton)](../LICENSE)
[![CI](https://github.com/cmj0121/baton/actions/workflows/ci.yml/badge.svg)](https://github.com/cmj0121/baton/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/cmj0121/baton/branch/main/graph/badge.svg)](https://codecov.io/gh/cmj0121/baton)

[English](../README.md) · [繁體中文](README.zh-TW.md) · **日本語** · [한국어](README.ko.md) ·
[Français](README.fr.md) · [Deutsch](README.de.md) · [Español](README.es.md)

AI coding agent を同時に何本も走らせていますか?状況はあっという間に混沌とします——たくさんのウィンドウを
やりくりし、session はタブのあちこちに散らばり、誰が動いていて、誰が詰まっていて、誰があなたの返事を待って
いるのかを一目で見られる場所がありません。

Baton と AI agent の関係は、tmux と shell の関係と同じです。**キーボードだけで操るコックピット**を
提供します。すべての agent を所属タスクごとにまとめた即時ダッシュボードで、どれもキー 1 つの距離にあります。

指揮棒はあなたの手の中に。agent たちが演奏し、あなたが指揮する。🎼

![Baton コックピットのデモ——まずキー一覧、panel を起こし、conductor を開き、2 つを work item にまとめ、分割表示と zoom で同じ ? をもう一度](assets/baton-demo.png)

_一つのキーで一巡します。`?` はいま立っている view のキーを出す——panel を起こし、`n C` で conductor を呼び、
`g g` から `g c` で 2 つを work item にまとめ、そして分割表示の `?` と zoom の `C-t ?` は三つの違う表です。_

_この映像は [`baton-demo.tape`](assets/baton-demo.tape) から生成されています。conductor の agent CLI は代役
([`demo-agent.sh`](assets/demo-agent.sh))で、どのマシンでも同じ映像が録れます。socket 越しに動かしている fleet
のほうは本物です。_

## はじめる

Baton は単一の静的バイナリです。macOS なら、[Homebrew](https://brew.sh) で入れられます:

```sh
brew install cmj0121/tap/baton
```

Linux なら、一行で済みます:

```sh
curl -fsSL https://raw.githubusercontent.com/cmj0121/baton/main/scripts/install.sh | sh
```

……あるいはプラットフォームを問わず、[Go](https://go.dev) 1.26+ で取得することもできます:

```sh
go install github.com/cmj0121/baton/cmd/baton@latest
```

……あるいは clone したソースから `make install` でビルドします。あとはこう実行するだけ:

```sh
baton
```

Baton はバックグラウンドサーバを起動し、あなたを**ダッシュボード**——本拠地——へ連れていきます。最初の 1 分:

1. **`A`** を押して agent を起動します(作業ディレクトリを選ぶことになります)。
2. **`enter`** を押してズームし、その働きを眺めます。**`C-t d`** でダッシュボードに戻れます。
3. **`q`** を押してデタッチし、席を立ちます——すべては動き続けます。いつでも `baton` で戻ってこられます。

迷いましたか?**`?`** はいつでも、今いる場所のキーを見せてくれます。

## tmux ではだめなのか

tmux は pane の中身を知らないからです。渡されるのは窓だけで、どれがどれかを覚えておくのはあなたの仕事。agent が
ずっと待っていたことに気づくには、一つずつ切り替えて見るしかありません。Baton は pane の中身が agent だと前提を
置き、あとはそこから出てきます:

| やっていること          | tmux で手作業                | Baton                                                                      |
| ----------------------- | ---------------------------- | -------------------------------------------------------------------------- |
| 誰が待っているかを知る  | pane を順に切り替えて読む    | panel ごとの生きた状態と、人を待って止まったものが並ぶ `C-t a` の受信箱    |
| 関係する仕事をまとめる  | 窓に名前を付け、規則を覚える | work item——名前の付いた panel のまとまり、キー 2 つで作れる                |
| 仕事を渡す              | pane ごとに自分で打ち込む    | task を一つにも group 全体にも配る、または conductor に fleet を動かさせる |
| 暴走した build を止める | なし                         | CPU・メモリ・プロセス数の上限を、panel のプロセスツリー全体に効かせる      |
| いくらかかっているか    | なし                         | 請求ウィンドウの token とコスト、そして quota バーを panel 単位で          |

Baton は tmux の置き換えではないし、あなたの shell を欲しがってもいません——tmux の中で動かして構いません。

## コンセプト

- **shell ではなく agent。** 作業の単位は、世話を焼くウィンドウではなく、走っている agent です。
- **ウィンドウではなくダッシュボード。** タブの山ではなく、すべてを一度に見渡せる即時のオーバービュー。
- **ヘッドレスな中核と、差し替え可能なフロントエンド。** 頭脳はバックグラウンドの常駐プロセス。それを描き出す顔は差し替えられます。

| Concept          | 何であるか                                                                                                                    |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| **Panel**        | 1 つの生きたターミナル——_agent_ panel(agent CLI)か _shell_ panel。                                                            |
| **Work item**    | 1 つのタスクに属する、名前付きの panel のまとまり。                                                                           |
| **Task**         | agent に投げる指示書——ライフサイクル全体を追跡し、待たせる必要があればキューに入れてスケジュールします。                      |
| **Conductor**    | あなたの代わりに fleet を動かす agent——socket 越しに panel を起こし、まとめ、他の panel に指示を出します。                    |
| **Global shell** | サーバが `$HOME` に 1 つだけ持つ素のホスト shell。つねにキー 1 つの距離にある——本拠地であって、fleet の運転手ではありません。 |

## 画面

Baton は 3 つの画面で操作し、キー 1 つで行き来します:

- **ダッシュボード(Dashboard)** — 管制塔。小さな艦隊は panel ごと・work item ごとに 1 枚の**カード**を並べた
  グリッドで、最上位のものが 6 つ以上になるとすべての panel を並べた即時**ツリー**に変わります:work item が 1 行、
  その サブグループがその下にインデントされ、panel はさらにその下に並びます。`space` はどの深さでもその行の下に
  ある入れ子を表示・非表示にし、`→` で work item を開いて中へ入り、`←` で閉じて外へ戻ります——最上位から外へ出れば
  カードに戻ります。行にはターミナルの幅に応じて状態・作業ディレクトリ・出力スパークライン・投入されたタスクが
  順に加わり、`v p` で横に詳細ペインを出せます。ここで移動し、panel を起こしたり閉じたりし、work item にまとめます。
- **グループ(Group)** — ある work item の即時分割画面:その panel が横並びに敷き詰められ、すべてが同時に
  ストリームします。先頭のいくつかがライブのタイルとしてストリームし、残りは 1 枚の**サマリタイル**に畳まれます
  (ズームして中を見られます)。いくつかを pin して常時表示にし、**`i`** でフォーカス中のものをその場で操作するか、
  **`enter`** でその中へ降ります。
- **ズーム(Zoom)** — 1 つの panel が唯一のターミナルになります。キー入力はそのままプログラムへ届き、リーダーキー
  **`C-t`** が、操作したり一段戻ったりするための入口です。

## キー

キーは**モーダル**です:ダッシュボードとグループでは各操作が単一キー。ズームや対話中はキー入力がプログラムを
動かすため、Baton の操作はリーダーキー **`C-t`** に続けてそのキーを押します。**`?`** で現在の画面の完全な
(再バインド可能な)一覧を、**`C-t k`** でキーマップの編集を開けます。
4 つのキーは「ランディング」で、単独では何もせずファミリーを開きます——`n` は起動、`v` は表示、`g` はグループ、
`x` は自分自身を確認するダブルタップ——ステータスバーが次に受け取れるキーを示します。

| 場所           | Key                   | 動作                                                                |
| -------------- | --------------------- | ------------------------------------------------------------------- |
| `C-t` のあと   | `d` / `b`             | ダッシュボードへ跳ぶ / 一段戻る                                     |
|                | `a`                   | attention の受信箱 — 人を必要としているものを片づける               |
|                | `[`                   | スクロールモードに入る                                              |
|                | `l` / `L`             | パネルをファイルに記録 / その記録を読み返す                         |
|                | `R` / `S`             | 設定を再読み込み / サーバを強制再起動                               |
|                | `q`                   | デタッチ(サーバは動き続けます)                                      |
| ダッシュボード | `jk` / `↑↓`           | カーソルを動かす                                                    |
|                | `hl` / `←→`           | カードを 1 つ移動·ツリーでは work item を閉じる／開く               |
|                | `space`               | その行の下にある入れ子を表示／非表示                                |
|                | `v p` / `v g`         | 詳細ペイン／グループ化の切替:work item・ディレクトリ・profile・状態 |
|                | `v l`                 | ダッシュボードの表示:カードかツリーか                               |
|                | `m`                   | 行をつかむ——矢印で運び、`enter` で置く                              |
|                | `enter`               | 選択中のものを開く / ズームする                                     |
|                | `p` / `A` / `n c`     | 新しい shell / agent / コマンド選択 panel                           |
|                | `n .`                 | フォーカス中の panel のディレクトリに新しい shell panel             |
|                | `n C`                 | conductor を開く(fleet を動かす agent)                              |
|                | `n h`                 | global shell を開く(`$HOME` のホスト shell)                         |
|                | `w` / `x x`           | 選択中を閉じる / 終了済みを一掃                                     |
|                | `r`                   | フォーカス配下の終了済み panel を再実行                             |
|                | `g g` / `g c` / `g u` | マーク / マークした panel をまとめる / 解除                         |
|                | `s` / `f` / `D`       | 選択中にシグナル / 検索 / diff                                      |
|                | `/`                   | すべての panel の出力を検索(fleet を grep)                          |
|                | `T` / `Q`             | タスクを投げる / タスクキューを管理                                 |
|                | `v u`                 | 使用量フッタを切り替え: off / 課金ウィンドウ / 対象パネル / 上限    |
|                | `v U`                 | アカウント使用量 — 上限バーと、消費しているパネル                   |
|                | `v k`                 | フッタのキー入力表示を切り替え                                      |
| グループ       | `tab`                 | 次の panel にフォーカス                                             |
|                | `+` / `-`             | ライブタイルを増やす / 減らす                                       |
|                | `L`                   | タイルのレイアウトを順に切り替え                                    |
|                | `p` / `i`             | フォーカス中の panel を pin / 対話                                  |
|                | `enter`               | フォーカス中の panel をズーム                                       |
| ズーム         | 打鍵                  | プログラムを直接操作                                                |
|                | `C-t f` / `C-t G`     | スクロールバックを検索 / git メニュー(agent)                        |

完全なキー一覧は **[docs/KEYS.md](KEYS.md)** を、各画面の背後にある設計は **[docs/SPEC.md](SPEC.md)** を参照してください。

## 機能

ターミナルマルチプレクサにはできないことが五つ:

- **ポーリングしない注意** — fleet はたいてい無事です。画面を見ているのは、無事ではない数個のためでしょう。一本の
  静かな時計がすべての panel を並べます——`running`、10 秒で `idle`、一巡を終えた agent の `done`、長引きすぎた
  `stuck`——そして agent は自分で手を挙げ、この段全体を追い越せます。`C-t a` はどの view からでも受信箱を開き、
  待ち行列はそこで片付きます。`settings.notify` は誰も見ていないときに OSC 9 のデスクトップ通知を出し、まとめて
  送られ、`done` では決して鳴りません。**[docs/ATTENTION.md](ATTENTION.md)** を参照。
- **conductor** — `n C` は fleet をあなたの代わりに動かす agent を開きます。socket 越しに——`baton ctl` あるいは
  `baton mcp` の tool を通じて——panel を起こし、まとめ、signal を送り、他の panel に prompt を渡します。自分の
  ホストを壊せないよう柵が張られています。目標は `$HOME/.baton/CONDUCTOR.md` に。
  **[docs/CONTROL.md](CONTROL.md)** を参照。
- **task とキュー** — `T` は brief を一つの agent に、あるいは work item 全体に配ります。カードに記録され、agent
  の準備ができた時点で届きます。`Q` は永続的なバックログを扱い、サーバ側のスケジューラが空いた agent へ流し込みます。
  Lua の `task.pre` フックは brief を書き換えたり拒否したりでき、`task.change` はそれを見張ります。
- **プロセスツリー全体にかかる上限** — panel が使える CPU・メモリ・プロセス数に上限をかけ、そのプロセスツリー全体を
  そこに抑えます。暴走した build がマシンごと持っていくことはありません。fleet 全体の下限に agent ごとの上書き、
  `C-t R` で動いている fleet に適用、Linux では cgroup v2 で強制——強制できないホストでは panel がそう言います。
  **[docs/LIMITS.md](LIMITS.md)** を参照。
- **panel まで辿れる使用量** — `v u` はフッタの表示を切り替えます:請求ウィンドウの token とコストとカウントダウン
  (`⊙ 1.2M tok · ≈$12.34 API · ⏳ 2:14:31`)、フォーカス中の panel の取り分、あるいはアカウントの rate-limit バー
  (`⊙ 5h ▓▓▓▓▓░░░ 2:14:31 · 7d ▓▓▓░░░░░ 3d4h`)。`v U` は全部を開きます——各 quota ウィンドウ、追加分のクレジット、
  そしてそれを食べている panel。**[docs/USAGE.md](USAGE.md)** を参照。

多くのマルチプレクサにもない、もう四つ:

- **コンテナ隔離** — agent profile ごとの任意機能です。`isolate: docker` にすると、その profile の panel は worktree を
  マウントしたコンテナの中で動きます。image は自分で指定し(Baton は同梱しません)、`mount`・`network`・`env-allow`・
  `user` が他に何を通すかを決め、環境変数は名指ししない限り渡りません。既定は off で、敵対的な agent に対する境界では
  ありません。**[docs/ISOLATION.md](ISOLATION.md)** を参照。
- **fleet 全体を grep** — `/` はすべての panel の出力を一度に検索し、ヒットを panel ごとにまとめて並べます。`enter`
  で選んだものを zoom し、ヒット位置に着地します。`C-t f` は単一の scrollback を正規表現で検索し、スクロールモード
  (`C-t [`)は OSC 52 で選択してコピーするので、SSH 越しでも補助バイナリなしで動きます。
- **agent バックエンド** — Baton は agent CLI の名簿(`claude`、`codex`、`gemini`、`aider`、`opencode`、`grok`)を
  持ち、fleet が動くマシンに実際に入っているものを検出します。`A` は選んだものを起こし、`C-t P` は fleet の既定を
  決めつつ、入っていないものの入手先を並べ、`C-t R` はインストール後に再検出します。自前のものは `panel.agents` に。
- **リモート接続** — `baton --remote` は同じコックピットを別のマシンの fleet につなぎます。使うのは元から使っている
  ssh だけで、待ち受けポートも TLS も Baton 独自の鍵交換もありません。既定は off。`C-t @` が有効化し、ディスクに
  書かれない passkey を発行し、生きている接続を一覧して切断・更新・停止できます。**[docs/REMOTE.md](REMOTE.md)** を参照。

そして、マルチプレクサに期待するコックピットが、どれもキー一つで:

| 機能               | キー            | 内容                                                                                |
| ------------------ | --------------- | ----------------------------------------------------------------------------------- |
| Diff               | `D`             | agent panel の作業ツリー diff——staged と unstaged を一度に、未追跡も                |
| Git                | `C-t G`         | diff・log・status・stage・commit・push・branch・worktree——**[docs/GIT.md](GIT.md)** |
| signal             | `s`             | 選択・フォーカス中のタイル・group 全体に任意の signal                               |
| 検索               | `f`             | fleet をタイトルや group で絞り込む                                                 |
| group のレイアウト | `+` `-` `L`     | 何個をライブタイルで流すか、そして分割の形                                          |
| global shell       | `n h`           | サーバが持つ `$HOME` の素のホスト shell、いつでもキー一つ                           |
| 作業ディレクトリ   | `n .`           | panel は OSC 7 から現在地を覚える——**[docs/RESTART.md](RESTART.md)**                |
| panel のログ       | `C-t l` `C-t L` | panel の出力をファイルへ、そして読み戻す——**[docs/LOGGING.md](LOGGING.md)**         |
| 永続化             | `r`             | fleet は再起動を越えて残り、保持した spec から再実行できる                          |
| 再起動ポリシー     | —               | `panel.restart: on-failure` がバックオフと上限つきで panel を戻す                   |
| ホットリロード     | `C-t R`         | fleet を止めずに設定を再読み込み——デーモンへの `SIGHUP` でも                        |
| 外観               | —               | テーマと自作の分割グリッドは `$HOME/.baton/TUI.yaml`——**[docs/TUI.md](TUI.md)**     |
| スクリーンセーバ   | —               | コックピットが休むと流れるデジタルの雨——**[docs/TUI.md](TUI.md)**                   |
| マウス             | —               | 既定は off、端末自身の選択を残すため                                                |
| 言語               | —               | キー一覧は英語と繁体字中国語——**[docs/TUI.md](TUI.md#language)**                    |

## アーキテクチャ

ヘッドレスな **baton server**(バックグラウンドの常駐プロセス)が、すべての状態とすべてのターミナルを所有します。
差し替え可能なフロントエンドが単一の Unix domain socket 越しにつながり——コマンドは上へ、イベントは下へ——
デタッチして再アタッチしても何ひとつ失いません。

完全な図と対話モデルは **[docs/SPEC.md](SPEC.md)** を参照してください。

## プラグイン

たった 1 つの Lua ファイル(`$HOME/.baton/plug-in.lua`)が、Baton をあなたのワークフローに作り替えます:
ライフサイクルイベントに反応し(agent があなたを必要としたら知らせる、1 つが終わったら次の手順につなぐ)、
fleet を動かし、自分のコマンドを足し、設定を書く——すべてを 1 つの `baton` オブジェクト経由で。
**[docs/PLUGIN.md](PLUGIN.md)** を参照してください。

## ドキュメント

- **[docs/SPEC.md](SPEC.md)** — 完全な仕様:画面、panel のライフサイクル、work item、シグナル、diff、
  永続化、画面ごとのキー一覧、そしてアーキテクチャ図。
- **[docs/ATTENTION.md](ATTENTION.md)** — スケールする attention:静けさのはしご(`done`、`stuck`、failed)、
  `C-t a` の受信箱、ダッシュボードの 2 つの折りたたみ、デスクトップ通知、そしてそれらのすべての設定。
- **[docs/TUI.md](TUI.md)** — コックピットの外観ファイル(`$HOME/.baton/TUI.yaml`):配色テーマと
  グループ分割のレイアウト(プリセットとカスタムグリッド)。
- **[docs/LIMITS.md](LIMITS.md)** — リソース上限:設定、2 つの層、ホットリロード、そして実際にどこで強制されるか。
- **[docs/ISOLATION.md](ISOLATION.md)** — コンテナ隔離:profile ごとの設定、agent が保持するもの、コンテナ内で上限がどう強制されるか、そして何の境界ではないか。
- **[docs/RESTART.md](RESTART.md)** — 再起動ポリシー:何が失敗で何が失敗でないか、バックオフと上限、そして
  `always` が存在しない理由。
- **[docs/GIT.md](GIT.md)** — git メニュー:各操作、commit エディタの流れ、worktree、そして設定。
- **[docs/LOGGING.md](LOGGING.md)** — パネルのログ:何が書かれるか、どこに置かれるか、セッションのマーカー、
  ローテーション、そして何の境界ではないのか。
- **[docs/REMOTE.md](REMOTE.md)** — SSH 越しのリモート接続:`--stdio` ブリッジ、passkey が何であって何でないか、
  `C-t @` の接続一覧、そして報告される失敗。
- **[docs/USAGE.md](USAGE.md)** — アカウント使用量フッタ:ローカルと Admin-API の 2 つのソース、設定、注意点。
- **[docs/PLUGIN.md](PLUGIN.md)** — Lua プラグイン API:`baton` オブジェクト、イベント、コマンド、そして設定。
- **[docs/CONTROL.md](CONTROL.md)** — agent で fleet を動かす:conductor、`baton ctl` CLI、`baton mcp` の
  ツール、そしてガードレール。
- **[docs/SCORE.md](SCORE.md)** — Score、fleet の記憶:`score.md` というファイルとその唯一の取り消し手段、
  ティアの階梯、ランキングの重み、コンパクション、そしてこれが境界ではないもの。
- **[docs/DAEMON.md](DAEMON.md)** — daemon:起動の順序、readiness プローブ、そして `baton` が「サーバーが
  起動しなかった」と言ったときにすること。

## DDD(Dream-Driven Development、夢駆動開発)

本プロジェクトは DDD(夢駆動開発)を実践しています:あらゆる機能は、私が夢見て、必要としたものから作られています。
