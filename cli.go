package main

import (
	"fmt"
	"io"
	"os"
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
		return fmt.Errorf("-now は日付か日時で: %q", s)
	}
	return nil
}

// listSegs は ls の1件。印(! / LATE)は grep できるよう英字のまま本文の前に置く。
func (m *model) listSegs(o occ) []seg {
	e := o.e
	when := "未定"
	switch {
	case e.Kind == kUndef && e.Raw != "":
		when = "?" + e.Raw
	case o.timed && !o.end.IsZero():
		when = o.at.Format("15:04") + "-" + endLabel(o.at, o.end)
	case o.timed:
		when = o.at.Format("15:04")
	case e.Kind != kUndef:
		when = "終日"
	}
	segs := []seg{{"  " + when + " ", stPlain}}
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
		segs = append(segs, seg{"  | " + strings.ReplaceAll(e.Memo, "\n", "⏎"), stDim})
	}
	return segs
}

// list は when の既定表示の真似。予定のある日だけを並べる。
// list は直近を出す。overdueOnly なら期限切れ(LATE)だけを出し、その件数を返す。
func list(w io.Writer, files []*File, past, days int, undef, cut, overdueOnly bool) int {
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
	m := &model{files: files}
	today := day(now())
	n := 0
	for i := -past; i <= days; i++ {
		d := today.AddDate(0, 0, i)
		os := occsOn(m.all(), d)
		if overdueOnly {
			var od []occ
			for _, o := range os {
				if overdue(o) {
					od = append(od, o)
				}
			}
			os = od
		}
		if len(os) == 0 {
			continue
		}
		head := dateLabel(d)
		st := stHead
		if i == 0 {
			st = stToday
		}
		fmt.Fprintln(w, st.Render(head))
		for _, o := range os {
			fmt.Fprintln(w, render(m.listSegs(o)))
			n++
		}
	}
	if overdueOnly {
		return n
	}
	if n == 0 {
		fmt.Fprintln(w, stDim.Render(fmt.Sprintf("%d日先まで予定なし", days)))
	}
	var us []*Entry
	for _, e := range m.all() {
		if e.Kind == kUndef {
			us = append(us, e)
		}
	}
	if len(us) == 0 {
		return n
	}
	if !undef {
		fmt.Fprintln(w, stDim.Render(fmt.Sprintf("未定 %d件(-u で表示)", len(us))))
		return n
	}
	fmt.Fprintln(w, stHead.Render("未定"))
	for _, e := range us {
		fmt.Fprintln(w, render(m.listSegs(occ{e: e})))
	}
	return n
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
		return 0, fmt.Errorf("時刻が変: %q", s)
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
		fmt.Fprintln(w, stDim.Render(fmt.Sprintf("%d日先まで %d分の空きなし", days, length)))
	}
}

// endLabel は 24:00 を 00:00 と書かないため。
func endLabel(s, e time.Time) string {
	if !day(e).Equal(day(s)) && e.Equal(day(e)) {
		return "24:00"
	}
	return e.Format("15:04")
}
