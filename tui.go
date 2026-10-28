package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// 表示は1つの縦の表だけ。行 = 日時(値のある所だけ)、列 = ファイル。
// 時 → 日 → 月 → 年 は同じ表の目盛りを粗くするだけ。

const (
	lvTime = iota
	lvDay
	lvMonth
	lvYear
)

var lvNames = []string{"hour", "day", "month", "year"}

const (
	slotAll   = -1 // 終日
	slotUndef = -2 // 未定
	slotYear  = -3 // 見出し: 年が変わるところ
	slotMonth = -4 // 見出し: 月が変わるところ
	slotDate  = -5 // 日付だけの行(その下に終日の値と時刻の行が並ぶ)
)

type cell struct {
	os     []occ
	ends   []occ // この行の時刻(日)に終わる予定。表に「… end」と出す。選ぶと元の予定を編集する
	ctx    bool  // その日の日付だけの値を、時刻の行にも添えたもの(曜日など)。操作の対象にしない
	mid    bool  // この行の時刻に、この列の予定が続いている途中(│ を引く)
	n      int   // 月・年の目盛りで、同じ予定がこの期間に何回あるか
	counts []int // mergeSame が数えた回数(os と同じ順)
}

func (c cell) empty() bool { return len(c.os) == 0 && len(c.ends) == 0 }

type trow struct {
	date  time.Time // 行の日(月・年は期間の初日)
	slot  int       // 0時からの分。slotAll / slotUndef
	first bool      // その日の最初の行(日付を書く)
	cells []cell
}

const (
	inNone = iota
	inQuick
	inSlot
	inText
	inMemo
	inWhen
	inSearch
	inDelete
)

type model struct {
	files    []*File
	target   int
	level    int
	rows     []trow
	sel, off int
	anchor   time.Time
	hideDone bool
	w, h     int
	in       textinput.Model
	inMode   int
	inE      *Entry
	inRow    trow
	msg      string
	hint     string
	help     bool
	gPending bool
	search   string
	pick     bool // マスを開いて中の予定を1件ずつ選んでいる
	pickList []occ
	pickSel  int
	ctxFile  []bool         // 「# nichiji: label」のファイル(曜日など)。自分では行を作らず、ほかの予定のある日に添えるだけ
	freeAt   time.Time      // f で選んでいる空きの始まり(A でここに足す)。ゼロなら無し
	freeGaps [][2]time.Time // f / F で巡る空きの一覧(終わりがゼロの最後の1つは「ここから先ずっと」)
	freeIdx  int
	reg      *Entry // y / x で取った予定(p で貼る)。file は持たない
	memoView bool   // 選んだ予定の内容とメモを全文で見ている
	memoOff  int
	listMemo bool // ls 用: メモ本文も出す
}

var (
	stSel   = lipgloss.NewStyle().Reverse(true)
	stDim   = lipgloss.NewStyle() // 灰色は透過の背景で見えなくなるので使わない(見分けは └ などの印で)
	stHead  = lipgloss.NewStyle().Bold(true)
	stToday = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	stWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	stPlain = lipgloss.NewStyle()
	tagCols = []string{"4", "5", "2", "1", "6", "3"}
)

func newModel(files []*File) *model {
	ti := textinput.New()
	ti.CharLimit = 0
	m := &model{files: files, w: 80, h: 24, in: ti, hideDone: true, anchor: day(now())}
	// 書込先の既定は、label のファイル(曜日など)を飛ばした最初のファイル
	for j, f := range files {
		if !f.Label {
			m.target = j
			break
		}
	}
	m.build()
	m.jumpNow()
	if n := len(undatedEntries(files)); n > 0 {
		m.msg = fmt.Sprintf("%d entries have no time: press u to jump to them", n)
	}
	// 壊れた疑いのある行(並べ替えで引用が分断された等)は何より先に知らせる
	for _, f := range files {
		for _, e := range f.Entries() {
			if e.odd {
				m.msg = fmt.Sprintf("%s:%d may be broken (one record swallows %d lines): run nichiji check", f.Name(), e.line, e.span)
			}
		}
	}
	return m
}

func (m *model) Init() tea.Cmd { return tick() }

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

// untimedIn は f の時刻の無い予定を、日付の近い順(繰り返しは今日以降の次の回)に並べる。未定は最後。
func untimedIn(f *File) []*Entry {
	if f.Label {
		return nil
	}
	type it struct {
		e *Entry
		t time.Time
	}
	var its []it
	far := time.Date(9999, 1, 1, 0, 0, 0, 0, time.Local)
	for _, e := range f.Entries() {
		if !unscheduled(e) {
			continue
		}
		t := far
		switch e.Kind {
		case kDate:
			t = e.At
		case kRepeat:
			for d, i := day(now()), 0; i < 400; d, i = d.AddDate(0, 0, 1), i+1 {
				if _, ok := e.on(d); ok {
					t = d
					break
				}
			}
		}
		its = append(its, it{e, t})
	}
	sort.SliceStable(its, func(i, j int) bool { return its[i].t.Before(its[j].t) })
	out := make([]*Entry, len(its))
	for i, x := range its {
		out[i] = x.e
	}
	return out
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

// freeGapsOf は f の空きの一覧: 今から先の、予定と予定のあいだ(15分以上)。0時で区切らず、
// 眠らない機械もあるので時間帯も決めない。最後の予定より後は終わりゼロの1つ(ここから先ずっと)。
func freeGapsOf(f *File, from time.Time) [][2]time.Time {
	horizon := day(from).AddDate(0, 0, 400) // 繰り返しは果てが無いので先は400日まで
	var busy [][2]time.Time
	for d := day(from).AddDate(0, 0, -1); d.Before(horizon); d = d.AddDate(0, 0, 1) {
		for _, o := range occsOn(f.Entries(), d) {
			if st, en, ok := span(o); ok && en.After(from) {
				busy = append(busy, [2]time.Time{st, en})
			}
		}
	}
	sort.Slice(busy, func(i, j int) bool { return busy[i][0].Before(busy[j][0]) })
	var gaps [][2]time.Time
	cur := from
	for _, b := range busy {
		if st := ceil5(cur); b[0].Sub(st) >= 15*time.Minute {
			gaps = append(gaps, [2]time.Time{st, b[0]})
		}
		if b[1].After(cur) {
			cur = b[1]
		}
	}
	if len(busy) == 0 || busy[len(busy)-1][1].Before(horizon.AddDate(0, 0, -7)) {
		gaps = append(gaps, [2]time.Time{ceil5(cur), {}}) // 最後の予定より後
	}
	return gaps
}

// jumpFree は f(dir=1)/ F(dir=-1): 選んでいる列の人の空きへ移る。一覧の端では反対の端へ回る。
func (m *model) jumpFree(dir int) {
	f := m.files[m.target]
	if m.freeAt.IsZero() || len(m.freeGaps) == 0 {
		m.freeGaps = freeGapsOf(f, now())
		m.freeIdx = 0
		if dir < 0 {
			m.freeIdx = len(m.freeGaps) - 1
		}
	} else {
		n := len(m.freeGaps)
		m.freeIdx = (m.freeIdx + dir + n) % n
	}
	g := m.freeGaps[m.freeIdx]
	m.showFree(f, g[0], g[1])
}

// showFree は見つけた空きの手前の行を選び、下の行に空きを出す。end がゼロなら「ここから先ずっと」。
func (m *model) showFree(f *File, st, end time.Time) {
	m.freeAt = st
	m.anchor = day(st)
	m.build()
	m.seek(st)
	if m.sel > 0 && m.rowTime(m.rows[m.sel]).After(st) {
		m.sel--
		m.skipHdr(-1)
	}
	pos := fmt.Sprintf("%d/%d", m.freeIdx+1, len(m.freeGaps))
	when := st.Format("01-02 15:04") + "-"
	switch {
	case end.IsZero():
		m.msg = fmt.Sprintf("free %s %s %s (nothing after this)  A add here, f back to the first, F back", pos, f.Name(), when)
		return
	case day(end).Equal(day(st)) || (end.Equal(day(end)) && day(end).Equal(day(st).AddDate(0, 0, 1))):
		when += endLabel(st, end)
	default:
		when = st.Format("01-02 15:04") + " - " + end.Format("01-02 15:04") // 0時をまたぐ空き
	}
	m.msg = fmt.Sprintf("free %s %s %s (%s)  A add here, f next, F back", pos, f.Name(), when, durLabel(end.Sub(st)))
}

// copyEntry は y / x で取っておく写し(元のファイルとは切り離す)。
func copyEntry(e *Entry) *Entry {
	c := *e
	c.file, c.src, c.line = nil, "", 0
	return &c
}

// paste は p: 取っておいた予定を、カーソルの行の日時・選んでいる列(人)に貼る。
// 時刻の行ならその時刻(長さは元のまま)、日付の行ならその日(元に時刻があれば同じ時刻)、未定の行なら未定。
func (m *model) paste() {
	if m.reg == nil {
		m.msg = "nothing to paste (y to copy, x to cut)"
		return
	}
	if m.level >= lvMonth {
		m.msg = "use the hour or day scale to paste"
		return
	}
	r, ok := m.curRow()
	if !ok {
		r = trow{date: day(now()), slot: slotAll}
	}
	src := m.reg
	f := m.files[m.target]
	e := &Entry{Text: src.Text, Memo: src.Memo, file: f}
	// 元の時刻と長さ
	startMin, length := -1, time.Duration(0)
	switch src.Kind {
	case kTime:
		startMin = mins(src.At, day(src.At))
		if !src.End.IsZero() {
			length = src.End.Sub(src.At)
		}
	case kRepeat:
		if src.min >= 0 {
			startMin = src.min
			if src.emin >= 0 {
				length = endAt(day(now()).Add(time.Duration(src.min)*time.Minute), src.emin).Sub(day(now()).Add(time.Duration(src.min) * time.Minute))
			}
		}
	}
	var at time.Time
	switch {
	case r.slot == slotUndef:
	case r.slot >= 0:
		at = m.rowTime(r)
	case startMin >= 0:
		at = r.date.Add(time.Duration(startMin) * time.Minute)
	default:
		e.setRaw(formatWhen(r.date, kDate))
	}
	if !at.IsZero() {
		raw := formatWhen(at, kTime)
		if length > 0 {
			end := at.Add(length)
			mm := mins(end, day(end))
			raw += fmt.Sprintf("-%02d:%02d", mm/60, mm%60)
		}
		e.setRaw(raw)
	}
	if o, bad := e.clash(f); bad {
		m.msg = "not pasted: " + clashMsg(o).Error()
		return
	}
	f.add(e)
	m.commit(f)
	m.focus(e)
	m.msg = "pasted: " + short(e.Text, 30) + " " + e.Raw
}

// undatedOnly は日時の無い予定(済んでいない、label のファイルは除く)。見出しの下に固定で出す。
func (m *model) undatedOnly() []*Entry {
	var out []*Entry
	for _, e := range undatedEntries(m.files) {
		if e.Kind == kUndef {
			out = append(out, e)
		}
	}
	return out
}

// jumpUntimed は u: 時刻の決まっていない予定へ移る。全員の分を、未定を先に、あとは日付の近い順に、
// 押すたびに次へ巡る。移った先の列も選ぶので、誰の予定か分かる。時刻は s で決める。
func (m *model) jumpUntimed() {
	var all []*Entry
	for _, f := range m.files {
		all = append(all, untimedIn(f)...)
	}
	if len(all) == 0 {
		m.msg = "every entry has a time"
		return
	}
	// 日付の近い順に全員分を並べ直す(untimedIn はファイルごとの順)
	key := func(e *Entry) time.Time {
		switch e.Kind {
		case kDate:
			return e.At
		case kRepeat:
			for d, i := day(now()), 0; i < 400; d, i = d.AddDate(0, 0, 1), i+1 {
				if _, ok := e.on(d); ok {
					return d
				}
			}
		}
		return time.Time{} // 未定は先頭(見出しの下の固定の行に見えているものへ、u 1回で行ける)
	}
	sort.SliceStable(all, func(i, j int) bool { return key(all[i]).Before(key(all[j])) })
	next := 0
	if cur, _ := m.curEntry(); cur != nil {
		for i, e := range all {
			if e == cur {
				next = (i + 1) % len(all)
			}
		}
	}
	e := all[next]
	m.focus(e)
	m.msg = fmt.Sprintf("no time %d/%d: %s  %s  (s to set a time, u for the next)", next+1, len(all), e.file.Name(), short(e.Text, 30))
}

type tickMsg struct{}

// tick は次の分の頭で時計を進める。
func tick() tea.Cmd {
	n := now()
	return tea.Tick(n.Truncate(time.Minute).Add(time.Minute).Sub(n), func(time.Time) tea.Msg { return tickMsg{} })
}

type editedMsg struct{ err error }

// entryEditedMsg は E でエディタに渡した予定1件が戻ってきたとき。
type entryEditedMsg struct {
	e    *Entry
	path string
	err  error
}

const entryHelp = "# nichiji: line 1 = date and time (empty = undated), line 2 = text, the rest = memo. (this line is removed)"

// editEntry は選んだ予定1件だけを一時ファイルにしてエディタで開く。
func (m *model) editEntry(e *Entry) tea.Cmd {
	tmp, err := os.CreateTemp("", "nichiji-entry-*.txt")
	if err != nil {
		m.msg = err.Error()
		return nil
	}
	fmt.Fprintf(tmp, "%s\n%s\n%s\n", entryHelp, e.Raw, e.Text)
	if e.Memo != "" {
		tmp.WriteString(e.Memo + "\n")
	}
	tmp.Close()
	args := editorCmd()
	c := exec.Command(args[0], append(args[1:], "+2", tmp.Name())...)
	path := tmp.Name()
	return tea.ExecProcess(c, func(err error) tea.Msg { return entryEditedMsg{e, path, err} })
}

// applyEntryEdit は E から戻った中身を予定に書き戻す。日時が読めない・重なるときは書かない。
func (m *model) applyEntryEdit(msg entryEditedMsg) {
	defer os.Remove(msg.path)
	if msg.err != nil {
		m.msg = "editor: " + msg.err.Error()
		return
	}
	b, err := os.ReadFile(msg.path)
	if err != nil {
		m.msg = err.Error()
		return
	}
	var ls []string
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != entryHelp { // 説明の行だけ外す(メモの # 見出しは残す)
			ls = append(ls, l)
		}
	}
	for len(ls) < 2 {
		ls = append(ls, "")
	}
	e := msg.e
	text := strings.TrimSpace(ls[1])
	if text == "" {
		m.msg = "not saved: the text (line 2) is empty"
		return
	}
	raw, err := parseInput(ls[0], e.At)
	if err != nil {
		m.msg = "not saved: " + err.Error()
		return
	}
	memo := strings.TrimRight(strings.Join(ls[2:], "\n"), "\n \t")
	if raw == e.Raw && text == e.Text && memo == e.Memo {
		return
	}
	old := *e
	e.setRaw(raw)
	e.Text, e.Memo, e.src = text, memo, ""
	if o, bad := e.clash(e.file); bad {
		*e = old
		m.msg = "not saved: " + clashMsg(o).Error()
		return
	}
	m.commit(e.file)
	m.focus(e)
	m.refreshPick(e)
	m.msg = "saved"
}

// memoEditedMsg は M でエディタに渡したメモが戻ってきたとき。
type memoEditedMsg struct {
	e    *Entry
	path string
	err  error
}

// mins は d の0時から t までの分(前日なら負、翌日なら1440超)。
func mins(t, d time.Time) int { return int(t.Sub(d) / time.Minute) }

func (m *model) all() []*Entry {
	var es []*Entry
	for _, f := range m.files {
		es = append(es, f.Entries()...)
	}
	return es
}

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

func (m *model) visible(e *Entry) bool { return !m.hideDone || !isDone(e) }

// ---- 表を組む ----

// window は表に入れる日の範囲。目盛りが粗いほど広く取る。
func (m *model) window() (time.Time, time.Time) {
	a := day(m.anchor)
	switch m.level {
	case lvMonth:
		return a.AddDate(-3, 0, 0), a.AddDate(3, 0, 0)
	case lvYear:
		return a.AddDate(-10, 0, 0), a.AddDate(10, 0, 0)
	}
	return a.AddDate(0, 0, -120), a.AddDate(0, 0, 240)
}

// markCtx は「# nichiji: label」のファイルに印を付ける。全部がそうなら、どれにも付けない。
// (中身から推測しない: 繰り返しの予定だけを持つ人のファイルも、普通に行を作る)
func (m *model) markCtx() {
	m.ctxFile = make([]bool, len(m.files))
	all := true
	for j, f := range m.files {
		m.ctxFile[j] = f.Label
		all = all && f.Label
	}
	if all {
		m.ctxFile = make([]bool, len(m.files))
	}
}

func (m *model) build() {
	m.markCtx()
	n := len(m.files)
	var rows []trow
	// 未定はいつも一番上
	undef := make([][]occ, n)
	nu := 0
	for j, f := range m.files {
		for _, e := range f.Entries() {
			if e.Kind == kUndef && m.visible(e) {
				undef[j] = append(undef[j], occ{e: e})
			}
		}
		nu = max(nu, len(undef[j]))
	}
	for i := 0; i < nu; i++ {
		r := trow{slot: slotUndef, first: i == 0, cells: make([]cell, n)}
		for j := range m.files {
			if i < len(undef[j]) {
				r.cells[j].os = []occ{undef[j][i]}
			}
		}
		rows = append(rows, r)
	}
	from, to := m.window()
	var group trow // 日・月・年の行をまとめる途中
	flush := func() {
		keep := false
		for j, c := range group.cells {
			if !c.empty() && !m.ctxFile[j] {
				keep = true
			}
		}
		switch {
		case !keep:
		default:
			if m.level >= lvMonth {
				group = mergeSame(group)
			} else {
				rows = append(rows, trow{date: group.date, slot: slotDate, first: true, cells: make([]cell, n)})
			}
			rows = append(rows, splitDay(group)...)
		}
		group = trow{}
	}
	dayOccs := func(d time.Time) [][]occ {
		per := make([][]occ, n)
		for j, f := range m.files {
			for _, o := range occsOn(f.Entries(), d) {
				if m.visible(o.e) {
					per[j] = append(per[j], o)
				}
			}
		}
		return per
	}
	prev := dayOccs(from.AddDate(0, 0, -1))
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		per := dayOccs(d)
		// d に終わる予定(前の日に始まって日付をまたいだものも)
		ends := make([][]occ, n)
		for j := range per {
			for _, o := range append(append([]occ{}, prev[j]...), per[j]...) {
				if o.timed && !o.end.IsZero() && day(o.end).Equal(d) {
					ends[j] = append(ends[j], o)
				}
			}
		}
		prev = per
		if m.level != lvTime {
			key := d
			switch m.level {
			case lvMonth:
				key = time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.Local)
			case lvYear:
				key = time.Date(d.Year(), 1, 1, 0, 0, 0, 0, time.Local)
			}
			if !group.date.Equal(key) {
				flush()
				group = trow{date: key, slot: slotAll, first: true, cells: make([]cell, n)}
			}
			for j := range per {
				if m.level >= lvMonth && m.ctxFile[j] {
					continue // 月・年では曜日などを数えても意味がない
				}
				group.cells[j].os = append(group.cells[j].os, per[j]...)
				if m.level == lvDay {
					for _, o := range ends[j] {
						if !day(o.at).Equal(d) { // 日の目盛りでは、日付をまたいだものだけ
							group.cells[j].ends = append(group.cells[j].ends, o)
						}
					}
				}
			}
			continue
		}
		rows = append(rows, dayRows(d, per, ends, m.ctxFile)...)
	}
	flush()
	m.rows = rows
	m.clamp()
}

// dayRows は「時」の目盛りで1日分の行を作る。細かい方に合わせ、
// 時刻を持たないファイル(曜日など)の値はその日のどの行にも添える。
func dayRows(d time.Time, per, ends [][]occ, ctxFile []bool) []trow {
	n := len(per)
	timed := make([][]occ, n)
	allday := make([][]occ, n)
	keys := map[int]bool{}
	any := false
	for j, os := range per {
		for _, o := range os {
			any = any || !ctxFile[j]
			if o.timed {
				timed[j] = append(timed[j], o)
				keys[mins(o.at, d)] = true
			} else {
				allday[j] = append(allday[j], o)
			}
		}
	}
	for j := range ends {
		for _, o := range ends[j] {
			any = any || !ctxFile[j]
			keys[mins(o.end, d)] = true
		}
	}
	if !any {
		return nil
	}
	// 日付だけの行を先に置き、その下に終日の値(曜日など)、時刻の行と並べる。
	// 左の欄は時刻の幅で足りるので、予定の列を広く取れる
	nAll := 0
	for j := range per {
		nAll = max(nAll, len(allday[j]))
	}
	rows := []trow{{date: d, slot: slotDate, first: true, cells: make([]cell, n)}}
	for i := 0; i < nAll; i++ {
		r := trow{date: d, slot: slotAll, cells: make([]cell, n)}
		for j := range per {
			if i < len(allday[j]) {
				r.cells[j].os = []occ{allday[j][i]}
			}
			for _, o := range ends[j] {
				if !day(o.at).Equal(d) && mins(o.end, d) > 0 {
					r.cells[j].mid = true // 前の日から日付をまたいで続いている
				}
			}
		}
		rows = append(rows, r)
	}
	ks := make([]int, 0, len(keys))
	for k := range keys {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	for _, k := range ks {
		r := trow{date: d, slot: k, cells: make([]cell, n)}
		for j := range per {
			for _, o := range timed[j] {
				if mins(o.at, d) == k {
					r.cells[j].os = append(r.cells[j].os, o)
				}
			}
			for _, o := range ends[j] {
				if mins(o.end, d) == k {
					r.cells[j].ends = append(r.cells[j].ends, o)
				}
			}
			// 続いている途中か(この日に始まったもの、前の日から日付をまたいできたもの)
			for _, o := range append(append([]occ{}, timed[j]...), ends[j]...) {
				if !o.end.IsZero() && mins(o.at, d) < k && mins(o.end, d) > k {
					r.cells[j].mid = true
				}
			}
		}
		rows = append(rows, r)
	}
	return rows
}

// mergeSame は月・年の目盛りで、同じ予定(繰り返しなど)を1件にまとめて回数を数える。
func mergeSame(g trow) trow {
	for j, c := range g.cells {
		var os []occ
		idx := map[*Entry]int{}
		cnt := map[*Entry]int{}
		for _, o := range c.os {
			if _, ok := idx[o.e]; !ok {
				idx[o.e] = len(os)
				os = append(os, o)
			}
			cnt[o.e]++
		}
		g.cells[j].os = os
		g.cells[j].counts = make([]int, len(os))
		for i, o := range os {
			g.cells[j].counts[i] = cnt[o.e]
		}
	}
	return g
}

// splitDay は日・月・年の目盛りで、1期間分のマスを1件1行に分ける(横に「 / 」で並べない)。
func splitDay(g trow) []trow {
	n := len(g.cells)
	k := 1
	for _, c := range g.cells {
		k = max(k, len(c.os)+len(c.ends))
	}
	rows := make([]trow, k)
	for i := range rows {
		rows[i] = trow{date: g.date, slot: slotAll, first: i == 0, cells: make([]cell, n)}
	}
	for j, c := range g.cells {
		for i, o := range c.os {
			rows[i].cells[j].os = []occ{o}
			if i < len(c.counts) {
				rows[i].cells[j].n = c.counts[i]
			}
		}
		for i, o := range c.ends {
			rows[len(c.os)+i].cells[j].ends = []occ{o}
		}
	}
	return rows
}

func isHdr(r trow) bool { return r.slot == slotYear || r.slot == slotMonth || r.slot == slotDate }

// skipHdr は見出しの行に止まらないよう dir の向きへずらす。
func (m *model) skipHdr(dir int) {
	for m.sel >= 0 && m.sel < len(m.rows) && isHdr(m.rows[m.sel]) {
		m.sel += dir
	}
	if m.sel < 0 || m.sel >= len(m.rows) {
		m.sel -= dir
		for m.sel >= 0 && m.sel < len(m.rows) && isHdr(m.rows[m.sel]) {
			m.sel -= dir
		}
	}
	m.clamp()
}

// ---- 選択 ----

func (m *model) clamp() {
	if m.sel >= len(m.rows) {
		m.sel = len(m.rows) - 1
	}
	if m.sel < 0 {
		m.sel = 0
	}
}

func (m *model) curRow() (trow, bool) {
	if m.sel < 0 || m.sel >= len(m.rows) {
		return trow{date: day(now()), slot: slotAll}, false
	}
	return m.rows[m.sel], true
}

// pickable はマスの中の選べる予定。終わりの行の予定も、元の予定として選べる。
func pickable(c cell) []occ {
	out := append([]occ{}, c.os...)
	for _, o := range c.ends {
		dup := false
		for _, x := range c.os {
			dup = dup || x.e == o.e
		}
		if !dup {
			out = append(out, o)
		}
	}
	return out
}

// openPick は選んでいるマスを開く。中の予定を1件ずつ選べるようにする。
func (m *model) openPick() {
	r, ok := m.curRow()
	if !ok || r.cells[m.target].empty() {
		m.msg = "this cell is empty"
		return
	}
	m.pick, m.pickList, m.pickSel = true, pickable(r.cells[m.target]), 0
}

// refreshPick は書き換えのあとに、開いているマスの中身を取り直す。
func (m *model) refreshPick(e *Entry) {
	if !m.pick {
		return
	}
	r, ok := m.curRow()
	if !ok || r.cells[m.target].empty() {
		m.pick = false
		return
	}
	m.pickList = pickable(r.cells[m.target])
	m.pickSel = min(m.pickSel, len(m.pickList)-1)
	for i, o := range m.pickList {
		if o.e == e {
			m.pickSel = i
		}
	}
}

// curEntry は選んでいるマスの予定(1件だけのとき。開いていれば選んだ1件)。
func (m *model) curEntry() (*Entry, string) {
	if m.pick {
		if m.pickSel < len(m.pickList) {
			return m.pickList[m.pickSel].e, ""
		}
		return nil, "no entry here"
	}
	r, ok := m.curRow()
	if !ok || m.target >= len(r.cells) {
		return nil, "no entry here"
	}
	c := r.cells[m.target]
	if len(c.os) == 0 && len(c.ends) == 1 {
		return c.ends[0].e, ""
	}
	switch {
	case len(c.os) == 0 && len(c.ends) == 0:
		return nil, "this cell is empty"
	case len(c.os) == 0:
		return nil, "several entries here: Enter to open the cell"
	case len(c.os) > 1:
		return nil, "several entries here: Enter to open the cell"
	}
	return c.os[0].e, ""
}

func (m *model) rowTime(r trow) time.Time {
	if r.slot >= 0 {
		return r.date.Add(time.Duration(r.slot) * time.Minute)
	}
	return r.date
}

// rowEnd は行の受け持つ期間の終わり。時刻の行は、その時刻に始まる予定の終わりまで。
func (m *model) rowEnd(r trow) time.Time {
	switch {
	case m.level == lvMonth:
		return r.date.AddDate(0, 1, 0)
	case m.level == lvYear:
		return r.date.AddDate(1, 0, 0)
	case m.level == lvTime && r.slot >= 0:
		end := m.rowTime(r).Add(time.Minute)
		for _, c := range r.cells {
			for _, o := range c.os {
				if o.timed && o.end.After(end) {
					end = o.end
				}
			}
		}
		return end
	}
	return r.date.AddDate(0, 0, 1)
}

// seek は t を含むか t より後の最初の行を選ぶ(未定の行は飛ばす)。
func (m *model) seek(t time.Time) {
	for i, r := range m.rows {
		if r.slot != slotUndef && !isHdr(r) && m.rowEnd(r).After(t) {
			m.sel = i
			return
		}
	}
	m.sel = max(0, len(m.rows)-1) // 表が空でも負にしない
}

// jumpNow は今日へ。時の目盛りなら、今かかっているかこれからの予定の行へ。
func (m *model) jumpNow() {
	m.anchor = day(now())
	m.build()
	n := now()
	m.seek(day(n))
	if m.level == lvTime {
		for i := m.sel; i < len(m.rows) && m.rows[i].date.Equal(day(n)); i++ {
			if r := m.rows[i]; r.slot >= 0 && m.rowEnd(r).After(n) {
				m.sel = i
				break
			}
		}
	}
	m.showDay()
}

// showDay は選んだ行の日の行(と月の見出し)が画面の一番上に来るようにする。
func (m *model) showDay() {
	if m.sel >= len(m.rows) { // 表が空
		m.off = 0
		return
	}
	off := m.sel
	for off > 0 && (m.rows[off-1].date.Equal(m.rows[m.sel].date) || m.rows[off-1].slot == slotMonth) && m.rows[off-1].slot != slotUndef {
		off--
		if m.rows[off].slot == slotMonth {
			break
		}
	}
	m.off = off
}

// jumpEdge は一番古い(last なら一番新しい)日付つきの予定へ飛ぶ。
// 繰り返しは果てが無いので数えない。未定は gg のあと k で上へ。
func (m *model) jumpEdge(last bool) {
	var t time.Time
	for _, e := range m.all() {
		if (e.Kind != kDate && e.Kind != kTime) || !m.visible(e) {
			continue
		}
		if t.IsZero() || (last && e.At.After(t)) || (!last && e.At.Before(t)) {
			t = e.At
		}
	}
	if t.IsZero() {
		if last {
			m.sel = max(0, len(m.rows)-1)
		} else {
			m.sel = 0
		}
		return
	}
	m.anchor = day(t)
	m.build()
	m.seek(t)
	if m.level == lvTime {
		// その時刻の行まで進める(終日の行で止まらないように)
		for i := m.sel; i < len(m.rows) && m.rows[i].date.Equal(day(t)); i++ {
			if r := m.rows[i]; m.rowTime(r).Equal(t) {
				m.sel = i
				break
			}
		}
	}
}

// reanchor は表の端に近づいたら範囲を選択中の日へ寄せて組み直す。
func (m *model) reanchor() {
	if len(m.rows) == 0 {
		return
	}
	r := m.rows[m.sel]
	if r.slot == slotUndef || (m.sel > 3 && m.sel < len(m.rows)-4) {
		return
	}
	from, to := m.window()
	if (m.sel <= 3 && r.date.Before(from.AddDate(0, 0, 3))) || m.sel >= len(m.rows)-4 && !r.date.Before(to.AddDate(0, 0, -3)) {
		t := m.rowTime(r)
		m.anchor = r.date
		m.build()
		m.seek(t)
	}
}

// focus は e のある行と列を選ぶ。
func (m *model) focus(e *Entry) {
	for j, f := range m.files {
		if f == e.file {
			m.target = j
		}
	}
	find := func() bool {
		// 繰り返しは何度も出てくるので、今日以降で最初のものを選ぶ(無ければ最初のもの)
		first, today := -1, day(now())
		for i, r := range m.rows {
			for _, o := range r.cells[m.target].os {
				if o.e == e && !r.cells[m.target].ctx {
					if first < 0 {
						first = i
					}
					if !r.date.Before(today) {
						m.sel = i
						return true
					}
				}
			}
		}
		if first >= 0 {
			m.sel = first
			return true
		}
		return false
	}
	if find() {
		return
	}
	if e.Kind == kDate || e.Kind == kTime {
		m.anchor = day(e.At)
		m.build()
		find()
	}
}

func (m *model) setLevel(lv int) {
	if lv < lvTime || lv > lvYear || lv == m.level {
		return
	}
	r, _ := m.curRow()
	t := m.rowTime(r)
	if n := now(); r.slot != slotUndef && !n.Before(t) && n.Before(m.rowEnd(r)) && lv < m.level {
		t = n // 細かくするとき、選んだ期間に今が入っていれば今へ(年の頭ではなく)
		r.date = day(n)
	}
	m.level = lv
	m.anchor = r.date
	defer func() {
		if m.hidden(m.target) {
			m.moveCol(1)
		}
	}()
	if r.date.IsZero() {
		m.anchor = day(now())
	}
	m.build()
	if r.slot == slotUndef {
		m.sel = 0
	} else {
		m.seek(t)
	}
}

// ---- 書き込み ----

func (m *model) startInput(mode int, prompt, val string) {
	m.inMode = mode
	m.in.Prompt = prompt
	m.in.SetValue(val)
	m.in.CursorEnd()
	m.in.Focus()
	m.msg, m.hint = "", ""
}

// commit は変更したファイルを書く。失敗したら読み直してメモリ側を捨てる。
func (m *model) commit(f *File) {
	if err := save(f); err != nil {
		if nf, lerr := load(f.Path); lerr == nil {
			for i, of := range m.files {
				if of == f {
					m.files[i] = nf
				}
			}
		}
		if err == errChanged {
			m.msg = f.Name() + " changed on disk; reloaded, try again"
		} else {
			m.msg = "save failed: " + err.Error()
		}
	}
	m.build()
}

func (m *model) reload() {
	for i, f := range m.files {
		if nf, err := load(f.Path); err == nil {
			m.files[i] = nf
		} else {
			m.msg = err.Error()
		}
	}
	m.build()
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		return m, tick()
	case entryEditedMsg:
		m.applyEntryEdit(msg)
		return m, nil
	case memoEditedMsg:
		defer os.Remove(msg.path)
		if msg.err != nil {
			m.msg = "editor: " + msg.err.Error()
			return m, nil
		}
		b, err := os.ReadFile(msg.path)
		if err != nil {
			m.msg = err.Error()
			return m, nil
		}
		memo := strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
		if memo != msg.e.Memo {
			msg.e.Memo, msg.e.src = memo, "" // 改行はセルの中にそのまま("…" で囲んで書く)
			m.commit(msg.e.file)
			m.focus(msg.e)
			m.refreshPick(msg.e)
			m.msg = "memo saved"
		}
		return m, nil
	case editedMsg:
		m.reload()
		m.refreshPick(nil)
		// 手で書き換えたので、重なりや読めない日時が入っていないか見る
		for _, f := range m.files {
			if p := checkMsg(f); p != "" {
				m.msg = p
				break
			}
		}
		if msg.err != nil {
			m.msg = "editor: " + msg.err.Error()
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if m.inMode != inNone {
			return m.updateInput(msg)
		}
		return m.updateKey(msg)
	}
	return m, nil
}

func (m *model) updateInput(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.inMode == inDelete {
		if k.String() == "y" {
			f := m.inE.file
			f.remove(m.inE)
			m.commit(f)
			m.refreshPick(nil)
		}
		m.inMode = inNone
		return m, nil
	}
	switch k.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.inMode = inNone
		m.in.Blur()
		return m, nil
	case tea.KeyEnter:
		val := strings.TrimSpace(m.in.Value())
		switch m.inMode {
		case inQuick, inSlot:
			if val == "" {
				break
			}
			f := m.files[m.target]
			e := &Entry{Text: val, file: f}
			if m.inMode == inQuick {
				// 先頭に日時があればその日時で(10/30 19:00-21:00 飲み会、+3 歯医者)
				if raw, text, ok := splitWhen(val, m.anchor); ok {
					e.Text = text
					e.setRaw(raw)
				}
			}
			if m.inMode == inSlot {
				r := m.inRow
				switch {
				case r.slot >= 0:
					e.setRaw(formatWhen(m.rowTime(r), kTime))
				case r.slot == slotAll:
					e.setRaw(formatWhen(r.date, kDate))
				}
			}
			if o, bad := e.clash(f); bad {
				m.msg = clashMsg(o).Error()
				return m, nil
			}
			f.add(e)
			m.commit(f)
			m.focus(e)
			if e.Kind == kUndef {
				m.msg = "added as undated: " + val
			}
		case inText:
			if val == "" {
				break
			}
			m.inE.Text, m.inE.src = val, ""
			m.commit(m.inE.file)
			m.focus(m.inE)
			m.refreshPick(m.inE)
		case inMemo:
			m.inE.Memo, m.inE.src = val, ""
			m.commit(m.inE.file)
			m.focus(m.inE)
			m.refreshPick(m.inE)
		case inWhen:
			base := m.anchor
			if m.inE.Kind == kDate || m.inE.Kind == kTime {
				base = m.inE.At
			}
			raw, err := parseInput(val, base)
			if err != nil {
				m.msg = err.Error()
				return m, nil
			}
			oldRaw, oldSrc := m.inE.Raw, m.inE.src
			raw = keepLength(m.inE, raw)
			m.inE.setRaw(raw)
			if o, bad := m.inE.clash(m.inE.file); bad {
				m.inE.setRaw(oldRaw)
				m.inE.src = oldSrc
				m.msg = clashMsg(o).Error()
				return m, nil
			}
			m.commit(m.inE.file)
			m.focus(m.inE)
			m.refreshPick(m.inE)
		case inSearch:
			m.search = val
			m.findNext(1)
		}
		m.inMode = inNone
		m.in.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.in, cmd = m.in.Update(k)
	return m, cmd
}

// findNext は / の検索: 選んでいる列(人)のファイルの予定だけから探し、日付の近い順に巡る。
// 全角・半角やひらがな・カタカナは区別せず、文字が順番どおりに出てくれば当たる(そのまま含むものが先)。
func (m *model) findNext(dir int) {
	if m.search == "" {
		return
	}
	f := m.files[m.target]
	var es []*Entry
	for _, e := range f.Entries() {
		if m.visible(e) {
			es = append(es, e)
		}
	}
	hits := searchEntries(es, m.search)
	if len(hits) == 0 {
		m.msg = fmt.Sprintf("not found in %s: %s  (h/l to search another person)", f.Name(), m.search)
		return
	}
	sortByWhen(hits)
	next := 0
	if dir < 0 {
		next = len(hits) - 1
	}
	if cur, _ := m.curEntry(); cur != nil {
		for i, e := range hits {
			if e == cur {
				next = (i + dir + len(hits)) % len(hits)
			}
		}
	}
	e := hits[next]
	m.focus(e)
	m.msg = fmt.Sprintf("found %d/%d in %s: %s  (n next, N back)", next+1, len(hits), e.file.Name(), short(e.Text, 30))
}

// sortByWhen は日付の近い順(繰り返しは今日以降の次の回、未定は最後)。
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
		return time.Date(9999, 1, 1, 0, 0, 0, 0, time.Local)
	}
	sort.SliceStable(es, func(i, j int) bool { return key(es[i]).Before(key(es[j])) })
}

func (m *model) bodyH() int {
	h := m.h - 4 // 見出し・列名・状態・操作ヒント
	if len(m.undatedOnly()) > 0 {
		h-- // 未定の固定の行
	}
	if m.inMode != inNone && m.inMode != inDelete && (m.msg != "" || m.hint != "") {
		h--
	}
	return max(1, h)
}

func (m *model) updateKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.msg = ""
	key := k.String()
	if key != "f" && key != "F" && key != "A" {
		m.freeAt = time.Time{} // 空きを辿るのは f / F を続けて押しているあいだだけ
	}
	if m.gPending {
		m.gPending = false
		if key == "g" {
			m.jumpEdge(false)
			return m, nil
		}
	}
	if m.help {
		switch key {
		case "j", "down":
			m.memoOff++
		case "k", "up":
			m.memoOff = max(0, m.memoOff-1)
		default:
			m.help = false
		}
		return m, nil
	}
	if m.memoView {
		switch key {
		case "j", "down":
			m.memoOff++
		case "k", "up":
			m.memoOff = max(0, m.memoOff-1)
		default:
			m.memoView = false
		}
		return m, nil
	}
	if m.pick {
		switch key {
		case "esc", "q", "enter", "backspace":
			m.pick = false
			return m, nil
		case "j", "down":
			m.pickSel = min(m.pickSel+1, len(m.pickList)-1)
			return m, nil
		case "k", "up":
			m.pickSel = max(m.pickSel-1, 0)
			return m, nil
		case "g":
			m.pickSel = 0
			return m, nil
		case "G":
			m.pickSel = len(m.pickList) - 1
			return m, nil
		case "e", "m", "M", "s", "x", "d", "E", "O", "?", "ctrl+c", " ", "i":
		default:
			return m, nil
		}
	}
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help, m.memoOff = true, 0
	case "g":
		m.gPending = true
	case "G":
		m.jumpEdge(true)
	case "j", "down":
		m.sel++
	case "k", "up":
		m.sel--
	case "ctrl+d", "pgdown":
		m.sel += m.bodyH() / 2
	case "ctrl+u", "pgup":
		m.sel -= m.bodyH() / 2
	case "h", "left":
		m.moveCol(-1)
	case "l", "right", "tab":
		m.moveCol(1)
	case "d":
		// 選んだ予定を済み([done])にする。済みは v で隠れるので、表から消える。もう一度押すと戻す
		e, why := m.curEntry()
		if e == nil {
			m.msg = why
			break
		}
		if isDone(e) {
			e.Text = strings.TrimSpace(reStatus.ReplaceAllString(e.Text, ""))
			m.msg = "back to open: " + short(e.Text, 30)
		} else {
			e.Text = setStatus(e.Text, "done")
			m.msg = "done: " + short(e.Text, 30)
			if m.hideDone {
				m.msg += "  (hidden; v to show done entries)"
			}
		}
		e.src = ""
		m.commit(e.file)
		m.clamp()
	case "1", "2", "3", "4": // 端末が + - を拡大縮小に使うときのため、目盛りを直接選べる
		m.setLevel(int(key[0] - '1'))
	case "+", "=":
		m.setLevel(m.level - 1)
	case "enter", "o":
		m.openPick()
	case " ", "i":
		if e, why := m.curEntry(); e == nil {
			m.msg = why
		} else {
			m.memoView, m.memoOff = true, 0
		}
	case "-", "backspace":
		m.setLevel(m.level + 1)
	case "t":
		m.jumpNow()
	case "v":
		m.hideDone = !m.hideDone
		r, _ := m.curRow()
		m.build()
		m.seek(m.rowTime(r))
		if m.hideDone {
			m.msg = "hiding done/canceled"
		} else {
			m.msg = "showing done/canceled"
		}
	case "r":
		m.reload()
		m.msg = "reloaded"
	case "/":
		m.startInput(inSearch, "/", "") // 毎回まっさら(前の語を繰り返すのは n / N)
	case "n":
		m.findNext(1)
	case "N":
		m.findNext(-1)
	case "u":
		m.jumpUntimed()
	case "a":
		m.startInput(inQuick, "add (when optional)> ", "")
	case "f":
		m.jumpFree(1)
	case "F":
		m.jumpFree(-1)
	case "A":
		r, _ := m.curRow()
		if !m.freeAt.IsZero() {
			// f で見つけた空きの始まりに足す
			r = trow{date: day(m.freeAt), slot: mins(m.freeAt, day(m.freeAt))}
			m.freeAt = time.Time{}
		} else if m.level >= lvMonth {
			m.msg = "use the hour or day scale"
			break
		}
		m.inRow = r
		at := "undated"
		switch {
		case r.slot >= 0:
			at = formatWhen(m.rowTime(r), kTime)
		case r.slot == slotAll:
			at = formatWhen(r.date, kDate)
		}
		m.startInput(inSlot, at+" > ", "")
	case "y":
		if e, why := m.curEntry(); e == nil {
			m.msg = why
		} else {
			m.reg = copyEntry(e)
			m.msg = "copied: " + short(e.Text, 30) + "  (move and p to paste)"
		}
	case "p":
		m.paste()
	case "e", "m", "s", "x":
		e, why := m.curEntry()
		if e == nil {
			m.msg = why
			break
		}
		m.inE = e
		switch key {
		case "e":
			if strings.Contains(e.Text, "\n") {
				m.msg = "the text has line breaks: edit it with E"
				break
			}
			m.startInput(inText, "text> ", e.Text)
		case "m":
			if strings.Contains(e.Memo, "\n") {
				return m, m.editMemo(e) // 1行の入力欄では改行が消えるので、エディタで開く
			}
			m.startInput(inMemo, "memo> ", e.Memo)
		case "s":
			pre := e.Raw
			if unscheduled(e) && pre != "" {
				pre += " " // 日付だけ・時刻の無い繰り返しには、時刻を続けて打てるように
			}
			m.startInput(inWhen, "when> ", pre)
			m.hint = "e.g. 10/25 14:00-15:00 | 14:00 | +3 | */12/25 | w=sun 10:00 | empty = undated"
		case "x":
			// 切り取り: 消して取っておく(p で貼れる)。確認は出さない(貼り直せば戻る)
			m.reg = copyEntry(e)
			e.file.remove(e)
			m.commit(e.file)
			m.refreshPick(nil)
			m.clamp()
			m.msg = "cut: " + short(e.Text, 30) + "  (p to paste)"
		}
	case "M":
		// メモだけを一時ファイルにしてエディタで開く(改行も日本語入力もエディタのまま)
		e, why := m.curEntry()
		if e == nil {
			m.msg = why
			break
		}
		return m, m.editMemo(e)
	case "E":
		// 選んだ予定だけをエディタで(予定が無ければファイル全体)
		if e, _ := m.curEntry(); e != nil {
			return m, m.editEntry(e)
		}
		fallthrough
	case "O":
		f := m.files[m.target]
		args := editorCmd()
		if e, _ := m.curEntry(); e != nil {
			f = e.file
			if e.line > 0 {
				args = append(args, "+"+strconv.Itoa(e.line))
			}
		}
		c := exec.Command(args[0], append(args[1:], f.Path)...)
		return m, tea.ExecProcess(c, func(err error) tea.Msg { return editedMsg{err} })
	}
	m.clamp()
	dir := 1
	switch key {
	case "k", "up", "ctrl+u", "pgup":
		dir = -1
	}
	m.skipHdr(dir)
	m.reanchor()
	m.skipHdr(dir)
	return m, nil
}

// hidden は月・年の目盛りで隠す列(label のファイル。曜日などは粗い目盛りでは意味がない)。
func (m *model) hidden(j int) bool {
	return m.level >= lvMonth && j < len(m.ctxFile) && m.ctxFile[j]
}

// moveCol は隠れた列を飛ばして列を移る。
func (m *model) moveCol(dir int) {
	n := len(m.files)
	for i := 0; i < n; i++ {
		m.target = (m.target + dir + n) % n
		if !m.hidden(m.target) {
			return
		}
	}
}

// editMemo はメモだけを一時ファイルにしてエディタで開く(改行も日本語入力もエディタのまま)。
func (m *model) editMemo(e *Entry) tea.Cmd {
	tmp, err := os.CreateTemp("", "nichiji-memo-*.txt")
	if err != nil {
		m.msg = err.Error()
		return nil
	}
	tmp.WriteString(e.Memo)
	if e.Memo != "" {
		tmp.WriteString("\n")
	}
	tmp.Close()
	args := editorCmd()
	c := exec.Command(args[0], append(args[1:], tmp.Name())...)
	path := tmp.Name()
	return tea.ExecProcess(c, func(err error) tea.Msg { return memoEditedMsg{e, path, err} })
}

func editorCmd() []string {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "vi"
	}
	return strings.Fields(ed)
}

// ---- 描画 ----

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

func fit(s string, w int) string {
	return runewidth.FillRight(runewidth.Truncate(s, w, "…"), w)
}

func dateLabel(d time.Time) string { return d.Format("2006-01-02") }

// label は左の欄。日付は日付だけの行に書き(月・年の目盛りでは年月・年)、時刻は2文字下げる(khal / gcalcli のアジェンダ風)。
// 同じ日の2件目以降は空ける。
func (m *model) label(r trow) string {
	switch {
	case r.slot == slotUndef:
		if r.first {
			return "undated"
		}
		return ""
	case r.slot == slotYear:
		return r.date.Format("2006")
	case r.slot == slotMonth:
		return r.date.Format("2006-01")
	case r.slot == slotDate:
		return dateLabel(r.date)
	case r.slot == slotAll && m.level <= lvDay:
		return "" // 日付は上の行に書いてある
	case !r.first && r.slot < 0:
		return "" // 同じ月・年の2件目以降
	case m.level == lvYear:
		return r.date.Format("2006")
	case m.level == lvMonth:
		return r.date.Format("2006-01")
	case r.slot >= 0:
		return fmt.Sprintf("  %02d:%02d", r.slot/60, r.slot%60)
	}
	return dateLabel(r.date) // 日の行は年月日で書く(月の見出しの行は置かない)
}

// fullLabel は状態の行などに出す、省かない日時。
func (m *model) fullLabel(r trow) string {
	switch {
	case r.slot == slotUndef:
		return "undated"
	case r.slot == slotYear || m.level == lvYear:
		return r.date.Format("2006")
	case r.slot == slotMonth || m.level == lvMonth:
		return r.date.Format("2006-01")
	case r.slot >= 0:
		return m.rowTime(r).Format("2006-01-02 15:04")
	}
	return dateLabel(r.date)
}

// crumb は画面の一番上の行がどの年・月か(見出しが画面の外に出ても分かるように)。
func (m *model) crumb() string {
	if m.off >= len(m.rows) {
		return ""
	}
	r := m.rows[m.off]
	switch {
	case r.slot == slotUndef || r.slot == slotYear || r.slot == slotMonth:
		return ""
	case m.level == lvMonth:
		return r.date.Format("2006")
	case m.level == lvYear:
		return ""
	case m.level <= lvDay:
		return r.date.Format("2006-01-02") // 日付の行が画面の外に出ても、どの日か分かるように
	}
	return r.date.Format("2006-01")
}

// shortDate は今年なら年を省く(狭い画面で左の欄を細くするため)。
func shortDate(d time.Time) string {
	if d.Year() == now().Year() {
		return d.Format("01-02")
	}
	return dateLabel(d)
}

// colWidths は列の幅を中身に合わせて決める。全部入るなら中身どおり、入らなければ
// 短い列は中身どおりにして、残りを長い列で等分する。
func colWidths(natural []int, avail int) []int {
	n := len(natural)
	w := make([]int, n)
	left, rest := avail, n
	done := make([]bool, n)
	for changed := true; changed && rest > 0; {
		changed = false
		share := left / rest
		for j := range natural {
			if !done[j] && natural[j] <= share {
				w[j], done[j] = natural[j], true
				left -= natural[j]
				rest--
				changed = true
			}
		}
	}
	for j := range natural {
		if !done[j] {
			w[j] = max(4, left/max(1, rest))
		}
	}
	return w
}

// occText はマスに書く1件分。
func (m *model) occText(o occ, withTime bool) string {
	e := o.e
	t := strings.ReplaceAll(e.textOn(day(o.at)), "\n", "⏎")
	if o.timed {
		if withTime {
			// 日の目盛り: 時刻は前にまとめる(09:00-10:00 定例)。時の目盛りでは └… end の行が終わりを示す
			hm := o.at.Format("15:04")
			if !o.end.IsZero() {
				hm += "-" + endLabel(o.at, o.end)
			}
			t = hm + " " + t
		}
		if overdue(o) {
			t = "LATE " + t
		}
		if _, bad := e.clashOn(e.file, day(o.at)); bad {
			t = "!" + t
		}
	}
	if e.Memo != "" {
		t += " *"
	}
	return t
}

func (m *model) cellText(c cell) string {
	var ps []string
	if m.level == lvTime && c.mid && len(c.os) == 0 && len(c.ends) == 0 {
		return "│"
	}
	for _, o := range c.os {
		t := m.occText(o, false) // 日の目盛りでも時刻は書かない(選べば下の行に出る)
		if m.level == lvTime && o.timed {
			// 予定ひとつを ┌ … └ でくくって、どこからどこまでかを見せる
			if !o.end.IsZero() {
				t = "┌" + t
			} else {
				t = " " + t
			}
		}
		if c.n > 1 {
			t += fmt.Sprintf(" x%d", c.n) // 月・年で同じ予定が何回あるか
		}
		ps = append(ps, t)
	}
	for _, o := range c.ends {
		t := strings.ReplaceAll(o.e.textOn(day(o.at)), "\n", "⏎")
		t = "└" + t // 日の目盛りでは、日付をまたいで続いてきたもの
		ps = append(ps, t)
	}
	return strings.Join(ps, " / ")
}

func (m *model) View() string {
	bodyH := m.bodyH()
	if m.sel < m.off {
		m.off = m.sel
	}
	if m.sel >= m.off+bodyH {
		m.off = m.sel - bodyH + 1
	}
	// 一番上のすぐ上が日付だけの行なら、それも見せる(どの日の予定か分かるように)
	for m.off > 0 && isHdr(m.rows[m.off-1]) && m.sel < m.off-1+bodyH {
		m.off--
	}
	// 見えている行から、左の欄と各列の幅を決める
	lw := 1
	natural := make([]int, len(m.files))
	for j, f := range m.files {
		natural[j] = runewidth.StringWidth(f.Name()) + 1
		if m.hidden(j) {
			natural[j] = 0
		}
	}
	for i := m.off; i < len(m.rows) && i < m.off+bodyH; i++ {
		r := m.rows[i]
		if !isHdr(r) {
			lw = max(lw, runewidth.StringWidth(m.label(r))+1)
		}
		for j, c := range r.cells {
			if !m.hidden(j) {
				natural[j] = max(natural[j], runewidth.StringWidth(m.cellText(c))+1)
			}
		}
	}
	crumb := m.crumb()
	cws := colWidths(natural, m.w-lw)

	// 見出し: 目盛り・画面の一番上の位置(年から)・今の時刻
	pos := seg{"", stPlain}
	if crumb != "" {
		pos = seg{"  " + crumb, stPlain}
	}
	und := seg{"", stWarn}
	if n := len(undatedEntries(m.files)); n > 0 {
		und.s = fmt.Sprintf("  no time:%d", n) // 決めるまで出し続ける
	}
	// 見出し: 今の目盛りだけ・画面の一番上の位置・時刻の無い予定の数・今の時刻
	segs := []seg{{"nichiji ", stHead}, {lvNames[m.level], stHead}, pos, und}
	if !m.hideDone {
		segs = append(segs, seg{"  +done", stPlain})
	}
	clock := "15:04"
	if m.w >= 70 {
		clock = "2006-01-02 15:04"
	}
	title := line(append(segs, seg{"  now " + now().Format(clock), stToday}), m.w, false)
	// 未定はいつも見えるように、見出しの下に固定で1行(カーソルは入らない。移るのは u)
	pin := ""
	if us := m.undatedOnly(); len(us) > 0 {
		var ts []string
		for _, e := range us {
			ts = append(ts, strings.ReplaceAll(e.Text, "\n", " "))
		}
		pin = "\n" + line([]seg{{fmt.Sprintf("undated %d: ", len(us)), stWarn}, {strings.Join(ts, " / "), stPlain}}, m.w, false)
	}

	hdr := strings.Repeat(" ", lw)
	for j, f := range m.files {
		if m.hidden(j) {
			continue
		}
		st := stDim
		if j == m.target {
			st = stHead.Underline(true)
		}
		hdr += st.Render(fit(f.Name(), cws[j]-1)) + " "
	}

	today := day(now())
	var body []string
	if m.help {
		body = m.helpBody(bodyH)
	} else if m.memoView {
		body = m.memoBody(bodyH)
	} else if m.pick {
		body = m.pickBody(bodyH)
	} else if len(m.rows) == 0 {
		body = []string{stDim.Render(" (nothing)  a quick add, A add here")}
	}
	for i := m.off; !m.pick && !m.memoView && !m.help && i < len(m.rows) && i < m.off+bodyH; i++ {
		r := m.rows[i]
		lst := stDim
		if r.first {
			lst = stPlain
		}
		if r.slot == slotDate {
			// 日付だけの行は、左の欄からはみ出して1行まるごと使う
			st := stHead
			if r.date.Equal(today) {
				st = stToday
			}
			body = append(body, st.Render(runewidth.Truncate(m.label(r), m.w, "")))
			continue
		}
		if isHdr(r) {
			lst = stHead
		}
		if r.slot != slotUndef && r.date.Equal(today) && m.level <= lvDay {
			lst = stToday
		}
		dayRow := m.level == lvTime && r.slot == slotAll && r.first
		if dayRow || (m.level == lvDay && r.slot == slotAll) {
			lst = stHead
			if r.date.Equal(today) {
				lst = stToday
			}
		}
		var b strings.Builder
		b.WriteString(lst.Render(fit(m.label(r), lw)))
		for j, c := range r.cells {
			if m.hidden(j) {
				continue
			}
			st := stPlain
			if c.ctx || (len(c.os) == 0 && len(c.ends) > 0) {
				st = stDim
			}
			txt := m.cellText(c)
			if strings.HasPrefix(txt, "!") || strings.HasPrefix(txt, "LATE") {
				st = stWarn
			}
			if i == m.sel && j == m.target {
				st = stSel
			}
			b.WriteString(st.Render(fit(txt, cws[j]-1)) + " ")
		}
		body = append(body, b.String())
	}
	for len(body) < bodyH {
		body = append(body, "")
	}
	return title + pin + "\n" + hdr + "\n" + strings.Join(body[:bodyH], "\n") + "\n" + m.footer()
}

// wrap は幅に合わせて折り返す(改行はそのまま改行)。
func wrap(s string, w int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		cur, cw := "", 0
		for _, r := range para {
			rw := runewidth.RuneWidth(r)
			if cw+rw > w {
				out = append(out, cur)
				cur, cw = "", 0
			}
			cur += string(r)
			cw += rw
		}
		out = append(out, cur)
	}
	return out
}

// memoBody は選んだ予定の日時・内容・メモを全文で出す。
func (m *model) memoBody(bodyH int) []string {
	e, _ := m.curEntry()
	if e == nil {
		return nil
	}
	when := e.Raw
	if when == "" {
		when = "undated"
	}
	w := max(10, m.w-2)
	out := []string{line([]seg{{" " + e.file.Name() + "  " + when, stHead}}, m.w, false)}
	for _, l := range wrap(e.Text, w) {
		out = append(out, " "+l)
	}
	out = append(out, "")
	if e.Memo == "" {
		out = append(out, stDim.Render(" (no memo)  m to write one"))
	} else {
		for _, l := range wrap(e.Memo, w) {
			out = append(out, " "+l)
		}
	}
	off := min(m.memoOff, max(0, len(out)-bodyH))
	m.memoOff = off
	out = out[off:]
	if len(out) > bodyH {
		out = out[:bodyH]
	}
	return out
}

// pickBody は開いたマスの中身を1件1行で並べる。
func (m *model) pickBody(bodyH int) []string {
	r, _ := m.curRow()
	out := []string{line([]seg{{fmt.Sprintf(" %s  %s  (%d)  j/k pick  e m s x E edit  Esc close", m.files[m.target].Name(), m.fullLabel(r), len(m.pickList)), stHead}}, m.w, false)}
	h := bodyH - 1
	off := max(0, m.pickSel-h+1)
	for i := off; i < len(m.pickList) && i < off+h; i++ {
		o := m.pickList[i]
		when := "undated"
		switch {
		case o.timed:
			when = o.at.Format("2006-01-02 15:04")
		case o.e.Kind != kUndef:
			when = dateLabel(o.at) + "      "
		}
		out = append(out, line([]seg{{" " + when + "  ", stDim}, {m.occText(o, false), stPlain}}, m.w, i == m.pickSel))
	}
	return out
}

func (m *model) status() string {
	e, _ := m.curEntry()
	if e == nil {
		r, ok := m.curRow()
		if !ok {
			return ""
		}
		return m.files[m.target].Name() + "  " + m.fullLabel(r)
	}
	s := e.file.Name() + "  " + e.Raw
	if e.Raw == "" {
		s += "(undated)"
	}
	s += "  " + strings.ReplaceAll(e.Text, "\n", "⏎")
	if e.Memo != "" {
		s += "  | memo: " + strings.ReplaceAll(e.Memo, "\n", "⏎")
	}
	return s
}

const hintShort = "j/k move h/l person 1-4 scale Enter open Space memo u no-time f free t now a quick A add e text m edit memo s when y/x/p copy/cut/paste d done E editor v done / find ? help q quit"

// helpKeys は ? で出すキーの一覧(1キー1行)。
var helpKeys = [][2]string{
	{"j k", "down / up one row"},
	{"gg G", "first / last dated entry"},
	{"C-d C-u", "half a page down / up"},
	{"h l Tab", "move to the person (file) on the left / right; new entries go to the selected one"},
	{"1 2 3 4", "scale: hour / day / month / year (also + finer, - coarser)"},
	{"t", "back to now"},
	{"u", "jump to the next entry with no time: undated first, then by date (give it one with s)"},
	{"f F", "jump to the next / previous free time of the selected person"},
	{"/ n N", "find in the selected person's entries (fuzzy, text and memo), next, previous"},
	{"Space i", "read the selected entry's text and memo in full"},
	{"Enter o", "list the selected cell's entries with their dates (when a cell holds several)"},
	{"a", "add: \"10/30 19:00-21:00 dinner\", \"+3 dentist\", or just text (undated)"},
	{"A", "add at this row (or at the free time f found)"},
	{"e", "edit the text"},
	{"s", "set the date and time"},
	{"m M", "edit the memo: m on one line, M in the editor"},
	{"E", "edit the selected entry in the editor (date and time, text, memo)"},
	{"O", "open the whole file in the editor at this line"},
	{"y x p", "copy / cut / paste the entry (paste goes to the cursor's row and person)"},
	{"d", "mark the entry done (it disappears; press again to undo)"},
	{"v", "show / hide the done and canceled entries"},
	{"r", "reload the files"},
	{"?", "this help"},
	{"q", "quit"},
}

func (m *model) helpBody(bodyH int) []string {
	out := []string{line([]seg{{" keys  (j/k scroll, any other key closes)", stHead}}, m.w, false)}
	for _, k := range helpKeys {
		out = append(out, line([]seg{{fmt.Sprintf(" %-8s ", k[0]), stHead}, {k[1], stPlain}}, m.w, false))
	}
	off := min(m.memoOff, max(0, len(out)-bodyH))
	m.memoOff = off
	out = out[off:]
	if len(out) > bodyH {
		out = out[:bodyH]
	}
	return out
}

func (m *model) footer() string {
	hint := hintShort
	hints := line([]seg{{hint, stDim}}, m.w, false)
	switch m.inMode {
	case inDelete:
		return stWarn.Render(runewidth.Truncate("delete? (y/N) "+m.inE.Text, m.w, "…")) + "\n" + hints
	case inNone:
	default:
		m.in.Width = max(10, m.w-runewidth.StringWidth(m.in.Prompt)-1)
		top := ""
		if m.msg != "" {
			top = line([]seg{{m.msg, stWarn}}, m.w, false) + "\n"
		} else if m.hint != "" {
			top = line([]seg{{m.hint, stDim}}, m.w, false) + "\n"
		}
		return top + m.in.View() + "\n" + line([]seg{{"Enter ok  Esc cancel", stDim}}, m.w, false)
	}
	if m.msg != "" {
		return line([]seg{{m.msg, stWarn}}, m.w, false) + "\n" + hints
	}
	return line([]seg{{m.status(), stPlain}}, m.w, false) + "\n" + hints
}
