package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/mattn/go-runewidth"
)

// 80桁の端末で折り返さないよう、1行を79桁までに収める
const usage = `nichiji - a tiny planner on CSV files (datetime,text[,memo]).
One file = one person or one machine.

  nichiji [ls] [-days 14] [-past 0] [-memo] [-full] [-all] [FILE|DIR ...]
      list upcoming entries and undated ones, today at the bottom
      (* = has a memo); [done] [canceled] only with -all;
      warns about LATE entries on earlier days (up to 30 days back)
  nichiji ls -notime [FILE ...]
      entries without a time (undated first, then by date); exit 1 if any
  nichiji ls -overdue [FILE ...]
      only LATE entries; exit 1 if any
  nichiji check [FILE ...]
      report unreadable dates, overlaps, LATE, broken records, 4+ columns;
      exit 1 if any
  nichiji find WORD [FILE ...]
      fuzzy search of text and memo; exit 1 if none
  nichiji add [-f FILE] [-d WHEN] [-m MEMO] TEXT
      append one entry ("10/30 19:00 dinner" works too); no time = undated
      -f -d -m may also follow TEXT; text after -- is taken as it is
  nichiji set [-f FILE] (WORD | -match WORD | -at WHEN) [-status done]
              [-end now|+30|15:20] [-d WHEN] [-note TEXT] [-text TEXT]
              [-m MEMO] [-force]
      rewrite one entry (refused if it overlaps); Tab after WORD fills in
      the current -d -text -m to overwrite; -d "" = undated, -m "" = no memo
      Tab skips [done] and [canceled] entries unless -all is given
  nichiji done [-f FILE] (WORD | -match WORD | -at WHEN) [-note TEXT]
      = set -status done -end now
  nichiji rm [-f FILE] (WORD | -match WORD | -at WHEN)
      remove one entry
  nichiji uniq FILE FILE ...
      entries found in only one of the files (same date and text = same);
      exit 1 if any
  nichiji when [WHEN]
      how dates are written, read against today; with WHEN, only that one
  nichiji free [-len 60] [-from 9:00] [-to 18:00] [-days 14] [FILE ...]
      gaps where every file is free
  nichiji notify [-before 10] [-cmd CMD] [-watch] [FILE ...]
      notify before start and when LATE (once each), and daily about entries
      without a time and LATE entries on earlier days; with no -cmd, shown
      both on every attached tmux client and as a desktop/phone notification
  nichiji completion bash|fish
      print tab completion; add to ~/.bashrc: eval "$(nichiji completion bash)"
      or to config.fish: nichiji completion fish | source

  common: -now "2026-12-24 10:00"   pretend it is this time;  nichiji -version
  options go before the files; an option a command does not use is an error
  to edit by hand, open the CSV in your editor and run nichiji check afterwards

WHEN: 2026-10-25 14:00 | 14:00-15:30 (with end) | 10/25 | 14:00 | 2pm | +3
      | +3 9:00 | empty = undated
range: 12/24~12/26 (every day, both ends included) 12/24~26 9:00-17:00
repeat: */12/25 (yearly) 1990*/8/29 (\a in text = age)
        w=sun 10:00-11:00 (weekly) d=1|d=15 (monthly) w=sat&b=1 (last Saturday)
        m=1&w=mon&a=2 (2nd Monday of January)
every entry is a slot of time: undated, date-only and untimed repeats are
"no time" until you give them a time
entries with a time may not overlap within one file (different files may)
CSV per RFC 4180: quote cells with commas or line breaks;
a "# nichiji: label" line makes a file a label (e.g. weekdays)
leading [reserved] [running] = open, [done] [canceled] = closed
blank lines and lines starting with # are ignored and kept as they are
default file: $NICHIJI_FILE or ~/.local/share/nichiji/nichiji.csv
`

func main() {
	// 落ちたときは、不具合だと分かる1行を先に出す(その下の詳しい行は報告用)
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "nichiji: internal error (a bug, not your data): %v\nyour files were not changed. please report the lines below: https://github.com/Mokkori-Tom/nichiji/issues\n\n", r)
			debug.PrintStack()
			os.Exit(2)
		}
	}()
	// 罫線(─ │ ┌)や … は、日本語の設定だと幅2と数えられるが、端末(Termux・tmux)は幅1で描くことが多い。
	// ずれると列が崩れるので幅1に揃える。幅2で描く端末なら NICHIJI_AMBIGUOUS_WIDE=1。
	runewidth.DefaultCondition.EastAsianWidth = os.Getenv("NICHIJI_AMBIGUOUS_WIDE") == "1"
	sub := ""
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "__complete" {
		complete(args[1:])
		return
	}
	if len(args) > 0 && args[0] == "completion" {
		completion(args[1:])
		return
	}
	if len(args) > 0 && strings.Contains(" add ls free set done rm find notify check uniq when ", " "+args[0]+" ") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("nichiji", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	nowS := fs.String("now", "", "pretend now is this date/time")
	file := fs.String("f", defaultFile(), "file to write (add/set/done)")
	when := fs.String("d", "", "date/time")
	days := fs.Int("days", 14, "days ahead")
	past := fs.Int("past", 0, "days back")
	lall := fs.Bool("all", false, "ls: also show [done] and [canceled]; set/done/rm: Tab also offers them")
	fs.Bool("u", false, "ls: no effect, undated entries are always listed (kept for old scripts)")
	length := fs.Int("len", 60, "free: minutes needed")
	fromS := fs.String("from", "9:00", "free: day starts")
	toS := fs.String("to", "18:00", "free: day ends")
	var so setOpts
	fs.StringVar(&so.match, "match", "", "set: pick by substring of the text")
	fs.StringVar(&so.at, "at", "", "set: pick by start date/time")
	fs.StringVar(&so.end, "end", "", "set: end now / +30 / 15:20")
	fs.StringVar(&so.st, "status", "", "set: replace the leading [status]")
	fs.StringVar(&so.note, "note", "", "set: append \" | note\" to the text")
	fs.StringVar(&so.text, "text", "", "set: replace the text")
	memo := fs.String("m", "", "add/set: memo (3rd column)")
	fs.BoolVar(&so.force, "force", false, "set: write even if it overlaps")
	full := fs.Bool("full", false, "ls: do not cut lines to the terminal width")
	od := fs.Bool("overdue", false, "ls: only LATE entries; exit 1 if any")
	lmemo := fs.Bool("memo", false, "ls: show memos (default: only a * mark)")
	notime := fs.Bool("notime", false, "ls: only entries without a time (undated, date-only, untimed repeats)")
	before := fs.Int("before", 10, "notify: minutes before start")
	ncmd := fs.String("cmd", "", "notify: command to run with title and body (default termux-notification / notify-send / Windows toast on WSL / terminal)")
	watch := fs.Bool("watch", false, "notify: keep running, check every minute")
	ver := fs.Bool("version", false, "print the version")
	fs.Parse(args)
	// set / done / rm は語をそのまま書けば -match と同じ。後ろのオプションも読む
	if (sub == "set" || sub == "done" || sub == "rm") && fs.NArg() > 0 && so.match == "" && so.at == "" {
		so.match = fs.Arg(0)
		fs.Parse(fs.Args()[1:])
	}
	if *ver {
		fmt.Println(version())
		return
	}
	checkFlags(fs, sub)
	if *nowS != "" {
		if err := setNow(*nowS); err != nil {
			die(err)
		}
	}
	switch sub {
	case "rm", "set", "done":
		// 引用し忘れた -d 11/1 23:00 の 23:00 などを黙って捨てない
		if fs.NArg() > 0 {
			die(fmt.Errorf("unexpected argument %q: quote a value with spaces (-d \"11/1 23:00\")", fs.Arg(0)))
		}
	}
	switch sub {
	case "rm":
		f, err := load(*file)
		if err == nil {
			var e *Entry
			if e, err = findEntry(f, so.match, so.at); err == nil {
				f.remove(e)
				if err = save(f); err == nil {
					fmt.Println("removed:", e.Raw, short(e.Text, 60))
				}
			}
		}
		if err != nil {
			die(err)
		}
		return
	case "set", "done":
		if sub == "done" {
			so.st = "done"
			if so.end == "" {
				so.end, so.softEnd = "now", true
			}
		}
		so.when, so.memo = *when, *memo
		fs.Visit(func(fl *flag.Flag) {
			switch fl.Name {
			case "d":
				so.whenSet = true
			case "m":
				so.memoSet = true
			case "text":
				so.textSet = true
			}
		})
		out, err := setEntry(*file, so)
		if err != nil {
			die(err)
		}
		fmt.Println(out)
		return
	case "add":
		words := fs.Args()
		if n := len(args) - fs.NArg(); n == 0 || args[n-1] != "--" {
			words = addArgs(fs, words)
		}
		if *when != "" {
			for _, w := range words {
				if reHHMM.MatchString(w) {
					die(fmt.Errorf("unexpected argument %q: quote a value with spaces (-d \"11/1 23:00\")", w))
				}
			}
		}
		text := strings.Join(words, " ")
		if text == "" {
			fs.Usage()
			os.Exit(2)
		}
		if *when == "" { // 「10/30 19:00 飲み会」のように先頭に日時があればそれを使う
			if r, t, ok := splitWhen(text, now()); ok {
				*when, text = r, t
			}
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
	if sub == "find" {
		if fs.NArg() == 0 {
			die(fmt.Errorf("usage: nichiji find WORD [FILE ...]"))
		}
		q, rest := fs.Arg(0), fs.Args()[1:]
		ps := expandArgs(rest)
		if len(ps) == 0 {
			ps = []string{defaultFile()}
		}
		var fl []*File
		for _, p := range ps {
			f, err := load(p)
			if err != nil {
				die(err)
			}
			fl = append(fl, f)
		}
		if find(os.Stdout, fl, q) == 0 {
			os.Exit(1)
		}
		return
	}
	if sub == "when" {
		if err := whenHelp(os.Stdout, fs.Args()); err != nil {
			die(err)
		}
		return
	}
	if sub == "notify" {
		ps := expandArgs(fs.Args())
		if len(ps) == 0 {
			ps = []string{defaultFile()}
		}
		if *watch {
			notifyWatch(ps, *before, *ncmd)
		}
		if err := notifyOnce(ps, *before, *ncmd); err != nil {
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
	if sub == "ls" || sub == "" { // コマンドを付けなければ ls と同じ(TUI は無くした)
		switch {
		case *notime:
			if listNoTime(os.Stdout, files) > 0 {
				os.Exit(1)
			}
		case *od:
			// 期限切れは過去にあるので、-past を指定しなければ30日前から見る
			p := *past
			if p == 0 {
				p = lateDays
			}
			if list(os.Stdout, files, p, 0, !*full, true, *lmemo, true) > 0 {
				os.Exit(1)
			}
		default:
			list(os.Stdout, files, *past, *days, !*full, false, *lmemo, *lall)
		}
		return
	}
	if sub == "uniq" {
		if len(files) < 2 {
			die(fmt.Errorf("usage: nichiji uniq FILE FILE ..."))
		}
		if uniq(os.Stdout, files) > 0 {
			os.Exit(1)
		}
		return
	}
	if sub == "check" {
		if check(os.Stdout, files) > 0 {
			os.Exit(1)
		}
		return
	}
	if sub == "free" {
		from, err1 := hhmm(*fromS)
		to, err2 := hhmm(*toS)
		if err1 != nil || err2 != nil || from >= to {
			die(fmt.Errorf("-from/-to like 9:00, with from < to"))
		}
		freeList(os.Stdout, files, *days, from, to, *length)
		return
	}
}

// allowed はコマンドごとに使えるオプション(-now はどれでも)。関係の無いものは打ち間違いとして止める。
var allowed = map[string]string{
	"":       "days past u memo full overdue notime all",
	"ls":     "days past u memo full overdue notime all",
	"uniq":   "",
	"when":   "",
	"rm":     "f match at all",
	"check":  "",
	"find":   "",
	"add":    "f d m",
	"set":    "f match at end status note text m force d all",
	"done":   "f match at end status note text m force d all",
	"free":   "len from to days",
	"notify": "before cmd watch",
}

// addArgs は add の内容の後ろに書いた -f -d -m をオプションとして読み、残りを内容として返す。
// -資料 のような語は内容のまま。-d そのものを内容に入れるなら -- の後ろに書く。
func addArgs(fs *flag.FlagSet, args []string) []string {
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(words, args[i+1:]...)
		}
		name, val, hasVal := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-"), "=")
		if !strings.HasPrefix(a, "-") || (name != "f" && name != "d" && name != "m") {
			words = append(words, a)
			continue
		}
		if !hasVal {
			if i++; i == len(args) {
				die(fmt.Errorf("%s needs a value (to use it as text: nichiji add -- TEXT)", a))
			}
			val = args[i]
		}
		fs.Set(name, val)
	}
	return words
}

func checkFlags(fs *flag.FlagSet, sub string) {
	name := sub
	if name == "" {
		name = "ls"
	}
	ok := " now " + allowed[sub] + " "
	fs.Visit(func(fl *flag.Flag) {
		if !strings.Contains(ok, " "+fl.Name+" ") {
			die(fmt.Errorf("-%s is not used by %s (see nichiji -h)", fl.Name, name))
		}
	})
	// Go の flag はファイル名の後ろのオプションを読まないので、ファイル名と取り違えないよう止める
	for _, a := range fs.Args() {
		if strings.HasPrefix(a, "-") && sub != "add" && sub != "when" {
			die(fmt.Errorf("%s: put options before the files (see nichiji -h)", a))
		}
	}
}

// version は go install で入れたときのモジュールの版(日付と commit を含む)。手元で build したなら (devel)。
func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		v := bi.Main.Version
		for _, st := range bi.Settings {
			if st.Key == "vcs.revision" && len(st.Value) >= 7 {
				v += " " + st.Value[:7]
			}
		}
		return "nichiji " + v
	}
	return "nichiji (unknown)"
}

func die(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
