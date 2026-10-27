package main

import (
	"strings"
	"unicode/utf8"

	unorm "golang.org/x/text/unicode/norm"
)

// fold は検索用に、全角・半角(NFKC)、大文字・小文字、ひらがな・カタカナの違いをならす。
func fold(s string) string {
	s = strings.ToLower(unorm.NFKC.String(s))
	b := make([]rune, 0, utf8.RuneCountInString(s))
	for _, r := range s {
		if r >= 'ぁ' && r <= 'ゖ' {
			r += 'ァ' - 'ぁ'
		}
		b = append(b, r)
	}
	return string(b)
}

// subseq は q の文字が s の中に順番どおりに出てくるか(間に別の文字があってよい)。
func subseq(s, q string) bool {
	rs := []rune(s)
	i := 0
	for _, r := range q {
		for i < len(rs) && rs[i] != r {
			i++
		}
		if i == len(rs) {
			return false
		}
		i++
	}
	return true
}

// matchLevel は q(空白区切りの語をすべて)が text にどう当たるか。2 = そのまま含む、1 = あいまい(順番どおり)、0 = 外れ。
func matchLevel(text, q string) int {
	t := fold(text)
	lv := 2
	for _, w := range strings.Fields(fold(q)) {
		switch {
		case strings.Contains(t, w):
		case subseq(t, w):
			lv = 1
		default:
			return 0
		}
	}
	return lv
}

// searchEntries は q に当たる予定を、そのまま含むものを先に、あいまいなものを後に返す。
func searchEntries(es []*Entry, q string) []*Entry {
	var exact, fuzzy []*Entry
	for _, e := range es {
		switch matchLevel(e.Text+"\n"+e.Memo, q) {
		case 2:
			exact = append(exact, e)
		case 1:
			fuzzy = append(fuzzy, e)
		}
	}
	return append(exact, fuzzy...)
}
