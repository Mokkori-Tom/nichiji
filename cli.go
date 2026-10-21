package main

import (
	"fmt"
	"io"
	"time"
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

var dayWords = map[int]string{-1: "昨日", 0: "今日", 1: "明日", 2: "明後日"}

// list は when の既定表示の真似。予定のある日だけを並べる。
func list(w io.Writer, files []*File, past, days int, undef bool) {
	m := &model{files: files}
	today := day(now())
	n := 0
	for i := -past; i <= days; i++ {
		d := today.AddDate(0, 0, i)
		os := m.occsOn(d)
		if len(os) == 0 {
			continue
		}
		head := dateLabel(d)
		if wd, ok := dayWords[i]; ok {
			head += " " + wd
		}
		st := stHead
		if i == 0 {
			st = stToday
		}
		fmt.Fprintln(w, st.Render(head))
		for _, o := range os {
			fmt.Fprintln(w, render(m.entrySegs("  ", occRow(o, d))))
			n++
		}
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
		return
	}
	if !undef {
		fmt.Fprintln(w, stDim.Render(fmt.Sprintf("未定 %d件(-u で表示)", len(us))))
		return
	}
	fmt.Fprintln(w, stHead.Render("未定"))
	for _, e := range us {
		fmt.Fprintln(w, render(m.entrySegs("  ", row{e: e, date: today, hour: -1})))
	}
}

func render(segs []seg) string {
	s := ""
	for _, g := range segs {
		s += g.st.Render(g.s)
	}
	return s
}
