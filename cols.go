package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
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

// slotOccs は t から step 分の間に始まる予定と、それより前から続いている予定を返す。
func slotOccs(os, spans []occ, d time.Time, t, step int) (starts, ongoing []occ) {
	for _, o := range os {
		if st := mins(o.at, d); o.timed && st >= t && st < t+step {
			starts = append(starts, o)
		}
	}
	for _, o := range spans {
		if o.timed && !o.end.IsZero() && mins(o.at, d) < t && mins(o.end, d) > t {
			ongoing = append(ongoing, o)
		}
	}
	return starts, ongoing
}

// cell は列表示の1マス(ファイル f の、d の t 分からの時間帯。t<0 は終日)。
func (m *model) cell(f *File, d time.Time, t int) row {
	os, spans := spansOn(f.Entries(), d)
	if t < 0 {
		var all []occ
		for _, o := range os {
			if !o.timed {
				all = append(all, o)
			}
		}
		if len(all) == 0 {
			return row{date: d, slot: -1}
		}
		r := occRow(all[0], d)
		r.more = len(all) - 1
		return r
	}
	starts, ong := slotOccs(os, spans, d, t, m.step)
	switch {
	case len(starts) > 0:
		r := occRow(starts[0], d)
		r.slot, r.more = t, len(starts)-1
		return r
	case len(ong) > 0:
		r := occRow(ong[0], d)
		r.slot, r.cont, r.more = t, true, len(ong)-1
		return r
	}
	return row{date: d, slot: t}
}

var stFree = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))

func fit(s string, w int) string {
	return runewidth.FillRight(runewidth.Truncate(s, w, "…"), w)
}

// colsBody は時間を縦、ファイル(人・機械)を横に並べる。選んでいる列 = 書込先。
func (m *model) colsBody(bodyH int) []string {
	const lw = 6
	n := len(m.files)
	cw := max(4, (m.w-lw)/n)
	hdr := strings.Repeat(" ", lw)
	for i, f := range m.files {
		st := stDim
		if i == m.target {
			st = stHead.Underline(true)
		}
		hdr += st.Render(fit(f.Name(), cw-1)) + " "
	}
	out := []string{hdr}
	rs := m.rows()
	h := max(1, bodyH-1)
	if m.sel < m.off {
		m.off = m.sel
	}
	if m.sel >= m.off+h {
		m.off = m.sel - h + 1
	}
	today := day(now())
	nm := mins(now(), today)
	for i := m.off; i < len(rs) && i < m.off+h; i++ {
		r := rs[i]
		cells := make([]row, n)
		busy := false
		for j, f := range m.files {
			cells[j] = m.cell(f, r.date, r.slot)
			if cells[j].e != nil {
				busy = true
			}
		}
		label, lst := "終日", stDim
		if r.slot >= 0 {
			label = fmt.Sprintf("%02d:%02d", r.slot/60, r.slot%60)
			switch {
			case r.date.Equal(today) && nm >= r.slot && nm < r.slot+m.step:
				lst = stToday
			case !busy:
				lst = stFree // 全員空いている
			}
		}
		var b strings.Builder
		b.WriteString(lst.Render(fit(label, lw)))
		for j, c := range cells {
			txt, st := "·", stDim
			if c.e != nil {
				txt, st = c.e.textOn(r.date), stPlain
				if c.cont {
					txt, st = "│"+txt, stDim
				} else if c.timed {
					if _, bad := c.e.clashOn(c.e.file, day(c.at)); bad {
						txt, st = "!"+txt, stWarn
					}
				}
				if c.more > 0 {
					txt += fmt.Sprintf("+%d", c.more)
				}
			}
			txt = strings.ReplaceAll(txt, "\n", "⏎")
			cellS := fit(txt, cw-1)
			if i == m.sel && j == m.target {
				st = stSel
			}
			b.WriteString(st.Render(cellS) + " ")
		}
		out = append(out, b.String())
	}
	return out
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
