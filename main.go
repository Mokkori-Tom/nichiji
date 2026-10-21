package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

const usage = `nichiji — CSV(日時,内容)の予定表

  nichiji [ファイル|ディレクトリ ...]          TUI。最初のファイルが書込先(Tab で切替)
  nichiji ls [-days 14] [-past 0] [-u] [ファイル ...]   直近の予定を表示して終わる
  nichiji add [-f ファイル] [-d 日時] 内容        1行追記。-d 無しは未定メモ
  共通: -now "2026-12-24 10:00"   今日を差し替える(確認用)

日時: 2026-10-25 14:00 / 10/25 14:00 / 10/25 / 14:00 / +3 / +3 9:00 / 空=未定
繰り返し: */12/25(毎年) 1990*/8/29(内容の \a が年齢) w=日 10:00(毎週)
          d=1|d=15(毎月) w=土&b=1(最終土曜) m=1&w=月&a=2(1月第2月曜)
CSV の空行と # で始まる行は無視し、保存してもそのまま残る
既定ファイル: $NICHIJI_FILE か ~/.local/share/nichiji/nichiji.csv
`

func main() {
	sub := ""
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "add" || args[0] == "ls") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("nichiji", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	nowS := fs.String("now", "", "今日を差し替える")
	file := fs.String("f", defaultFile(), "add の書込先")
	when := fs.String("d", "", "add の日時")
	days := fs.Int("days", 14, "ls で何日先まで")
	past := fs.Int("past", 0, "ls で何日前から")
	undef := fs.Bool("u", false, "ls で未定も並べる")
	fs.Parse(args)
	if *nowS != "" {
		if err := setNow(*nowS); err != nil {
			die(err)
		}
	}
	switch sub {
	case "add":
		text := strings.Join(fs.Args(), " ")
		if text == "" {
			fs.Usage()
			os.Exit(2)
		}
		raw, err := parseInput(*when, now())
		if err == nil {
			err = appendLine(*file, raw, text)
		}
		if err != nil {
			die(err)
		}
		return
	}
	paths := expandArgs(fs.Args())
	if len(paths) == 0 {
		paths = []string{defaultFile()}
	}
	var files []*File
	for _, p := range paths {
		f, err := load(p)
		if err != nil {
			die(err)
		}
		files = append(files, f)
	}
	if sub == "ls" {
		list(os.Stdout, files, *past, *days, *undef)
		return
	}
	m := newModel()
	m.files = files
	m.resetSel()
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		die(err)
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

const (
	vDay = iota
	vWeek
	vYear
	vInbox
)

var viewNames = []string{"日", "週", "年", "未定"}

const (
	inNone = iota
	inQuick
	inSlot
	inText
	inWhen
	inDelete
)

type row struct {
	e     *Entry
	at    time.Time
	timed bool
	date  time.Time
	hour  int // -1 = 終日・見出し
	head  bool
}

type model struct {
	files  []*File
	target int
	view   int
	cur    time.Time
	sel    int
	off    int
	w, h   int
	in     textinput.Model
	inMode int
	inE    *Entry
	inRow  row
	msg    string
	help   bool
}

var (
	stSel   = lipgloss.NewStyle().Reverse(true)
	stDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	stHead  = lipgloss.NewStyle().Bold(true)
	stToday = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	stMark  = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true).Underline(true)
	stWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	stPlain = lipgloss.NewStyle()
	tagCols = []string{"4", "5", "2", "1", "6", "3"}
	wdays   = []string{"日", "月", "火", "水", "木", "金", "土"}
)

func newModel() *model {
	ti := textinput.New()
	ti.CharLimit = 0
	return &model{cur: day(now()), view: vWeek, w: 80, h: 24, in: ti}
}

func (m *model) Init() tea.Cmd { return nil }

type editedMsg struct{ err error }

func (m *model) all() []*Entry {
	var es []*Entry
	for _, f := range m.files {
		es = append(es, f.Entries()...)
	}
	return es
}

func (m *model) occsOn(d time.Time) []occ { return occsOn(m.all(), d) }

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

func occRow(o occ, d time.Time) row {
	h := -1
	if o.timed {
		h = o.at.Hour()
	}
	return row{e: o.e, at: o.at, timed: o.timed, date: d, hour: h}
}

func (m *model) rows() []row {
	var rs []row
	switch m.view {
	case vDay:
		d := day(m.cur)
		os := m.occsOn(d)
		for _, o := range os {
			if !o.timed {
				rs = append(rs, occRow(o, d))
			}
		}
		for h := 0; h < 24; h++ {
			n := 0
			for _, o := range os {
				if o.timed && o.at.Hour() == h {
					rs = append(rs, occRow(o, d))
					n++
				}
			}
			if n == 0 {
				rs = append(rs, row{date: d, hour: h})
			}
		}
	case vWeek:
		s := weekStart(m.cur)
		for i := 0; i < 7; i++ {
			d := s.AddDate(0, 0, i)
			rs = append(rs, row{date: d, hour: -1, head: true})
			for _, o := range m.occsOn(d) {
				rs = append(rs, occRow(o, d))
			}
		}
	case vInbox:
		for _, e := range m.all() {
			if e.Kind == kUndef {
				rs = append(rs, row{e: e, date: day(m.cur), hour: -1})
			}
		}
	}
	return rs
}

func (m *model) curRow() (row, bool) {
	rs := m.rows()
	if m.sel < 0 || m.sel >= len(rs) {
		return row{date: day(m.cur), hour: -1}, false
	}
	return rs[m.sel], true
}

// resetSel は日付や表示を変えた直後に、選ぶ行を決め直す。
func (m *model) resetSel() {
	rs := m.rows()
	m.sel, m.off = 0, 0
	now := now()
	switch m.view {
	case vDay:
		want := 9
		if day(m.cur).Equal(day(now)) {
			want = now.Hour()
		}
		if !day(m.cur).Equal(day(now)) {
			for i, r := range rs {
				if r.e != nil {
					m.sel = i
					return
				}
			}
		}
		for i, r := range rs {
			if r.hour == want {
				m.sel = i
				return
			}
		}
	case vWeek:
		for i, r := range rs {
			if r.head && r.date.Equal(day(m.cur)) {
				m.sel = i
				return
			}
		}
	}
}

// focus は e の行を選ぶ。今の表示に無い日付なら、その日へ移る。
func (m *model) focus(e *Entry) {
	for pass := 0; pass < 2; pass++ {
		for i, r := range m.rows() {
			if r.e == e {
				m.sel = i
				return
			}
		}
		if (e.Kind != kDate && e.Kind != kTime) || m.view == vInbox || m.view == vYear {
			break
		}
		m.cur = day(e.At)
		m.resetSel()
	}
	m.clamp()
}

func (m *model) clamp() {
	n := len(m.rows())
	if m.sel >= n {
		m.sel = n - 1
	}
	if m.sel < 0 {
		m.sel = 0
	}
}

func (m *model) startInput(mode int, prompt, val string) {
	m.inMode = mode
	m.in.Prompt = prompt
	m.in.SetValue(val)
	m.in.CursorEnd()
	m.in.Focus()
	m.msg = ""
}

// commit は変更したファイルを書く。失敗したら読み直してメモリ側を捨てる。
func (m *model) commit(f *File) {
	err := save(f)
	if err == nil {
		return
	}
	if nf, lerr := load(f.Path); lerr == nil {
		for i, of := range m.files {
			if of == f {
				m.files[i] = nf
			}
		}
	}
	if err == errChanged {
		m.msg = f.Name() + " が外部で変更されていたので読み直した。もう一度どうぞ"
	} else {
		m.msg = "保存失敗: " + err.Error()
	}
	m.clamp()
}

func (m *model) reload() {
	for i, f := range m.files {
		if nf, err := load(f.Path); err == nil {
			m.files[i] = nf
		} else {
			m.msg = err.Error()
		}
	}
	m.clamp()
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case editedMsg:
		m.reload()
		if msg.err != nil {
			m.msg = "エディタ: " + msg.err.Error()
		}
		m.resetSel()
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
			m.clamp()
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
				if r.hour >= 0 {
					e.setRaw(formatWhen(r.date.Add(time.Duration(r.hour)*time.Hour), kTime))
				} else {
					e.setRaw(formatWhen(r.date, kDate))
				}
			}
			f.add(e)
			m.commit(f)
			if e.Kind == kUndef {
				m.msg = "未定に追加: " + val
			} else {
				m.focus(e)
			}
		case inText:
			if val == "" {
				break
			}
			m.inE.Text, m.inE.src = val, ""
			m.commit(m.inE.file)
		case inWhen:
			base := m.cur
			if m.inE.Kind == kDate || m.inE.Kind == kTime {
				base = m.inE.At
			}
			raw, err := parseInput(val, base)
			if err != nil {
				m.msg = err.Error()
				return m, nil
			}
			m.inE.setRaw(raw)
			m.commit(m.inE.file)
			m.focus(m.inE)
		}
		m.inMode = inNone
		m.in.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.in, cmd = m.in.Update(k)
	return m, cmd
}

func (m *model) move(days int) {
	m.cur = day(m.cur).AddDate(0, 0, days)
	m.resetSel()
}

func (m *model) updateKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.msg = ""
	r, ok := m.curRow()
	if m.view == vYear {
		r, ok = row{date: day(m.cur), hour: -1}, false
	}
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = !m.help
	case "d":
		m.view = vDay
		m.resetSel()
	case "w":
		m.view = vWeek
		m.resetSel()
	case "y":
		m.view = vYear
	case "u":
		m.view = vInbox
		m.resetSel()
	case "t":
		m.cur = day(now())
		m.resetSel()
	case "tab":
		m.target = (m.target + 1) % len(m.files)
		m.msg = "書込先: " + m.files[m.target].Path
	case "r":
		m.reload()
		m.msg = "再読込した"
	case "E":
		f := m.files[m.target]
		if ok && r.e != nil {
			f = r.e.file
		}
		ed := os.Getenv("VISUAL")
		if ed == "" {
			ed = os.Getenv("EDITOR")
		}
		if ed == "" {
			ed = "vi"
		}
		args := strings.Fields(ed)
		c := exec.Command(args[0], append(args[1:], f.Path)...)
		return m, tea.ExecProcess(c, func(err error) tea.Msg { return editedMsg{err} })
	case "a":
		m.startInput(inQuick, "メモ(日時は後で)> ", "")
	case "A":
		if m.view == vInbox {
			m.startInput(inQuick, "メモ(日時は後で)> ", "")
			break
		}
		m.inRow = r
		at := formatWhen(r.date, kDate)
		if r.hour >= 0 {
			at = formatWhen(r.date.Add(time.Duration(r.hour)*time.Hour), kTime)
		}
		m.startInput(inSlot, at+" > ", "")
	case "e":
		if ok && r.e != nil {
			m.inE = r.e
			m.startInput(inText, "内容> ", r.e.Text)
		}
	case "s", "enter":
		if m.view == vYear {
			if k.String() == "enter" {
				m.view = vDay
				m.resetSel()
			}
			break
		}
		if ok && r.e != nil {
			m.inE = r.e
			m.startInput(inWhen, "日時(10/25 14:00・14:00・+3・*/12/25・w=日 10:00・空=未定)> ", r.e.Raw)
		}
	case "x":
		if ok && r.e != nil {
			m.inE = r.e
			m.inMode = inDelete
		}
	case "j", "down":
		if m.view == vYear {
			m.cur = m.cur.AddDate(0, 0, 7)
		} else {
			m.sel++
			m.clamp()
		}
	case "k", "up":
		if m.view == vYear {
			m.cur = m.cur.AddDate(0, 0, -7)
		} else {
			m.sel--
			m.clamp()
		}
	case "h", "left":
		switch m.view {
		case vDay, vYear:
			m.move(-1)
		case vWeek:
			m.move(-7)
		}
	case "l", "right":
		switch m.view {
		case vDay, vYear:
			m.move(1)
		case vWeek:
			m.move(7)
		}
	case "H":
		if m.view == vYear {
			m.cur = m.cur.AddDate(-1, 0, 0)
		} else {
			m.cur = m.cur.AddDate(0, -1, 0)
		}
		m.resetSel()
	case "L":
		if m.view == vYear {
			m.cur = m.cur.AddDate(1, 0, 0)
		} else {
			m.cur = m.cur.AddDate(0, 1, 0)
		}
		m.resetSel()
	}
	if m.view == vWeek {
		if r, ok := m.curRow(); ok {
			m.cur = r.date
		}
	}
	return m, nil
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

func (m *model) tag(e *Entry) seg {
	if len(m.files) < 2 {
		return seg{}
	}
	for i, f := range m.files {
		if f == e.file {
			return seg{" [" + f.Name() + "]", lipgloss.NewStyle().Foreground(lipgloss.Color(tagCols[i%len(tagCols)]))}
		}
	}
	return seg{}
}

func (m *model) entrySegs(indent string, r row) []seg {
	e := r.e
	var when seg
	switch {
	case e.Kind == kUndef && e.Raw != "":
		when = seg{"?" + e.Raw + " ", stWarn}
	case e.Kind == kUndef:
		when = seg{"未定 ", stDim}
	case r.timed:
		when = seg{r.at.Format("15:04"), stPlain}
	default:
		when = seg{"終日 ", stDim}
	}
	rep := seg{" ", stPlain}
	if e.Kind == kRepeat {
		rep = seg{"↻", stDim}
	}
	// 内容を先に削らせないよう、タグは内容の前に置く
	return []seg{{indent, stPlain}, when, rep, m.tag(e), {" " + strings.ReplaceAll(e.textOn(r.date), "\n", "⏎"), stPlain}}
}

func dateLabel(d time.Time) string {
	return fmt.Sprintf("%d/%d (%s)", d.Month(), d.Day(), wdays[d.Weekday()])
}

func (m *model) rowSegs(r row) []seg {
	today := day(now())
	switch {
	case r.head:
		st := stHead
		if r.date.Equal(today) {
			st = stToday
		}
		return []seg{{dateLabel(r.date), st}}
	case r.e != nil && m.view == vWeek:
		return m.entrySegs("  ", r)
	case r.e != nil:
		return m.entrySegs(" ", r)
	default:
		st := stDim
		if r.date.Equal(today) && r.hour == now().Hour() {
			st = stToday
		}
		return []seg{{fmt.Sprintf(" %02d:00", r.hour), st}, {"  ·", stDim}}
	}
}

func (m *model) View() string {
	bodyH := max(1, m.h-2)
	var title string
	var body []string
	d := day(m.cur)
	switch m.view {
	case vDay:
		title = fmt.Sprintf("%d年 %s", d.Year(), dateLabel(d))
	case vWeek:
		y, wk := d.ISOWeek()
		s := weekStart(d)
		title = fmt.Sprintf("%d-W%02d  %s 〜 %s", y, wk, dateLabel(s), dateLabel(s.AddDate(0, 0, 6)))
	case vYear:
		title = fmt.Sprintf("%d年", d.Year())
	case vInbox:
		title = "未定(s で日時を決める)"
	}
	var tabs []seg
	for i, n := range viewNames {
		st := stDim
		if i == m.view {
			st = stHead
		}
		tabs = append(tabs, seg{" " + n, st})
	}
	title = line(append([]seg{{title + "  ", stHead}}, tabs...), m.w, false)

	if m.view == vYear {
		body = m.yearBody(bodyH)
	} else {
		rs := m.rows()
		if len(rs) == 0 {
			body = []string{stDim.Render(" (なし)  a でメモを追加")}
		}
		if m.sel < m.off {
			m.off = m.sel
		}
		if m.sel >= m.off+bodyH {
			m.off = m.sel - bodyH + 1
		}
		for i := m.off; i < len(rs) && i < m.off+bodyH; i++ {
			body = append(body, line(m.rowSegs(rs[i]), m.w, i == m.sel))
		}
	}
	for len(body) < bodyH {
		body = append(body, "")
	}
	return title + "\n" + strings.Join(body[:bodyH], "\n") + "\n" + m.footer()
}

func (m *model) footer() string {
	switch m.inMode {
	case inDelete:
		return stWarn.Render(runewidth.Truncate("削除? (y/N) "+m.inE.Text, m.w, "…"))
	case inNone:
	default:
		m.in.Width = max(10, m.w-runewidth.StringWidth(m.in.Prompt)-1)
		return m.in.View()
	}
	if m.msg != "" {
		return line([]seg{{m.msg, stWarn}}, m.w, false)
	}
	if m.help {
		return line([]seg{{"a:メモ A:ここに追加 e:内容 s/Enter:日時 x:削除 E:エディタ d/w/y/u:日週年未定 h/l:前後 H/L:月(年) t:今日 Tab:書込先 r:再読込 q:終了", stDim}}, m.w, false)
	}
	return line([]seg{{"書込: " + m.files[m.target].Name(), stDim}, {"  ?:ヘルプ", stDim}}, m.w, false)
}

func (m *model) monthBlock(y int, mo time.Month, marks map[time.Time]bool) []string {
	const bw = 20
	today := day(now())
	cur := day(m.cur)
	first := time.Date(y, mo, 1, 0, 0, 0, 0, time.Local)
	ttl := fmt.Sprintf("%d月", mo)
	st := stHead
	if mo == cur.Month() {
		st = stToday
	}
	pad := (bw - runewidth.StringWidth(ttl)) / 2
	lines := []string{strings.Repeat(" ", pad) + st.Render(ttl) + strings.Repeat(" ", bw-pad-runewidth.StringWidth(ttl))}
	lines = append(lines, stDim.Render("月 火 水 木 金 土 日"))
	d := weekStart(first)
	for wk := 0; wk < 6; wk++ {
		var b strings.Builder
		for i := 0; i < 7; i++ {
			cell := "  "
			cs := stPlain
			if d.Month() == mo {
				cell = fmt.Sprintf("%2d", d.Day())
				switch {
				case d.Equal(cur):
					cs = stSel
				case d.Equal(today):
					cs = stToday
				case marks[d]:
					cs = stMark
				case i >= 5:
					cs = stDim
				}
			}
			b.WriteString(cs.Render(cell))
			if i < 6 {
				b.WriteString(" ")
			}
			d = d.AddDate(0, 0, 1)
		}
		lines = append(lines, b.String())
	}
	return lines
}

func (m *model) yearBody(bodyH int) []string {
	y := m.cur.Year()
	marks := map[time.Time]bool{}
	all := m.all()
	for d := time.Date(y, 1, 1, 0, 0, 0, 0, time.Local); d.Year() == y; d = d.AddDate(0, 0, 1) {
		for _, e := range all {
			if _, ok := e.on(d); ok {
				marks[d] = true
				break
			}
		}
	}
	cols := max(1, min(6, (m.w+2)/22))
	var bands [][]string // 月を cols 個ずつ横に並べた帯
	for mo := 1; mo <= 12; mo += cols {
		var blocks []string
		for c := 0; c < cols && mo+c <= 12; c++ {
			blk := strings.Join(m.monthBlock(y, time.Month(mo+c), marks), "\n")
			if c > 0 {
				blk = lipgloss.NewStyle().PaddingLeft(2).Render(blk)
			}
			blocks = append(blocks, blk)
		}
		bands = append(bands, strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, blocks...), "\n"))
	}
	os := m.occsOn(day(m.cur))
	preview := min(len(os)+1, max(2, bodyH/3))
	fit := max(1, (bodyH-preview)/9)
	curBand := (int(m.cur.Month()) - 1) / cols
	start := max(0, min(curBand-fit/2, len(bands)-fit))
	var out []string
	for i := start; i < len(bands) && i < start+fit; i++ {
		out = append(out, bands[i]...)
		out = append(out, "")
	}
	out = append(out, line([]seg{{dateLabel(day(m.cur)), stHead}}, m.w, false))
	for _, o := range os {
		out = append(out, line(m.entrySegs(" ", occRow(o, day(m.cur))), m.w, false))
	}
	return out
}
