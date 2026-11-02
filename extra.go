package main

// extra.go: uniq(ファイルどうしの差)と when(日時の書き方)。

import (
	"fmt"
	"io"
	"strings"
)

// entryKey は「同じ予定」かどうかの鍵。日時は正規形にそろえ(10/25 と 2026-10-25 は同じ)、メモは見ない。
func entryKey(e *Entry) string {
	raw := strings.TrimSpace(e.Raw)
	if c, err := parseInput(raw, now()); err == nil { // 年の無い 11/5 は今年として読む
		raw = c
	}
	return raw + "\x00" + strings.TrimSpace(e.Text)
}

// uniq は、並べたファイルのうち1つにしか無い予定を出す。件数を返す。
func uniq(w io.Writer, files []*File) int {
	in := map[string]map[*File]bool{}
	for _, f := range files {
		for _, e := range f.Entries() {
			k := entryKey(e)
			if in[k] == nil {
				in[k] = map[*File]bool{}
			}
			in[k][f] = true
		}
	}
	n := 0
	for _, f := range files {
		for _, e := range f.Entries() {
			if len(in[entryKey(e)]) != 1 {
				continue
			}
			when := e.Raw
			if when == "" {
				when = "undated"
			}
			fmt.Fprintf(w, "%s:%d  %s  %s\n", f.Name(), e.line, when, strings.ReplaceAll(e.Text, "\n", "⏎"))
			n++
		}
	}
	return n
}

// whenExamples は nichiji when で、今日を基準にどう読むかを見せる例。
var whenExamples = []struct{ in, note string }{
	{"2026-12-24 14:00", ""},
	{"12/24 14:00-15:30", "with end"},
	{"12/24", "date only: give it a time later"},
	{"14:00", "today"},
	{"2pm", "today"},
	{"+1 9:00", "tomorrow"},
	{"+7", "in 7 days, date only"},
	{"", ""},
	{"w=sun 10:00-11:00", "every Sunday"},
	{"w=mon|w=thu 9:00", "every Monday and Thursday"},
	{"d=1|d=15 10:00", "1st and 15th of every month"},
	{"w=sat&b=1 9:00", "last Saturday of the month"},
	{"m=1&w=mon&a=2", "2nd Monday of January"},
	{"*/12/25", "every Dec 25"},
	{"1990*/8/29", "every Aug 29 since 1990 (\\a in text = age)"},
}

// whenShow は s をどう読むかを1行で返す。読めなければ err。
func whenShow(s string) (string, error) {
	raw, err := parseInput(s, now())
	if err != nil {
		return "", err
	}
	if raw == "" {
		return "undated", nil
	}
	e := &Entry{}
	e.setRaw(raw)
	out := raw
	if e.Kind == kRepeat {
		for d, i := day(now()), 0; i < 400; d, i = d.AddDate(0, 0, 1), i+1 {
			if _, ok := e.on(d); ok {
				out += "   next " + d.Format("2006-01-02 Mon")
				break
			}
		}
	}
	return out, nil
}

// whenHelp は日時の書き方を、今日を基準にした読み方つきで並べる。WHEN を渡せばそれだけを読む。
func whenHelp(w io.Writer, args []string) error {
	if len(args) > 0 {
		out, err := whenShow(strings.Join(args, " "))
		if err != nil {
			return err
		}
		fmt.Fprintln(w, out)
		return nil
	}
	fmt.Fprintf(w, "how dates are read (now %s):\n", now().Format("2006-01-02 Mon 15:04"))
	for _, x := range whenExamples {
		out, err := whenShow(x.in)
		if err != nil {
			out = "error: " + err.Error()
		}
		in := x.in
		if in == "" {
			in = "(empty)"
		}
		fmt.Fprintf(w, "  %-20s -> %s", in, out)
		if x.note != "" {
			fmt.Fprintf(w, "   (%s)", x.note)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "w: sun-sat  d: day of month  m: month  a: n-th weekday  b: n-th from the end;  | = or, & = and")
	fmt.Fprintln(w, "/ and - are the same (12/24 = 12-24); the year is this year if left out")
	return nil
}
