package main

import (
	"fmt"
	"sort"
	"time"
)

// spansOn は d の予定(os)と、時間帯を占めうる予定(前日から続くものを含む)を返す。
func spansOn(es []*Entry, d time.Time) (os, spans []occ) {
	os = occsOn(es, d)
	spans = os
	for _, o := range occsOn(es, d.AddDate(0, 0, -1)) {
		if o.end.After(d) {
			spans = append(spans, o)
		}
	}
	return os, spans
}

// freeSlots は全ファイルが空いている length 分以上の枠を返す(1日の from〜to 分の範囲)。
func freeSlots(files []*File, d time.Time, from, to, length int) [][2]time.Time {
	lo := d.Add(time.Duration(from) * time.Minute)
	hi := d.Add(time.Duration(to) * time.Minute)
	if n := now(); n.After(lo) {
		lo = n
	}
	var busy [][2]time.Time
	for _, f := range files {
		_, spans := spansOn(f.Entries(), d)
		for _, o := range spans {
			if s, e, ok := span(o); ok {
				busy = append(busy, [2]time.Time{s, e})
			}
		}
	}
	sort.Slice(busy, func(i, j int) bool { return busy[i][0].Before(busy[j][0]) })
	var out [][2]time.Time
	cur := lo
	for _, b := range append(busy, [2]time.Time{hi, hi}) {
		if b[0].After(hi) {
			b[0] = hi
		}
		if st := ceil5(cur); b[0].Sub(st) >= time.Duration(length)*time.Minute && st.Before(hi) {
			out = append(out, [2]time.Time{st, b[0]})
		}
		if b[1].After(cur) {
			cur = b[1]
		}
	}
	return out
}

// ceil5 は枠の始まりを5分単位に切り上げる(終わり無しの予定は1分だけ占めるので 16:01 などを避ける)。
func ceil5(t time.Time) time.Time {
	c := t.Truncate(5 * time.Minute)
	if c.Before(t) {
		c = c.Add(5 * time.Minute)
	}
	return c
}

func durLabel(d time.Duration) string {
	h, mm := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h == 0:
		return fmt.Sprintf("%d分", mm)
	case mm == 0:
		return fmt.Sprintf("%d時間", h)
	}
	return fmt.Sprintf("%d時間%d分", h, mm)
}
