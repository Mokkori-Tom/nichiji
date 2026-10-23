package main

import (
	"fmt"
	"io"
	"strings"
)

// problems はファイルを手で書き換えたあとの見落としを拾う:
// 読めない日時、同じファイル内の重なり、終わりを過ぎた [reserved] [running]。
func problems(f *File) []string {
	var out []string
	where := func(e *Entry) string {
		return fmt.Sprintf("%s:%d  %s  %s", f.Name(), e.line, e.Raw, short(e.Text, 40))
	}
	seen := map[[2]*Entry]bool{}
	for _, e := range f.Entries() {
		switch {
		case e.odd:
			out = append(out, fmt.Sprintf("broken? record spans %d lines and swallows lines that look like entries (sorted by line?): %s", e.span, where(e)))
		case e.span > 1:
			out = append(out, fmt.Sprintf("spans %d lines: write newlines as \\n on one line (editing it in the TUI does this): %s", e.span, where(e)))
		}
		if e.cols > 3 {
			out = append(out, fmt.Sprintf("%d columns: an unquoted comma? (read as part of the memo): %s", e.cols, where(e)))
		}
		if e.Kind == kUndef && e.Raw != "" {
			out = append(out, "unreadable date: "+where(e))
			continue
		}
		if o, bad := e.clash(f); bad {
			pair := [2]*Entry{e, o.e}
			if e.line > o.e.line {
				pair = [2]*Entry{o.e, e}
			}
			if !seen[pair] {
				seen[pair] = true
				out = append(out, "overlaps: "+where(pair[0])+"  <->  "+where(pair[1]))
			}
		}
	}
	t := day(now())
	for i := -30; i <= 0; i++ {
		for _, o := range occsOn(f.Entries(), t.AddDate(0, 0, i)) {
			if overdue(o) {
				out = append(out, "LATE: "+where(o.e))
			}
		}
	}
	return out
}

// check は問題を出して数を返す。
func check(w io.Writer, files []*File) int {
	n := 0
	for _, f := range files {
		for _, p := range problems(f) {
			fmt.Fprintln(w, p)
			n++
		}
	}
	return n
}

// checkMsg は TUI 用の1行。
func checkMsg(f *File) string {
	ps := problems(f)
	if len(ps) == 0 {
		return ""
	}
	s := strings.ReplaceAll(ps[0], "  ", " ")
	if len(ps) > 1 {
		s += fmt.Sprintf("  (+%d more: nichiji check)", len(ps)-1)
	}
	return s
}
