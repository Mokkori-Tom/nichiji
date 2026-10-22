package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const usage = `nichiji - a tiny planner on CSV files (datetime,text[,memo]). One file = one person or one machine.

  nichiji [FILE|DIR ...]                       TUI: one vertical table, rows = datetimes, columns = files
  nichiji ls [-days 14] [-past 0] [-u] [FILE ...]  list upcoming entries
  nichiji ls -overdue [FILE ...]               only LATE entries; exit 1 if any
  nichiji check [FILE ...]                     report unreadable dates, overlaps and LATE entries; exit 1 if any
  nichiji find WORD [FILE ...]                 search text and memo of all entries; exit 1 if none
  nichiji add [-f FILE] [-d WHEN] [-m MEMO] TEXT   append one entry; no -d = undated
  nichiji set -f FILE (-match WORD | -at WHEN) [-status done] [-end now|+30|15:20] [-d WHEN] [-note TEXT] [-text TEXT] [-m MEMO] [-force]
                                               rewrite one entry (refused if it overlaps)
  nichiji done -f FILE -match WORD [-note TEXT]  = set -status done -end now
  nichiji free [-len 60] [-from 9:00] [-to 18:00] [-days 14] FILE ...   gaps where every file is free
  nichiji notify [-before 10] [-cmd CMD] [-watch] [FILE ...]
                                               notify before start and when LATE (once each)
  common: -now "2026-12-24 10:00"   pretend it is this time

WHEN: 2026-10-25 14:00 | 14:00-15:30 (with end) | 10/25 | 14:00 | +3 | +3 9:00 | empty = undated
repeat: */12/25 (yearly) 1990*/8/29 (\a in text = age) w=sun 10:00-11:00 (weekly)
        d=1|d=15 (monthly) w=sat&b=1 (last Saturday) m=1&w=mon&a=2 (2nd Monday of January)
entries with a time may not overlap within one file (different files may)
leading [reserved] [running] = open, [done] [canceled] = closed (hidden in the TUI, v to show)
blank lines and lines starting with # are ignored and kept as they are
default file: $NICHIJI_FILE or ~/.local/share/nichiji/nichiji.csv
`

func main() {
	sub := ""
	args := os.Args[1:]
	if len(args) > 0 && strings.Contains(" add ls free set done find notify check ", " "+args[0]+" ") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("nichiji", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	nowS := fs.String("now", "", "pretend now is this date/time")
	file := fs.String("f", defaultFile(), "file to write (add/set/done)")
	when := fs.String("d", "", "date/time")
	days := fs.Int("days", 14, "days ahead")
	past := fs.Int("past", 0, "days back")
	undef := fs.Bool("u", false, "ls: also list undated")
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
	before := fs.Int("before", 10, "notify: minutes before start")
	ncmd := fs.String("cmd", "", "notify: command to run with title and body (default termux-notification / notify-send / terminal)")
	watch := fs.Bool("watch", false, "notify: keep running, check every minute")
	fs.Parse(args)
	if *nowS != "" {
		if err := setNow(*nowS); err != nil {
			die(err)
		}
	}
	switch sub {
	case "set", "done":
		if sub == "done" {
			so.st = "done"
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
	m := newModel(files)
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		die(err)
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
