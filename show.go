package main

// show.go: 予定を端末に出すための小物(ls・check・find・free・notify が使う)。

import (
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

var (
	stDim   = lipgloss.NewStyle() // 灰色は透過の背景で見えなくなるので使わない
	stHead  = lipgloss.NewStyle().Bold(true)
	stToday = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	stWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	stPlain = lipgloss.NewStyle()
	stSel   = lipgloss.NewStyle().Reverse(true)
)

// lister は ls などで読み込んだファイルの束。
type lister struct {
	files    []*File
	listMemo bool // ls -memo: メモ本文も出す
}

func (m *lister) all() []*Entry {
	var es []*Entry
	for _, f := range m.files {
		es = append(es, f.Entries()...)
	}
	return es
}

// mins は d の0時から t までの分(前日なら負、翌日なら1440超)。
func mins(t, d time.Time) int { return int(t.Sub(d) / time.Minute) }

// occsOn は d の予定。終日が先、あとは時刻順。
func occsOn(es []*Entry, d time.Time) []occ {
	var os []occ
	for _, e := range es {
		if o, ok := e.on(d); ok {
			os = append(os, o)
		}
	}
	sort.SliceStable(os, func(i, j int) bool {
		if os[i].timed != os[j].timed {
			return !os[i].timed
		}
		return os[i].at.Before(os[j].at)
	})
	return os
}

func isDone(e *Entry) bool {
	s := status(e.Text)
	return s == "done" || s == "canceled" || s == "完了" || s == "中止"
}

// unscheduled は時刻の枠が決まっていない予定。予定表の上の予定はどれも体を動かす時間なので、
// 未定・日付だけ・時刻の無い繰り返しは、時刻を決めるよう促す。label のファイルと済んだ予定は除く。
func unscheduled(e *Entry) bool {
	if isDone(e) {
		return false
	}
	switch e.Kind {
	case kUndef:
		return e.Raw == ""
	case kDate:
		return true
	case kRepeat:
		return e.min < 0
	}
	return false
}

func undatedEntries(files []*File) []*Entry {
	var out []*Entry
	for _, f := range files {
		if f.Label {
			continue
		}
		for _, e := range f.Entries() {
			if unscheduled(e) {
				out = append(out, e)
			}
		}
	}
	return out
}

type seg struct {
	s  string
	st lipgloss.Style
}

// line は幅に収めて描く。選択行は全体を反転する。
func line(segs []seg, w int, selected bool) string {
	var b strings.Builder
	left := w
	plain := ""
	for _, s := range segs {
		if left <= 0 {
			break
		}
		t := s.s
		if runewidth.StringWidth(t) > left {
			t = runewidth.Truncate(t, left, "…")
		}
		left -= runewidth.StringWidth(t)
		plain += t
		b.WriteString(s.st.Render(t))
	}
	if selected {
		return stSel.Render(plain + strings.Repeat(" ", max(0, left)))
	}
	return b.String()
}

func dateLabel(d time.Time) string { return d.Format("2006-01-02") }
