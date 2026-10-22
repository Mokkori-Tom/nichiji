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
)

type cell struct {
	os   []occ
	ends []occ // この行の時刻(日)に終わる予定。表に「… end」と出す。選ぶと元の予定を編集する
	ctx  bool  // その日の日付だけの値を、時刻の行にも添えたもの(曜日など)。操作の対象にしない
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
	memoView bool // 選んだ予定の内容とメモを全文で見ている
	memoOff  int
	listMemo bool // ls 用: メモ本文も出す
}

var (
	stSel   = lipgloss.NewStyle().Reverse(true)
	stDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
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
	// 書込先の既定は、繰り返しだけのファイル(曜日など)を飛ばした最初のファイル
	for j, f := range files {
		only := len(f.Entries()) > 0
		for _, e := range f.Entries() {
			if e.Kind != kRepeat {
				only = false
			}
		}
		if !only {
			m.target = j
			break
		}
	}
	m.build()
	m.jumpNow()
	return m
}

func (m *model) Init() tea.Cmd { return tick() }

type tickMsg struct{}

// tick は次の分の頭で時計を進める。
func tick() tea.Cmd {
	n := now()
	return tea.Tick(n.Truncate(time.Minute).Add(time.Minute).Sub(n), func(time.Time) tea.Msg { return tickMsg{} })
}

type editedMsg struct{ err error }

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

func (m *model) build() {
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
		for _, c := range group.cells {
			if !c.empty() {
				rows = append(rows, group)
				break
			}
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
		rows = append(rows, dayRows(d, per, ends)...)
	}
	flush()
	m.rows = rows
	m.clamp()
}

// dayRows は「時」の目盛りで1日分の行を作る。細かい方に合わせ、
// 時刻を持たないファイル(曜日など)の値はその日のどの行にも添える。
func dayRows(d time.Time, per, ends [][]occ) []trow {
	n := len(per)
	timed := make([][]occ, n)
	allday := make([][]occ, n)
	keys := map[int]bool{}
	for j, os := range per {
		for _, o := range os {
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
			keys[mins(o.end, d)] = true
		}
	}
	// 終日の行が要るのは、時刻も持つファイルに終日の値があるとき、または時刻の行が1つも無いとき
	nAll := 0
	for j := range per {
		if len(allday[j]) > 0 && (len(timed[j]) > 0 || len(keys) == 0) {
			nAll = max(nAll, len(allday[j]))
		}
	}
	var rows []trow
	for i := 0; i < nAll; i++ {
		r := trow{date: d, slot: slotAll, cells: make([]cell, n)}
		for j := range per {
			if i < len(allday[j]) {
				r.cells[j].os = []occ{allday[j][i]}
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
			if len(timed[j]) == 0 && len(ends[j]) == 0 && len(allday[j]) > 0 {
				r.cells[j] = cell{os: allday[j], ctx: true}
			}
		}
		rows = append(rows, r)
	}
	if len(rows) > 0 {
		rows[0].first = true
	}
	return rows
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
		if r.slot != slotUndef && m.rowEnd(r).After(t) {
			m.sel = i
			return
		}
	}
	m.sel = len(m.rows) - 1
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
	m.off = max(0, m.sel-3)
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
			m.sel = len(m.rows) - 1
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
		for i, r := range m.rows {
			for _, o := range r.cells[m.target].os {
				if o.e == e && !r.cells[m.target].ctx {
					m.sel = i
					return true
				}
			}
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
	m.level = lv
	m.anchor = r.date
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

func (m *model) rowText(r trow) string {
	var b strings.Builder
	for _, c := range r.cells {
		for _, o := range c.os {
			b.WriteString(o.e.Text + " " + o.e.Memo + "\n")
		}
	}
	return strings.ToLower(b.String())
}

func (m *model) findNext(dir int) {
	if m.search == "" {
		return
	}
	q := strings.ToLower(m.search)
	for i := 1; i <= len(m.rows); i++ {
		j := ((m.sel+dir*i)%len(m.rows) + len(m.rows)) % len(m.rows)
		if strings.Contains(m.rowText(m.rows[j]), q) {
			m.sel = j
			return
		}
	}
	m.msg = "not found: " + m.search
}

func (m *model) bodyH() int {
	h := m.h - 4 // 見出し・列名・状態・操作ヒント
	if m.inMode != inNone && m.inMode != inDelete && (m.msg != "" || m.hint != "") {
		h--
	}
	return max(1, h)
}

func (m *model) updateKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.msg = ""
	key := k.String()
	if m.gPending {
		m.gPending = false
		if key == "g" {
			m.jumpEdge(false)
			return m, nil
		}
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
		case "e", "m", "s", "x", "E", "?", "ctrl+c", " ", "i":
		default:
			return m, nil
		}
	}
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = !m.help
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
		m.target = (m.target + len(m.files) - 1) % len(m.files)
	case "l", "right", "tab":
		m.target = (m.target + 1) % len(m.files)
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
		m.startInput(inSearch, "/", m.search)
	case "n":
		m.findNext(1)
	case "N":
		m.findNext(-1)
	case "a":
		m.startInput(inQuick, "quick (no date)> ", "")
	case "A":
		r, _ := m.curRow()
		if m.level >= lvMonth {
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
	case "e", "m", "s", "x":
		e, why := m.curEntry()
		if e == nil {
			m.msg = why
			break
		}
		m.inE = e
		switch key {
		case "e":
			m.startInput(inText, "text> ", e.Text)
		case "m":
			m.startInput(inMemo, "memo> ", e.Memo)
		case "s":
			m.startInput(inWhen, "when> ", e.Raw)
			m.hint = "e.g. 10/25 14:00-15:00 | 14:00 | +3 | */12/25 | w=sun 10:00 | empty = undated"
		case "x":
			m.inMode = inDelete
		}
	case "E":
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
	m.reanchor()
	return m, nil
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

func (m *model) label(r trow) string {
	switch {
	case r.slot == slotUndef:
		if r.first {
			return "undated"
		}
		return ""
	case m.level == lvMonth:
		return r.date.Format("2006-01")
	case m.level == lvYear:
		return r.date.Format("2006")
	case m.level == lvDay:
		return dateLabel(r.date)
	}
	d := "          "
	if r.first {
		d = dateLabel(r.date)
	}
	if r.slot >= 0 {
		return fmt.Sprintf("%s %02d:%02d", d, r.slot/60, r.slot%60)
	}
	return d
}

func (m *model) labelW() int {
	switch m.level {
	case lvTime:
		return 17
	case lvDay:
		return 11
	case lvMonth:
		return 8
	}
	return 5
}

// occText はマスに書く1件分。
func (m *model) occText(o occ, withTime bool) string {
	e := o.e
	t := strings.ReplaceAll(e.textOn(day(o.at)), "\n", "⏎")
	if o.timed {
		if withTime {
			t = o.at.Format("15:04") + " " + t
		}
		if !o.end.IsZero() {
			t += " 〜" + endLabel(o.at, o.end)
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
	seen := map[string]bool{}
	for _, o := range c.os {
		t := m.occText(o, m.level == lvDay)
		if m.level >= lvMonth {
			// 月・年では同じ内容(曜日など)を1つにまとめ、件数を前に出す
			t = strings.ReplaceAll(o.e.textOn(day(o.at)), "\n", "⏎")
			if seen[t] {
				continue
			}
			seen[t] = true
		}
		ps = append(ps, t)
	}
	for _, o := range c.ends {
		t := strings.ReplaceAll(o.e.textOn(day(o.at)), "\n", "⏎") + " end"
		if m.level == lvDay {
			t = "〜" + endLabel(o.at, o.end) + " " + t
		}
		ps = append(ps, t)
	}
	s := strings.Join(ps, " / ")
	if m.level >= lvMonth && len(c.os) > 1 {
		s = fmt.Sprintf("%d: %s", len(c.os), s)
	}
	return s
}

func (m *model) View() string {
	lw := m.labelW()
	n := len(m.files)
	cw := max(4, (m.w-lw)/n)
	bodyH := m.bodyH()

	done := "done:hidden"
	if !m.hideDone {
		done = "done:shown"
	}
	var lvs []seg
	for i, nm := range lvNames {
		st := stDim
		if i == m.level {
			st = stHead
		}
		lvs = append(lvs, seg{" " + nm, st})
	}
	title := line(append(append([]seg{{"nichiji ", stHead}}, lvs...),
		seg{"  " + done, stDim}, seg{"  " + now().Format("2006-01-02 15:04"), stToday}), m.w, false)

	hdr := strings.Repeat(" ", lw)
	for j, f := range m.files {
		st := stDim
		if j == m.target {
			st = stHead.Underline(true)
		}
		hdr += st.Render(fit(f.Name(), cw-1)) + " "
	}

	if m.sel < m.off {
		m.off = m.sel
	}
	if m.sel >= m.off+bodyH {
		m.off = m.sel - bodyH + 1
	}
	today := day(now())
	var body []string
	if m.memoView {
		body = m.memoBody(bodyH)
	} else if m.pick {
		body = m.pickBody(bodyH)
	} else if len(m.rows) == 0 {
		body = []string{stDim.Render(" (nothing)  a quick add, A add here")}
	}
	for i := m.off; !m.pick && !m.memoView && i < len(m.rows) && i < m.off+bodyH; i++ {
		r := m.rows[i]
		lst := stDim
		if r.first {
			lst = stPlain
		}
		if r.slot != slotUndef && r.date.Equal(today) && m.level <= lvDay {
			lst = stToday
		}
		var b strings.Builder
		b.WriteString(lst.Render(fit(m.label(r), lw)))
		for j, c := range r.cells {
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
			b.WriteString(st.Render(fit(txt, cw-1)) + " ")
		}
		body = append(body, b.String())
	}
	for len(body) < bodyH {
		body = append(body, "")
	}
	return title + "\n" + hdr + "\n" + strings.Join(body[:bodyH], "\n") + "\n" + m.footer()
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
	out := []string{line([]seg{{fmt.Sprintf(" %s  %s  (%d)  j/k pick  e m s x E edit  Esc close", m.files[m.target].Name(), strings.TrimSpace(m.label(r)), len(m.pickList)), stHead}}, m.w, false)}
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
		return m.files[m.target].Name() + "  " + strings.TrimSpace(m.label(r))
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

const hintShort = "j/k move h/l column +/- scale Enter open Space memo t now a quick A add e text m edit memo s when x del E editor v done / find ? help q quit"

const hintFull = "j/k down/up  gg/G first/last dated entry  C-d/C-u half page  h/l(Tab) column = write target  +/- finer/coarser  Enter open cell  t now  " +
	"Space/i read text+memo  a quick add  A add at row  e text  m edit memo  s when  x delete  E editor at line  v show/hide done  / find n/N  r reload  q quit"

func (m *model) footer() string {
	hint := hintShort
	if m.help {
		hint = hintFull
	}
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
