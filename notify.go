package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// find は全期間・未定・繰り返しを含めて、内容とメモ欄の部分一致を探す(大文字小文字は区別しない)。
func find(w io.Writer, files []*File, q string) int {
	q = strings.ToLower(q)
	n := 0
	for _, f := range files {
		for _, e := range f.Entries() {
			if !strings.Contains(strings.ToLower(e.Text+"\n"+e.Memo), q) {
				continue
			}
			when := e.Raw
			if when == "" {
				when = "undated"
			}
			s := fmt.Sprintf("%s:%d  %s  %s", f.Name(), e.line, when, strings.ReplaceAll(e.Text, "\n", "⏎"))
			if e.Memo != "" {
				s += "  | " + strings.ReplaceAll(e.Memo, "\n", "⏎")
			}
			fmt.Fprintln(w, s)
			n++
		}
	}
	return n
}

// ---- 通知 ----
// 始まる before 分前と、[reserved] [running] のまま終わりを過ぎたとき(LATE)に1回ずつ知らせる。
// 知らせた印は状態ファイルに残すので、cron などで毎分呼んでも二重にならない。

type notice struct {
	key, title, body string
}

func dueNotices(files []*File, before int) []notice {
	n := now()
	var out []notice
	for _, f := range files {
		for i := -1; i <= 1; i++ {
			d := day(n).AddDate(0, 0, i)
			for _, o := range occsOn(f.Entries(), d) {
				if !o.timed || isDone(o.e) {
					continue
				}
				base := fmt.Sprintf("%s|%s|%s|%s", f.Path, o.e.Raw, o.e.Text, o.at.Format(time.RFC3339))
				when := o.at.Format("15:04")
				if !o.end.IsZero() {
					when += "-" + endLabel(o.at, o.end)
				}
				body := f.Name()
				if o.e.Memo != "" {
					body += "  " + o.e.Memo
				}
				text := o.e.textOn(d)
				if lead := o.at.Sub(n); lead > 0 && lead <= time.Duration(before)*time.Minute {
					out = append(out, notice{"start|" + base, when + " " + text, body})
				}
				if overdue(o) {
					out = append(out, notice{"late|" + base, "LATE " + when + " " + text, body})
				}
			}
		}
	}
	// 未定があれば1日1回だけ促す
	if us := undatedEntries(files); len(us) > 0 {
		out = append(out, notice{"undated|" + day(n).Format("2006-01-02"),
			fmt.Sprintf("%d undated: schedule them", len(us)), "first: " + short(us[0].Text, 40) + "  (nichiji: press u)"})
	}
	return out
}

func statePath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "nichiji", "notified")
}

// loadState は知らせた印を読む。2日より古い印は捨てる。
func loadState(p string) map[string]time.Time {
	st := map[string]time.Time{}
	fh, err := os.Open(p)
	if err != nil {
		return st
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		at, key, ok := strings.Cut(sc.Text(), "\t")
		t, err := time.Parse(time.RFC3339, at)
		if ok && err == nil && now().Sub(t) < 48*time.Hour {
			st[key] = t
		}
	}
	return st
}

func saveState(p string, st map[string]time.Time) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	for k, t := range st {
		fmt.Fprintf(&b, "%s\t%s\n", t.Format(time.RFC3339), k)
	}
	return os.WriteFile(p, []byte(b.String()), 0o644)
}

// send は通知を出す。-cmd があればそれに題と本文を引数で渡す。
// 無ければ termux-notification → notify-send の順に探し、どちらも無ければ端末に出してベルを鳴らす。
func send(cmd string, nt notice) error {
	title := "nichiji: " + nt.title
	if cmd != "" {
		args := strings.Fields(cmd)
		c := exec.Command(args[0], append(args[1:], title, nt.body)...)
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		return c.Run()
	}
	if p, err := exec.LookPath("termux-notification"); err == nil {
		return exec.Command(p, "--title", title, "--content", nt.body).Run()
	}
	if p, err := exec.LookPath("notify-send"); err == nil {
		return exec.Command(p, title, nt.body).Run()
	}
	_, err := fmt.Printf("\a%s  %s  %s\n", now().Format("15:04"), title, nt.body)
	return err
}

func notifyOnce(paths []string, before int, cmd string) error {
	var files []*File
	for _, p := range paths {
		f, err := load(p)
		if err != nil {
			return err
		}
		files = append(files, f)
	}
	sp := statePath()
	st := loadState(sp)
	for _, nt := range dueNotices(files, before) {
		if _, done := st[nt.key]; done {
			continue
		}
		if err := send(cmd, nt); err != nil {
			fmt.Fprintln(os.Stderr, "notify:", err)
			continue
		}
		st[nt.key] = now()
	}
	return saveState(sp, st)
}

// notifyWatch は毎分の頭に notifyOnce を呼び続ける(ファイルは毎回読み直す)。
func notifyWatch(paths []string, before int, cmd string) {
	for {
		if err := notifyOnce(paths, before, cmd); err != nil {
			fmt.Fprintln(os.Stderr, "notify:", err)
		}
		n := now()
		time.Sleep(n.Truncate(time.Minute).Add(time.Minute).Sub(n) + time.Second)
	}
}
