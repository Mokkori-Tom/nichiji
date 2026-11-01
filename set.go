package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// 内容の先頭の [状態]。[予約] [実行中] は「まだ閉じていない」扱いで、終わりを過ぎると LATE が付く。
var reStatus = regexp.MustCompile(`^\[([^\]]*)\]\s*`)

func status(text string) string {
	if m := reStatus.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

func isOpen(text string) bool {
	s := status(text)
	return s == "reserved" || s == "running" || s == "予約" || s == "実行中"
}

// overdue は、閉じていない予定が終わりの時刻を過ぎているか。
func overdue(o occ) bool {
	return o.timed && !o.end.IsZero() && o.end.Before(now()) && isOpen(o.e.Text)
}

func setStatus(text, st string) string {
	return "[" + st + "] " + reStatus.ReplaceAllString(text, "")
}

func short(s string, w int) string {
	return runewidth.Truncate(strings.ReplaceAll(s, "\n", "⏎"), w, "…")
}

type setOpts struct {
	match, at     string
	when, end, st string
	note, text    string
	memo          string
	force         bool
	softEnd       bool // done: 日時つきの予定のときだけ終わりを今にする
	// 空でも指定されたか。-d "" は未定に、-m "" はメモ無しにする
	whenSet, memoSet, textSet bool
}

// findEntry は -match(内容の部分一致)と -at(開始の日時)で1件に絞る。
func findEntry(f *File, match, at string) (*Entry, error) {
	if match == "" && at == "" {
		return nil, fmt.Errorf("pick an entry with -match or -at")
	}
	atRaw := ""
	if at != "" {
		r, err := parseInput(at, now())
		if err != nil {
			return nil, err
		}
		atRaw = r
	}
	var hit []*Entry
	for _, e := range f.Entries() {
		if match != "" && !strings.Contains(e.Text, match) {
			continue
		}
		if atRaw != "" {
			t, k := parseWhen(atRaw)
			if e.Kind == kUndef || e.Kind == kRepeat || k == kUndef || !e.At.Equal(t) {
				continue
			}
		}
		hit = append(hit, e)
	}
	switch len(hit) {
	case 0:
		return nil, fmt.Errorf("no matching entry")
	case 1:
		return hit[0], nil
	}
	// 「会議」と「会議A」があるとき、完全に同じものがあればそれを選ぶ
	var exact []*Entry
	for _, e := range hit {
		if first, _, _ := strings.Cut(e.Text, "\n"); match != "" && first == match {
			exact = append(exact, e)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d entries match; narrow it down with -at or -match:", len(hit))
	for _, e := range hit {
		fmt.Fprintf(&b, "\n  %s  %s", e.Raw, short(e.Text, 50))
	}
	return nil, fmt.Errorf("%s", b.String())
}

// newEnd は -end の値を解く。now / +分 / HH:MM。
func newEnd(e *Entry, v string) (string, error) {
	if e.Kind != kTime {
		return "", fmt.Errorf("-end needs a dated entry with a time (rewrite repeats with -d)")
	}
	var end time.Time
	switch {
	case v == "now":
		end = now().Truncate(time.Minute)
	case strings.HasPrefix(v, "+"):
		n, err := strconv.Atoi(v[1:])
		if err != nil {
			return "", fmt.Errorf("-end +minutes: %q", v)
		}
		base := e.End
		if base.IsZero() {
			base = e.At
		}
		end = base.Add(time.Duration(n) * time.Minute)
	default:
		m, err := hhmm(v)
		if err != nil {
			return "", err
		}
		end = endAt(e.At, m)
	}
	if !end.After(e.At) {
		return "", fmt.Errorf("end is not after start")
	}
	if end.Sub(e.At) >= 24*time.Hour {
		return "", fmt.Errorf("cannot span 24 hours or more")
	}
	m := mins(end, day(end))
	return formatWhen(e.At, kTime) + fmt.Sprintf("-%02d:%02d", m/60, m%60), nil
}

// setEntry は CLI からの書き換え。重なると拒否(-force なら書いて ! が付く)。
func setEntry(path string, o setOpts) (string, error) {
	f, err := load(path)
	if err != nil {
		return "", err
	}
	e, err := findEntry(f, o.match, o.at)
	if err != nil {
		return "", err
	}
	if o.textSet && o.text == "" {
		return "", fmt.Errorf("-text cannot be empty (to remove the entry: nichiji rm)")
	}
	raw, oldRaw := e.Raw, e.Raw
	if o.whenSet && o.when != oldRaw { // 補完で入った今の値のままなら書き換えない
		base := now()
		if e.Kind == kDate || e.Kind == kTime {
			base = e.At
		}
		if raw, err = parseInput(o.when, base); err != nil {
			return "", err
		}
		raw = keepLength(e, raw)
	}
	text := e.Text
	if o.text != "" {
		text = o.text
	}
	if o.st != "" {
		text = setStatus(text, o.st)
	}
	if o.note != "" {
		text += " | " + o.note
	}
	e.setRaw(raw)
	if o.end != "" && !(o.softEnd && e.Kind != kTime) {
		r, err := newEnd(e, o.end)
		if err != nil {
			return "", err
		}
		e.setRaw(r)
	}
	e.Text = text
	if o.memoSet {
		e.Memo = o.memo
	}
	// 日時を変えないなら(名前の修正など)、もともとの重なりでは止めない
	if c, bad := e.clash(f); bad && !o.force && e.Raw != oldRaw {
		return "", fmt.Errorf("%v (use -force to write anyway)", clashMsg(c))
	}
	if err := save(f); err != nil {
		return "", err
	}
	return setWhen(e) + "  " + short(e.Text, 60), nil
}

// setWhen は書き換えの結果に出す日時。翌日に続く予定は 23:00-22:00 では読めないので、終わりの日付と長さも書く。
func setWhen(e *Entry) string {
	if e.Raw == "" {
		return "undated"
	}
	if e.Kind != kTime || !e.End.After(day(e.At).AddDate(0, 0, 1)) {
		return e.Raw
	}
	d := e.End.Sub(e.At)
	return fmt.Sprintf("%s → %s (%dh%02dm)", formatWhen(e.At, kTime), formatWhen(e.End, kTime), int(d.Hours()), int(d.Minutes())%60)
}
