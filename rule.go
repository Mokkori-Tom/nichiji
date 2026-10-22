package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// 繰り返しの書き方(when を真似た小さな式)。末尾に " 10:00" や " 10:00-11:30" を付けると時刻つき。
//
//	*-12-25        毎年12月25日
//	1990*-08-29    毎年。内容の \a が年齢、\y が起点の年になる
//	w=日           毎週日曜(w=0〜6 や w=sun も可)
//	d=1|d=15       毎月1日と15日
//	w=土&b=1       毎月最後の土曜(a=第n週、b=最後からn週目)
//	m=1&w=月&a=2   1月の第2月曜
//	w=金&!(m=12&d=25)
//	w=月 10:00-11:30   終わりの時刻も書ける
//
// 変数 w 曜日 m 月 d 日 y 年 a 第n(1〜7日が1) b 最後からn n 月の日数
// 演算 = != < > <= >=、& | ! ( )
type rule func(t time.Time) bool

var (
	reYearly  = regexp.MustCompile(`^(\d{4})?\*-(\d{1,2})-(\d{1,2})$`)
	reRuleTim = regexp.MustCompile(`^(.*\S)\s+(\d{1,2}):(\d{2})(?:-(\d{1,2}):(\d{2}))?$`)
)

// parseRule は繰り返しを読む。min・emin は時刻と終わり(分、無ければ -1)、base は起点の年(無ければ 0)。
func parseRule(s string) (r rule, min, emin, base int, err error) {
	min, emin = -1, -1
	if m := reRuleTim.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[2])
		mm, _ := strconv.Atoi(m[3])
		if h > 23 || mm > 59 {
			return nil, 0, 0, 0, fmt.Errorf("bad time: %s", s)
		}
		s, min = m[1], h*60+mm
		if m[4] != "" {
			h, _ = strconv.Atoi(m[4])
			mm, _ = strconv.Atoi(m[5])
			if h > 24 || mm > 59 {
				return nil, 0, 0, 0, fmt.Errorf("bad end time: %s", s)
			}
			emin = h*60 + mm
		}
	}
	if m := reYearly.FindStringSubmatch(s); m != nil {
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		if mo < 1 || mo > 12 || d < 1 || d > 31 {
			return nil, 0, 0, 0, fmt.Errorf("bad date: %s", s)
		}
		base, _ = strconv.Atoi(m[1])
		return func(t time.Time) bool { return int(t.Month()) == mo && t.Day() == d }, min, emin, base, nil
	}
	if !strings.ContainsAny(s, "=<>") {
		return nil, 0, 0, 0, fmt.Errorf("not a repeat rule")
	}
	p := &parser{toks: tokenize(s)}
	r, err = p.or()
	if err == nil && p.i < len(p.toks) {
		err = fmt.Errorf("unexpected %q", p.toks[p.i])
	}
	if err != nil {
		return nil, 0, 0, 0, err
	}
	return r, min, emin, 0, nil
}

func tokenize(s string) []string {
	var out []string
	rs := []rune(s)
	for i := 0; i < len(rs); {
		c := rs[i]
		switch {
		case unicode.IsSpace(c):
			i++
		case strings.ContainsRune("&|()", c):
			out = append(out, string(c))
			i++
		case strings.ContainsRune("!<>=", c):
			if i+1 < len(rs) && rs[i+1] == '=' {
				out = append(out, string(rs[i:i+2]))
				i += 2
			} else {
				out = append(out, string(c))
				i++
			}
		default:
			j := i
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j])) {
				j++
			}
			if j == i {
				j++
			}
			out = append(out, string(rs[i:j]))
			i = j
		}
	}
	return out
}

type parser struct {
	toks []string
	i    int
}

func (p *parser) peek() string {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return ""
}

func (p *parser) next() string { t := p.peek(); p.i++; return t }

func (p *parser) or() (rule, error) {
	l, err := p.and()
	for err == nil && p.peek() == "|" {
		p.next()
		var r rule
		if r, err = p.and(); err == nil {
			a, b := l, r
			l = func(t time.Time) bool { return a(t) || b(t) }
		}
	}
	return l, err
}

func (p *parser) and() (rule, error) {
	l, err := p.unary()
	for err == nil && p.peek() == "&" {
		p.next()
		var r rule
		if r, err = p.unary(); err == nil {
			a, b := l, r
			l = func(t time.Time) bool { return a(t) && b(t) }
		}
	}
	return l, err
}

func (p *parser) unary() (rule, error) {
	switch p.peek() {
	case "!":
		p.next()
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		return func(t time.Time) bool { return !r(t) }, nil
	case "(":
		p.next()
		r, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.next() != ")" {
			return nil, fmt.Errorf("missing )")
		}
		return r, nil
	}
	return p.cmp()
}

var wdayNames = map[string]int{
	"日": 0, "月": 1, "火": 2, "水": 3, "木": 4, "金": 5, "土": 6,
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

func (p *parser) cmp() (rule, error) {
	v := p.next()
	get, ok := vars[v]
	if !ok {
		return nil, fmt.Errorf("unknown variable %q (w m d y a b n)", v)
	}
	op := p.next()
	val := p.next()
	n, err := strconv.Atoi(val)
	if err != nil && v == "w" {
		key := strings.TrimSuffix(strings.ToLower(val), "曜")
		if len(key) > 3 && key[0] < 0x80 {
			key = key[:3]
		}
		n, ok = wdayNames[key]
		if ok {
			err = nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("bad value %q", val)
	}
	var f func(a int) bool
	switch op {
	case "=":
		f = func(a int) bool { return a == n }
	case "!=":
		f = func(a int) bool { return a != n }
	case "<":
		f = func(a int) bool { return a < n }
	case ">":
		f = func(a int) bool { return a > n }
	case "<=":
		f = func(a int) bool { return a <= n }
	case ">=":
		f = func(a int) bool { return a >= n }
	default:
		return nil, fmt.Errorf("bad operator %q", op)
	}
	return func(t time.Time) bool { return f(get(t)) }, nil
}

func monthDays(t time.Time) int {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.Local).Day()
}

var vars = map[string]func(time.Time) int{
	"w": func(t time.Time) int { return int(t.Weekday()) },
	"m": func(t time.Time) int { return int(t.Month()) },
	"d": func(t time.Time) int { return t.Day() },
	"y": func(t time.Time) int { return t.Year() },
	"a": func(t time.Time) int { return (t.Day()-1)/7 + 1 },
	"b": func(t time.Time) int { return (monthDays(t)-t.Day())/7 + 1 },
	"n": monthDays,
}
