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

`-f` を省くと既定のファイル(`$NICHIJI_FILE` か `~/.local/share/nichiji/nichiji.csv`)を使う。

タブ補完(bash)はコマンド、オプション、`-match` の内容、`-at` の日時に効く。

```sh
echo 'eval "$(nichiji completion bash)"' >> ~/.bashrc
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

## tmux に通知する

`nichiji notify` は `-cmd` のコマンドに題と本文を引数で渡す。始まる10分前(`-before` で変更)に、tmux のステータスラインと履歴へ出す例が `examples/` にある。

```sh
mkdir -p ~/bin ~/.config/systemd/user
install -m 755 examples/tmux-notify ~/bin/tmux-notify
install -m 644 examples/nichiji-notify.service ~/.config/systemd/user/
cat examples/tmux.conf >> ~/.tmux.conf && tmux source-file ~/.tmux.conf
systemctl --user daemon-reload
systemctl --user enable --now nichiji-notify.service
```

- 通知は履歴を開く(`prefix` `N`)まで出たまま。ほかのコマンドも `~/bin/tmux-notify "題"` で同じ履歴に残せる
- `-cmd` の値は空白で区切られるので、パスに空白を入れない
- サービスで動かすときは、`nichiji-notify.service` の `PATH` に tmux の場所を入れる
- ログインしている間だけ動く

## ライセンス

MIT
