# Baton

> 拡張可能で、agent にやさしいターミナルマルチプレクサ。

[![Release](https://img.shields.io/github/v/release/cmj0121/baton)](https://github.com/cmj0121/baton/releases/latest)
[![License](https://img.shields.io/github/license/cmj0121/baton)](../LICENSE)
[![CI](https://github.com/cmj0121/baton/actions/workflows/ci.yml/badge.svg)](https://github.com/cmj0121/baton/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/cmj0121/baton/branch/main/graph/badge.svg)](https://codecov.io/gh/cmj0121/baton)

[English](../README.md) · [繁體中文](README.zh-TW.md) · **日本語** · [한국어](README.ko.md) ·
[Français](README.fr.md) · [Deutsch](README.de.md) · [Español](README.es.md)

AI coding agent を同時に何本も走らせると、状況はあっという間に混沌とします——ウィンドウをやりくりし、session は
タブのあちこちに散らばり、誰が動いていて、誰が詰まっていて、誰があなたを待っているのかを一目で見られる場所が
ありません。Baton と AI agent の関係は、tmux と shell の関係と同じです。**キーボードだけで操るコックピット**、
すべての agent を所属タスクごとにまとめた即時ダッシュボードで、どれもキー 1 つの距離にあります。指揮棒は
あなたの手の中に。agent たちが演奏し、あなたが指揮する。🎼

![Baton コックピットのデモ——まずキー一覧、panel を起こし、conductor を開き、2 つを work item にまとめ、分割表示と zoom で同じ ? をもう一度](assets/baton-demo.png)

`?` はいま立っている場所のキーを出します。panel を起こし、`n C` で conductor を呼び、`g g` から `g c` で 2 つを
work item にまとめ、そして分割表示の `?` と zoom の `C-t ?` は三つの違う表を見せます。

## はじめる

Baton は単一の静的バイナリです。macOS なら `brew install cmj0121/tap/baton`、Linux なら:

```sh
curl -fsSL https://raw.githubusercontent.com/cmj0121/baton/main/scripts/install.sh | sh
```

……あるいは [Go](https://go.dev) 1.26+ で `go install github.com/cmj0121/baton/cmd/baton@latest`。あとは `baton` を
実行して:

1. **`A`** で agent を起動します(作業ディレクトリを選び、`enter` でここに置き、`w` でブランチに隔離します)。
2. **`enter`** でズームしてその働きを眺め、**`C-t d`** でダッシュボードに戻ります。
3. **`q`** でデタッチ——すべては動き続けます。いつでも `baton` で戻ってこられます。

迷ったら **`?`** が今いる場所のキーを見せてくれます。fleet 全体には、**`n C`** が `$HOME/.baton/CONDUCTOR.md` の
常設命令に従う conductor を開きます——**[docs/CONTROL.md](CONTROL.md)** を参照。

## コンセプト

**panel** は agent CLI か shell が動く 1 つの生きたターミナルです。**work item** は 1 つのタスクに属する panel を
まとめます。**task** は agent に投げる指示書で、agent が空くまでキューで待ちます。**conductor** はあなたの代わりに
残りの fleet を動かす agent です。

これらは 3 つの画面で見ます:**ダッシュボード**(すべての panel をカードか即時ツリーで)、**グループ**(ある
work item の panel が横並びでストリーム)、そして **ズーム**(1 つの panel が唯一のターミナルになり、リーダー
キー `C-t` で外へ出る)。その背後ではヘッドレスなサーバが Unix socket 越しにすべてのターミナルを所有するので、
デタッチして再アタッチしても何ひとつ失いません。**[docs/SPEC.md](SPEC.md)** を参照。

## なぜ Baton か

tmux は窓を渡すだけで、その中身を知りません。Baton はどの pane にも agent がいると前提を置き、そこから組み立てます:

- **誰があなたを必要としているかが見える。** panel ごとに生きた状態があり、人を待っているものは一つの受信箱に
  並びます。ウィンドウを順に巡って探す必要はありません。
- **仕事はまとまったまま。** 1 つのタスクに仕える panel は work item になり、キー 2 つで作れて、一つとして動かせます。
- **渡すのは打鍵ではなく仕事。** 指示書を一つの agent にも group 全体にも配り、あるいは conductor に fleet を
  任せます。
- **fleet には上限がある。** 上限は暴走した build のプロセスツリー全体を抑え、使用量はそれを費やした panel まで
  辿れます。
- **fleet は覚えている。** agent たちが学び続けたことは順位づけされ、次の指示書に渡されます。

Baton はあなたの shell を欲しがってはいません——tmux に住んでいるなら、その中で動かして構いません。

## 機能

- **Attention** — panel を `running`、`idle`、`done`、`stuck` に並べ、`C-t a` が受信箱。
  [ATTENTION](ATTENTION.md)
- **Conductor** — `n C` が `baton ctl` か `baton mcp` 経由で fleet を動かし、自分のホストからは柵で隔てられます。
  [CONTROL](CONTROL.md)
- **Tasks** — `T` は指示書を agent か work item に配り、`Q` はスケジューラが空いた agent へ流し込むバックログ。
- **Score** — agent たちが学び続けたことははしごを登り、すべての指示書の先頭に立ち、`score.md` に残ります。
  [SCORE](SCORE.md)
- **Caps** — CPU・メモリ・プロセス数の上限を panel のプロセスツリー全体にかけます。[LIMITS](LIMITS.md)
- **Usage** — `v u` は token・コスト・quota バーを、`v U` はそれを費やす panel を見せます。[USAGE](USAGE.md)
- **Isolation** — `isolate: docker` で profile の panel を worktree の上のコンテナで動かします。
  [ISOLATION](ISOLATION.md)
- **Fleet grep** — `/` はすべての panel の出力を検索し、`C-t [` はスクロールして OSC 52 でコピー、SSH 越しでも。
- **Backends** — `claude`、`codex`、`gemini`、`aider`、`opencode`、`grok` を検出し、自前のものは `panel.agents` に。
- **Remote** — `baton --remote` は手持ちの ssh で別のマシンの fleet につなぎます。[REMOTE](REMOTE.md)
- **Plugins** — Lua ファイル 1 つ、`$HOME/.baton/plug-in.lua` がイベントをフックし、コマンドを足します。
  [PLUGIN](PLUGIN.md)
- **いつもの機能** — diff(`D`)、git メニュー(`C-t G`)、シグナル(`s`)、panel のログ、シリアルポート、
  再起動ポリシー、ホットリロード(`C-t R`)、テーマと分割レイアウト。

## 基本のキー

ダッシュボードでは各操作が単一キー。ズーム中はキー入力がプログラムを動かすので、リーダーキー `C-t` を前置します。

| 場所           | Key           | 動作                                                             |
| -------------- | ------------- | ---------------------------------------------------------------- |
| ダッシュボード | `jk` / `↑↓`   | カーソルを動かす                                                 |
|                | `enter`       | 選択中のものを開く / ズームする                                  |
|                | `A`           | 新しい agent — 作業ディレクトリのあと、enter でここか `w` で隔離 |
|                | `n C`         | conductor を開く(fleet を動かす agent)                           |
|                | `g g` / `g c` | マーク / マークした panel をまとめる                             |
|                | `T` / `Q`     | タスクを投げる / キューを管理する                                |
|                | `w` / `x x`   | 選択中を閉じる / 終了済みを一掃                                  |
| `C-t` のあと   | `d` / `b`     | ダッシュボードへ跳ぶ / 一段戻る                                  |
|                | `a`           | attention の受信箱 — 人を必要としているものを片づける            |
|                | `q`           | デタッチ(サーバは動き続けます)                                   |

ほかのすべては `C-t k` で再バインドでき、**[docs/KEYS.md](KEYS.md)** にあります。

## ドキュメント

- **使う** — [SPEC](SPEC.md) · [KEYS](KEYS.md) · [ATTENTION](ATTENTION.md) · [TUI](TUI.md) · [GIT](GIT.md) ·
  [LOGGING](LOGGING.md)
- **fleet** — [CONTROL](CONTROL.md) · [SCORE](SCORE.md) · [USAGE](USAGE.md) · [LIMITS](LIMITS.md) ·
  [ISOLATION](ISOLATION.md) · [RESTART](RESTART.md)
- **拡張する** — [PLUGIN](PLUGIN.md) · [REMOTE](REMOTE.md) · [SERIAL](SERIAL.md)
- **内部** — [DAEMON](DAEMON.md)

## DDD(Dream-Driven Development、夢駆動開発)

本プロジェクトは DDD(夢駆動開発)を実践しています:あらゆる機能は、私が夢見て、必要としたものから作られています。
