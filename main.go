package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const usage = `nichiji — CSV(日時,内容[,メモ])の予定表。1ファイル = 1人か1台

  nichiji [ファイル|ディレクトリ ...]          TUI(縦の表。行 = 日時、列 = ファイル)
  nichiji ls [-days 14] [-past 0] [-u] [ファイル ...]   直近の予定を表示して終わる
  nichiji ls -overdue [ファイル ...]             期限切れ(LATE)だけ。あれば終了コード 1
  nichiji add [-f ファイル] [-d 日時] [-m メモ] 内容   1行追記。-d 無しは未定メモ
  nichiji set -f ファイル (-match 語 | -at 日時) [-status 完了] [-end now|+30|15:20] [-d 日時] [-note 結果] [-text 内容] [-m メモ] [-force]
                                               1件を書き換える(重なると拒否)
  nichiji done -f ファイル -match 語 [-note 結果]  = set -status 完了 -end now
  nichiji free [-len 60] [-from 9:00] [-to 18:00] [-days 14] ファイル ...
                                               全員が空いている枠を探す
  共通: -now "2026-12-24 10:00"   今日を差し替える(確認用)

日時: 2026-10-25 14:00 / 14:00-15:30(終わりつき) / 10/25 / 14:00 / +3 / +3 9:00 / 空=未定
繰り返し: */12/25(毎年) 1990*/8/29(内容の \a が年齢) w=日 10:00-11:00(毎週)
          d=1|d=15(毎月) w=土&b=1(最終土曜) m=1&w=月&a=2(1月第2月曜)
同じファイルで時刻が重なる予定は入れられない(別ファイルは可)
内容の先頭の [予約] [実行中] は未完、[完了] [中止] は済み(TUI では隠れる、v で出す)
CSV の空行と # で始まる行は無視し、保存してもそのまま残る
既定ファイル: $NICHIJI_FILE か ~/.local/share/nichiji/nichiji.csv
`

func main() {
	sub := ""
	args := os.Args[1:]
	if len(args) > 0 && strings.Contains(" add ls free set done ", " "+args[0]+" ") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("nichiji", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	nowS := fs.String("now", "", "今日を差し替える")
	file := fs.String("f", defaultFile(), "add の書込先")
	when := fs.String("d", "", "add の日時")
	days := fs.Int("days", 14, "ls で何日先まで")
	past := fs.Int("past", 0, "ls で何日前から")
	undef := fs.Bool("u", false, "ls で未定も並べる")
	length := fs.Int("len", 60, "free で必要な長さ(分)")
	fromS := fs.String("from", "9:00", "free で1日の探し始め")
	toS := fs.String("to", "18:00", "free で1日の探し終わり")
	var so setOpts
	fs.StringVar(&so.match, "match", "", "set: 内容の部分一致で選ぶ")
	fs.StringVar(&so.at, "at", "", "set: 開始の日時で選ぶ")
	fs.StringVar(&so.end, "end", "", "set: 終わり now / +30 / 15:20")
	fs.StringVar(&so.st, "status", "", "set: 先頭の [状態] を書き換える")
	fs.StringVar(&so.note, "note", "", "set: 内容の末尾に「 | 」で足す")
	fs.StringVar(&so.text, "text", "", "set: 内容を丸ごと置き換える")
	memo := fs.String("m", "", "add / set: メモ欄(3列目)")
	fs.BoolVar(&so.force, "force", false, "set: 重なっても書く")
	full := fs.Bool("full", false, "ls: 端末の幅で切らない")
	od := fs.Bool("overdue", false, "ls: 期限切れ(LATE)だけ。あれば終了コード 1")
	fs.Parse(args)
	if *nowS != "" {
		if err := setNow(*nowS); err != nil {
			die(err)
		}
	}
	switch sub {
	case "set", "done":
		if sub == "done" {
			so.st = "完了"
			if so.end == "" {
				so.end, so.softEnd = "now", true
			}
		}
		so.when, so.memo = *when, *memo
		out, err := setEntry(*file, so)
		if err != nil {
			die(err)
		}
		fmt.Println(out)
		return
	case "add":
		text := strings.Join(fs.Args(), " ")
		if text == "" {
			fs.Usage()
			os.Exit(2)
		}
		raw, err := parseInput(*when, now())
		if err == nil {
			var f *File
			if f, err = load(*file); err == nil {
				e := &Entry{Text: text, Memo: *memo, file: f}
				e.setRaw(raw)
				if o, bad := e.clash(f); bad {
					err = clashMsg(o)
				} else {
					err = appendLine(*file, e)
				}
			}
		}
		if err != nil {
			die(err)
		}
		return
	}
	paths := expandArgs(fs.Args())
	if len(paths) == 0 {
		paths = []string{defaultFile()}
	}
	var files []*File
	for _, p := range paths {
		f, err := load(p)
		if err != nil {
			die(err)
		}
		files = append(files, f)
	}
	if sub == "ls" {
		if *od {
			// 期限切れは過去にあるので、-past を指定しなければ30日前から見る
			p := *past
			if p == 0 {
				p = 30
			}
			if list(os.Stdout, files, p, 0, false, !*full, true) > 0 {
				os.Exit(1)
			}
			return
		}
		list(os.Stdout, files, *past, *days, *undef, !*full, false)
		return
	}
	if sub == "free" {
		from, err1 := hhmm(*fromS)
		to, err2 := hhmm(*toS)
		if err1 != nil || err2 != nil || from >= to {
			die(fmt.Errorf("-from / -to は 9:00 のように、from < to で"))
		}
		freeList(os.Stdout, files, *days, from, to, *length)
		return
	}
	m := newModel(files)
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		die(err)
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
