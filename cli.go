package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
)

var nowShift time.Duration

func now() time.Time { return time.Now().Add(nowShift) }

// setNow は -now の指定で「今」をずらす。日付だけなら時刻は今のまま。
func setNow(s string) error {
	raw, err := parseInput(s, time.Now())
	if err != nil {
		return err
	}
	t, k := parseWhen(raw)
	switch k {
	case kTime:
		nowShift = time.Until(t)
	case kDate:
		nowShift = t.Sub(day(time.Now()))
	default:
		return fmt.Errorf("-now needs a date or date/time: %q", s)
	}
	return nil
}

// listSegs は ls の1件。印(! / LATE)は grep できるよう英字のまま本文の前に置く。
func (m *lister) listSegs(o occ) []seg {
	e := o.e
	when := "" // 未定は見出し undated の下に並ぶので、行には書かない
	switch {
	case e.Kind == kUndef && e.Raw != "":
		when = "?" + e.Raw
	case o.timed && !o.end.IsZero():
		when = o.at.Format("15:04") + "-" + endLabel(o.at, o.end)
	case o.timed:
		when = o.at.Format("15:04")
	case e.Kind != kUndef:
		when = "all-day"
	}
	if when != "" {
		when += " "
	}
	segs := []seg{{"  " + when, stPlain}}
	if len(m.files) > 1 {
		segs = append(segs, seg{"[" + e.file.Name() + "] ", stDim})
	}
	if o.timed {
		if _, bad := e.clashOn(e.file, day(o.at)); bad {
			segs = append(segs, seg{"! ", stWarn})
		}
		if overdue(o) {
			segs = append(segs, seg{"LATE ", stHead})
		}
	}
	segs = append(segs, seg{strings.ReplaceAll(e.textOn(day(o.at)), "\n", "⏎"), stPlain})
	if e.Memo != "" {
		// メモ本文は -memo のときだけ。ふだんは印だけにして一覧を崩さない
		if m.listMemo {
			segs = append(segs, seg{"  | " + strings.ReplaceAll(e.Memo, "\n", "⏎"), stDim})
		} else {
			segs = append(segs, seg{" *", stDim})
		}
	}
	return segs
}

// list は直近を出す。シェルでは最後の行がプロンプトのすぐ上に来るので、
// 未定と知らせを上に、日の並びは遠い日から今日へ、今日をいちばん下に出す。
// 済んだ予定([done] [canceled])は all のときだけ出す。
// overdueOnly なら期限切れ(LATE)だけを出し、その件数を返す。
func list(w io.Writer, files []*File, past, days int, cut, overdueOnly, memo, all bool) int {
	width := 0 // 0 = 切らない(パイプのときは全文を出す)
	if fd := os.Stdout.Fd(); cut && term.IsTerminal(fd) {
		if cw, _, err := term.GetSize(fd); err == nil {
			width = cw
		}
	}
	render := func(segs []seg) string {
		if width > 0 {
			return line(segs, width, false)
		}
		return render(segs)
	}
	m := &lister{files: files, listMemo: memo}
	today := day(now())
	n := 0
	var blocks [][]string // 日ごとの行(見出しと予定)。日付の順に集めて、最後に逆から出す
	for i := -past; i <= days; i++ {
		d := today.AddDate(0, 0, i)
		var os []occ
		for _, o := range occsOn(m.all(), d) {
			if (overdueOnly && !overdue(o)) || (!all && isDone(o.e)) {
				continue
			}
			os = append(os, o)
		}
		if len(os) == 0 {
			continue
		}
		st := stHead
		if i == 0 {
			st = stToday
		}
		b := []string{st.Render(dateLabel(d))}
		for _, o := range os {
			b = append(b, render(m.listSegs(o)))
			n++
		}
		blocks = append(blocks, b)
	}
	defer func() {
		for i := len(blocks) - 1; i >= 0; i-- {
			for _, l := range blocks[i] {
				fmt.Fprintln(w, l)
			}
		}
	}()
	if overdueOnly {
		return n
	}
	var us []*Entry
	for _, e := range m.all() {
		if e.Kind == kUndef && !isDone(e) { // 済んだ予定は未定に数えない
			us = append(us, e)
		}
	}
	if len(us) > 0 {
		// 日時を決める気になるよう、件数だけでなく中身も常に見せる
		fmt.Fprintln(w, stHead.Render("undated"))
		for _, e := range us {
			fmt.Fprintln(w, render(m.listSegs(occ{e: e})))
		}
	}
	if nt := len(undatedEntries(files)); nt > 0 {
		fmt.Fprintln(w, stWarn.Render(fmt.Sprintf("%d entries have no time: schedule them (nichiji ls -notime)", nt)))
	}
	// 一覧に出ていない過去の日の LATE は見落としやすいので、済ませるか取り消すよう促す
	if nl := len(pastLate(files, past)); nl > 0 {
		fmt.Fprintln(w, stWarn.Render(fmt.Sprintf("%d past entries are not done (LATE): check them (nichiji ls -overdue)", nl)))
	}
	if n == 0 {
		fmt.Fprintln(w, stDim.Render(fmt.Sprintf("nothing in the next %d days", days)))
	}
	return n
}

// lateDays は、過去の LATE をさかのぼって探す日数(ls -overdue・check と同じ)。
const lateDays = 30

// pastLate は、昨日から lateDays 日前までのうち、ls の -past で見せている日より前にある LATE。
// 古い順に返す。label のファイルは除く。
func pastLate(files []*File, past int) []occ {
	var out []occ
	today := day(now())
	for i := -lateDays; i < -past && i < 0; i++ {
		for _, f := range files {
			if f.Label {
				continue
			}
			for _, o := range occsOn(f.Entries(), today.AddDate(0, 0, i)) {
				if overdue(o) {
					out = append(out, o)
				}
			}
		}
	}
	return out
}

func render(segs []seg) string {
	s := ""
	for _, g := range segs {
		s += g.st.Render(g.s)
	}
	return s
}

func hhmm(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || h > 24 || m > 59 {
		return 0, fmt.Errorf("bad time: %q", s)
	}
	return h*60 + m, nil
}

// freeList は読み込んだ全ファイル(全員)が空いている枠を日ごとに並べる。
func freeList(w io.Writer, files []*File, days, from, to, length int) {
	today := day(now())
	n := 0
	for i := 0; i <= days; i++ {
		d := today.AddDate(0, 0, i)
		fs := freeSlots(files, d, from, to, length)
		if len(fs) == 0 {
			continue
		}
		head := dateLabel(d)
		fmt.Fprintln(w, stHead.Render(head))
		for _, f := range fs {
			fmt.Fprintf(w, "  %s-%s %s\n", f[0].Format("15:04"), endLabel(f[0], f[1]), stDim.Render("("+durLabel(f[1].Sub(f[0]))+")"))
			n++
		}
	}
	if n == 0 {
		fmt.Fprintln(w, stDim.Render(fmt.Sprintf("no %[2]d-minute gap in the next %[1]d days", days, length)))
	}
}

// endLabel は 24:00 を 00:00 と書かないため。
func endLabel(s, e time.Time) string {
	if !day(e).Equal(day(s)) && e.Equal(day(e)) {
		return "24:00"
	}
	return e.Format("15:04")
}

// listNoTime は時刻の決まっていない予定を、未定を先に、あとは日付の近い順に出す。件数を返す。
func listNoTime(w io.Writer, files []*File) int {
	es := undatedEntries(files)
	sortByWhen(es)
	for _, e := range es {
		when := e.Raw
		if when == "" {
			when = "undated"
		}
		fmt.Fprintf(w, "%s:%d  %s  %s\n", e.file.Name(), e.line, when, strings.ReplaceAll(e.Text, "\n", "⏎"))
	}
	return len(es)
}

// sortByWhen は未定を先に、あとは日付の近い順(繰り返しは今日以降の次の回)。
func sortByWhen(es []*Entry) {
	key := func(e *Entry) time.Time {
		switch e.Kind {
		case kDate, kTime:
			return e.At
		case kRepeat:
			for d, i := day(now()), 0; i < 400; d, i = d.AddDate(0, 0, 1), i+1 {
				if _, ok := e.on(d); ok {
					return d
				}
			}
		}
		return time.Time{}
	}
	sort.SliceStable(es, func(i, j int) bool { return key(es[i]).Before(key(es[j])) })
}
