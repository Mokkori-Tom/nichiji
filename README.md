# nichiji

CSV(`日時,内容,メモ`)で動く、コマンドラインの小さな予定表です。画面(TUI)は持たず、編集は好きなエディタで CSV を直接、確認とよく使う操作はコマンドで。

- 1ファイル = 1人か1台。同じファイルの中で時間が重なる予定は入れられない
- 予定はどれも、実際に動くための時間枠。時刻の決まっていない予定は、決めるまで促す
- 曜日や祝日もただの CSV として一緒に並べる

## 入れ方

```sh
go install github.com/Mokkori-Tom/nichiji@latest
```

## 使い方

```sh
nichiji examples/曜日.csv tom.csv hana.csv   # 直近の予定(nichiji ls と同じ)
nichiji ls -notime tom.csv                    # 時刻の決まっていない予定
nichiji add -f tom.csv '10/30 19:00-21:00 飲み会'
nichiji check tom.csv                         # エディタで書き換えたあとの確認
nichiji -h                                    # ほかのコマンド(find / set / done / rm / free / notify)
```

## CSV

```csv
日時,内容,メモ
2026-10-22 14:00,歯医者,"保険証を持っていく
会計は現金のみ"
2026-10-22 13:00-15:30,研修
,いつか本を返す
w=日 10:00-11:00,教会
```

- 1列目 日時、2列目 内容、3列目 メモ(無くてよい)。CSV のルール(RFC 4180)どおり
- 日時が空なら未定。繰り返しの書き方は `nichiji -h`
- `# nichiji: label` の行があるファイル(曜日など)は、ほかの予定がある日に添えるだけ
- ファイルを並べ替えるなら、セルの中の改行を壊さないツールで(例: `mlr --csv -N --allow-ragged-csv-input --pass-comments sort -f 1`)

## ライセンス

MIT
