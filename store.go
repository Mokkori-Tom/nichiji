package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 日時列の状態
const (
	kUndef  = iota // 未定(空、または読めない)
	kDate          // 日付のみ(終日)
	kTime          // 日時
	kRepeat        // 繰り返し(rule.go)
)

type Entry struct {
	Raw  string // 日時列。触っていない行はこのまま書き戻す
	Text string
	At   time.Time
	End  time.Time // 終わり(無ければゼロ)
	Kind int
	rule rule
	min  int    // 繰り返しの時刻(分)、-1 = 終日
	emin int    // 繰り返しの終わり(分)、-1 = 無し
	base int    // 起点の年(\a \y 用)
	src  string // 読み込んだ行そのもの。変更したら空にする
	file *File
}

// item は1行。予定でない行(空行・# コメント・見出し)は raw のまま持って書き戻す。
type item struct {
	e   *Entry
	raw string
}

type File struct {
	Path  string
	Items []item
	Mod   time.Time
}

func (f *File) Entries() []*Entry {
	var es []*Entry
	for _, it := range f.Items {
		if it.e != nil {
			es = append(es, it.e)
		}
	}
	return es
}

func (f *File) add(e *Entry) { f.Items = append(f.Items, item{e: e}) }

func (f *File) remove(e *Entry) {
	for i, it := range f.Items {
		if it.e == e {
			f.Items = append(f.Items[:i], f.Items[i+1:]...)
			return
		}
	}
}

// occ は一つの日に現れた予定(繰り返しは日ごとに作る)。
type occ struct {
	e     *Entry
	at    time.Time
	end   time.Time // 終わり(無ければゼロ)
	timed bool
}

// endAt は start の日の分 m を終わりにする。開始以前なら翌日。
func endAt(start time.Time, m int) time.Time {
	t := day(start).Add(time.Duration(m) * time.Minute)
	if !t.After(start) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// on は d に当たれば occ を返す。
func (e *Entry) on(d time.Time) (occ, bool) {
	switch e.Kind {
	case kDate, kTime:
		if day(e.At).Equal(d) {
			return occ{e, e.At, e.End, e.Kind == kTime}, true
		}
	case kRepeat:
		if e.rule(d) && (e.base == 0 || d.Year() >= e.base) {
			if e.min >= 0 {
				at := d.Add(time.Duration(e.min) * time.Minute)
				o := occ{e: e, at: at, timed: true}
				if e.emin >= 0 {
					o.end = endAt(at, e.emin)
				}
				return o, true
			}
			return occ{e: e, at: d}, true
		}
	}
	return occ{}, false
}

// textOn は \a(年齢) \y(起点の年) を埋めた内容。
func (e *Entry) textOn(d time.Time) string {
	if e.base == 0 {
		return e.Text
	}
	t := strings.ReplaceAll(e.Text, `\a`, strconv.Itoa(d.Year()-e.base))
	return strings.ReplaceAll(t, `\y`, strconv.Itoa(e.base))
}

func (f *File) Name() string {
	return strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
}

var layouts = []struct {
	l string
	k int
}{
	{"2006-1-2 15:04", kTime},
	{"2006-1-2T15:04", kTime},
	{"2006-1-2 15:04:05", kTime},
	{"2006-1-2T15:04:05", kTime},
	{"2006-1-2", kDate},
}

// parseWhen は CSV の日時列を読む。区切りは - と / のどちらでもよい。
func parseWhen(s string) (time.Time, int) {
	s = strings.ReplaceAll(strings.TrimSpace(s), "/", "-")
	if s == "" {
		return time.Time{}, kUndef
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l.l, s, time.Local); err == nil {
			return t, l.k
		}
	}
	return time.Time{}, kUndef
}

func formatWhen(t time.Time, k int) string {
	switch k {
	case kTime:
		return t.Format("2006-01-02 15:04")
	case kDate:
		return t.Format("2006-01-02")
	}
	return ""
}

var (
	reRel  = regexp.MustCompile(`^\+(\d+)(?:\s+(\d{1,2}:\d{2}(?:-\d{1,2}:\d{2})?))?$`)
	reHHMM = regexp.MustCompile(`^\d{1,2}:\d{2}(?:-\d{1,2}:\d{2})?$`)
	reEnd  = regexp.MustCompile(`^(.*\d:\d{2})-(\d{1,2}):(\d{2})$`)
)

// splitEnd は末尾の「-15:30」を外して分で返す(無ければ -1)。
func splitEnd(s string) (string, int) {
	m := reEnd.FindStringSubmatch(s)
	if m == nil {
		return s, -1
	}
	h, _ := strconv.Atoi(m[2])
	mm, _ := strconv.Atoi(m[3])
	if h > 24 || mm > 59 {
		return s, -1
	}
	return m[1], h*60 + mm
}

// canon は日付・日時(と終わり)を正規形にする。
func canon(s string) (string, bool) {
	st, em := splitEnd(s)
	t, k := parseWhen(st)
	if k == kUndef || (em >= 0 && k != kTime) {
		return "", false
	}
	out := formatWhen(t, k)
	if em >= 0 {
		out += fmt.Sprintf("-%02d:%02d", em/60, em%60)
	}
	return out, true
}

var ()

// parseInput は人が打つ日時を正規化する。
// 空=未定 / 2026-10-25 14:00 / 10/25 14:00 / 10/25 / 14:00(base の日) / +3 / +3 9:00(今日から)
func parseInput(s string, base time.Time) (string, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "/", "-"))
	if s == "" {
		return "", nil
	}
	if m := reRel.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		d := day(now()).AddDate(0, 0, n)
		if m[2] == "" {
			return formatWhen(d, kDate), nil
		}
		s = d.Format("2006-01-02 ") + m[2]
	}
	if reHHMM.MatchString(s) {
		s = day(base).Format("2006-01-02 ") + s
	}
	if c, ok := canon(s); ok {
		return c, nil
	}
	if _, _, _, _, err := parseRule(s); err == nil {
		return s, nil
	} else if strings.ContainsAny(s, "=<>") {
		return "", fmt.Errorf("繰り返しが読めない: %v", err)
	}
	if c, ok := canon(fmt.Sprintf("%d-%s", base.Year(), s)); ok {
		return c, nil
	}
	return "", fmt.Errorf("日時が読めない: %q", s)
}

func (e *Entry) setRaw(raw string) {
	e.Raw, e.src = raw, ""
	e.rule, e.End = nil, time.Time{}
	st, em := splitEnd(norm(raw))
	e.At, e.Kind = parseWhen(st)
	if em >= 0 && e.Kind == kTime {
		e.End = endAt(e.At, em)
	} else if em >= 0 {
		e.Kind = kUndef
	}
	if e.Kind == kUndef {
		if r, min, emin, base, err := parseRule(norm(raw)); err == nil {
			e.Kind, e.rule, e.min, e.emin, e.base = kRepeat, r, min, emin, base
		}
	}
}

func norm(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "/", "-") }

func load(path string) (*File, error) {
	f := &File{Path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(path); err == nil {
		f.Mod = st.ModTime()
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(b) == 0 {
		lines = nil
	}
	if len(lines) > 0 {
		lines[0] = strings.TrimPrefix(lines[0], "\ufeff")
	}
	for i := 0; i < len(lines); i++ {
		ln := strings.TrimSuffix(lines[i], "\r")
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") || (i == 0 && strings.HasPrefix(t, "日時,")) {
			f.Items = append(f.Items, item{raw: ln})
			continue
		}
		// 内容に改行を含む("…" で囲まれた)行は閉じるまで繋ぐ
		rec, err := csv1(ln)
		for err != nil && i+1 < len(lines) {
			i++
			ln += "\n" + strings.TrimSuffix(lines[i], "\r")
			rec, err = csv1(ln)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		e := &Entry{file: f}
		e.setRaw(rec[0])
		if len(rec) > 1 {
			e.Text = strings.Join(rec[1:], ",")
		}
		e.src = ln
		f.add(e)
	}
	return f, nil
}

// csv1 は1件を読む。閉じていない " のときだけエラーを返して次の行と繋がせる。
func csv1(s string) ([]string, error) {
	rec, err := csvRead(s, false)
	var pe *csv.ParseError
	if errors.As(err, &pe) && pe.Err != csv.ErrQuote {
		rec, err = csvRead(s, true) // 引用の外に " がある程度なら緩く読む
	}
	return rec, err
}

func csvRead(s string, lazy bool) ([]string, error) {
	r := csv.NewReader(strings.NewReader(s))
	r.FieldsPerRecord = -1
	r.LazyQuotes = lazy
	rec, err := r.Read()
	if err == nil {
		if _, err2 := r.Read(); err2 != io.EOF {
			err = fmt.Errorf("1行に収まらない")
		}
	}
	return rec, err
}

var errChanged = errors.New("外部で変更されている")

// save は一時ファイルに書いて rename する。読み込み後に他から書き換えられていたら書かない。
func save(f *File) error {
	if st, err := os.Stat(f.Path); err == nil && !st.ModTime().Equal(f.Mod) {
		return errChanged
	}
	dir := filepath.Dir(f.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".nichiji-*")
	if err != nil {
		return err
	}
	w := csv.NewWriter(tmp)
	for _, it := range f.Items {
		if it.e != nil && it.e.src == "" {
			w.Write([]string{it.e.Raw, it.e.Text})
		} else if it.e != nil {
			w.Flush()
			tmp.WriteString(it.e.src + "\n")
		} else {
			w.Flush()
			tmp.WriteString(it.raw + "\n")
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	if err := os.Rename(tmp.Name(), f.Path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if st, err := os.Stat(f.Path); err == nil {
		f.Mod = st.ModTime()
	}
	return nil
}

// appendLine は TUI を開かずに1行足す(nichiji add 用)。
func appendLine(path, raw, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	fh, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer fh.Close()
	if st, _ := fh.Stat(); st.Size() > 0 {
		b := make([]byte, 1)
		if _, err := fh.ReadAt(b, st.Size()-1); err == nil && b[0] != '\n' {
			fh.WriteString("\n")
		}
	}
	w := csv.NewWriter(fh)
	w.Write([]string{raw, text})
	w.Flush()
	return w.Error()
}

// expandArgs はディレクトリを中の *.csv に展開する。
func expandArgs(args []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if a, err := filepath.Abs(p); err == nil {
			p = a
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, a := range args {
		if st, err := os.Stat(a); err == nil && st.IsDir() {
			ms, _ := filepath.Glob(filepath.Join(a, "*.csv"))
			sort.Strings(ms)
			for _, m := range ms {
				add(m)
			}
			continue
		}
		add(a)
	}
	return out
}

func defaultFile() string {
	if p := os.Getenv("NICHIJI_FILE"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "nichiji", "nichiji.csv")
}

func day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

func weekStart(t time.Time) time.Time {
	d := day(t)
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}

// ---- 重なり ----
// 1ファイル = 1人(1台)。同じファイルの中で時刻つきの予定が重なるのを防ぐ。
// 終わりの無い予定は開始の1分だけを占める。終日・未定は数えない。

func span(o occ) (time.Time, time.Time, bool) {
	if !o.timed {
		return time.Time{}, time.Time{}, false
	}
	if o.end.IsZero() {
		return o.at, o.at.Add(time.Minute), true
	}
	return o.at, o.end, true
}

func overlap(a, b occ) bool {
	as, ae, ok1 := span(a)
	bs, be, ok2 := span(b)
	return ok1 && ok2 && as.Before(be) && bs.Before(ae)
}

// clashOn は d に現れる e が、同じファイルの他の予定と重なるかを見る。
func (e *Entry) clashOn(f *File, d time.Time) (occ, bool) {
	o, ok := e.on(d)
	if !ok || !o.timed {
		return occ{}, false
	}
	for _, x := range f.Entries() {
		if x == e {
			continue
		}
		for i := -1; i <= 1; i++ { // 日付をまたぐ予定があるので前後の日も
			if xo, ok := x.on(d.AddDate(0, 0, i)); ok && overlap(o, xo) {
				return xo, true
			}
		}
	}
	return occ{}, false
}

// clash は e を f に置いたときに重なる相手を探す。繰り返しは今日から1年分を見る。
func (e *Entry) clash(f *File) (occ, bool) {
	switch e.Kind {
	case kTime:
		return e.clashOn(f, day(e.At))
	case kRepeat:
		t := day(now())
		for i := 0; i <= 366; i++ {
			if o, ok := e.clashOn(f, t.AddDate(0, 0, i)); ok {
				return o, true
			}
		}
	}
	return occ{}, false
}

func clashMsg(o occ) error {
	t := o.at.Format("15:04")
	if !o.end.IsZero() {
		t += "-" + o.end.Format("15:04")
	}
	return fmt.Errorf("重なる: %d/%d %s %s", o.at.Month(), o.at.Day(), t, short(o.e.textOn(day(o.at)), 40))
}
