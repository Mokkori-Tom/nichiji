package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// 内容の先頭の [状態]。[予約] [実行中] は「まだ閉じていない」扱いで、終わりを過ぎると ⏰ が付く。
var reStatus = regexp.MustCompile(`^\[([^\]]*)\]\s*`)

func status(text string) string {
	if m := reStatus.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

func isOpen(text string) bool {
	s := status(text)
	return s == "予約" || s == "実行中"
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
	force         bool
	softEnd       bool // done: 日時つきの予定のときだけ終わりを今にする
}

// findEntry は -match(内容の部分一致)と -at(開始の日時)で1件に絞る。
func findEntry(f *File, match, at string) (*Entry, error) {
	if match == "" && at == "" {
		return nil, fmt.Errorf("-match か -at で予定を指定して")
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
		return nil, fmt.Errorf("見つからない")
	case 1:
		return hit[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d件あるので絞り込んで(-at か -match を足す):", len(hit))
	for _, e := range hit {
		fmt.Fprintf(&b, "\n  %s  %s", e.Raw, short(e.Text, 50))
	}
	return nil, fmt.Errorf("%s", b.String())
}

// newEnd は -end の値を解く。now / +分 / HH:MM。
func newEnd(e *Entry, v string) (string, error) {
	if e.Kind != kTime {
		return "", fmt.Errorf("-end は日時つきの予定だけ(繰り返しは -d で書き換える)")
	}
	var end time.Time
	switch {
	case v == "now":
		end = now().Truncate(time.Minute)
	case strings.HasPrefix(v, "+"):
		n, err := strconv.Atoi(v[1:])
		if err != nil {
			return "", fmt.Errorf("-end +分: %q", v)
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
		return "", fmt.Errorf("終わりが始まりより前")
	}
	if end.Sub(e.At) >= 24*time.Hour {
		return "", fmt.Errorf("24時間以上は書けない")
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
	raw := e.Raw
	if o.when != "" {
		base := now()
		if e.Kind == kDate || e.Kind == kTime {
			base = e.At
		}
		if raw, err = parseInput(o.when, base); err != nil {
			return "", err
		}
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
	if c, bad := e.clash(f); bad && !o.force {
		return "", fmt.Errorf("%v(それでも書くなら -force)", clashMsg(c))
	}
	if err := save(f); err != nil {
		return "", err
	}
	return e.Raw + "  " + short(e.Text, 60), nil
}
