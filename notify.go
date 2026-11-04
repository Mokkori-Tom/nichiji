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

// find は全期間・未定・繰り返しを含めて、内容とメモを探す(TUI の / と同じあいまい検索)。
func find(w io.Writer, files []*File, q string) int {
	n := 0
	for _, f := range files {
		for _, e := range searchEntries(f.Entries(), q) { // / と同じ探し方(あいまい、そのまま含むものが先)
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
	// 時刻の無い予定(未定・日付だけ・時刻の無い繰り返し)があれば1日1回だけ促す
	if us := undatedEntries(files); len(us) > 0 {
		out = append(out, notice{"undated|" + day(n).Format("2006-01-02"),
			fmt.Sprintf("%d entries have no time: schedule them", len(us)), "first: " + short(us[0].Text, 40) + "  (nichiji ls -notime)"})
	}
	// 前の日の LATE は上で1回知らせたきりなので、済ませるか取り消すまで1日1回促す
	if ls := pastLate(files, 0); len(ls) > 0 {
		out = append(out, notice{"latepast|" + day(n).Format("2006-01-02"),
			fmt.Sprintf("%d past entries are not done (LATE): check them", len(ls)), "oldest: " + short(ls[0].e.Text, 40) + "  (nichiji ls -overdue)"})
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
// 無ければデスクトップの通知に出し、それも無ければ端末に出してベルを鳴らす。
func send(cmd string, nt notice) error {
	title := "nichiji: " + nt.title
	if cmd != "" {
		args := strings.Fields(cmd)
		c := exec.Command(args[0], append(args[1:], title, nt.body)...)
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		return c.Run()
	}
	if ok, err := desktop(title, nt.body); ok {
		return err
	}
	_, err := fmt.Printf("\a%s  %s  %s\n", now().Format("15:04"), title, nt.body)
	return err
}

// desktop は termux-notification → notify-send → (WSL なら) Windows の通知、の順に探して出す。
// どれも無ければ ok は false。
func desktop(title, body string) (ok bool, err error) {
	if p, err := exec.LookPath("termux-notification"); err == nil {
		return true, exec.Command(p, "--title", title, "--content", body).Run()
	}
	if p, err := exec.LookPath("notify-send"); err == nil {
		return true, exec.Command(p, title, body).Run()
	}
	if isWSL() {
		if p, err := exec.LookPath("powershell.exe"); err == nil {
			// WSL: Windows の通知(トースト)。文字は環境変数で渡して、引用の崩れを避ける
			c := exec.Command(p, "-NoProfile", "-NonInteractive", "-Command", wslToast)
			c.Env = append(os.Environ(), "NJ_TITLE="+title, "NJ_BODY="+body, "WSLENV=NJ_TITLE/u:NJ_BODY/u")
			return true, c.Run()
		}
	}
	return false, nil
}

// tmuxNotify は tmux に繋いでいるすべての画面の下の行に、10秒だけ msg を出す(-cmd が無いときは、デスクトップの通知と両方に出す)。
// cron などから呼んでも届くよう、$TMUX ではなく繋いでいる画面の有無で決める。出せなければ false。
func tmuxNotify(msg string) bool {
	p, err := exec.LookPath("tmux")
	if err != nil {
		return false
	}
	out, err := exec.Command(p, "list-clients", "-F", "#{client_name}").Output()
	if err != nil {
		return false
	}
	msg = strings.ReplaceAll(msg, "#", "##") // # は tmux の書式の印
	sent := false
	for _, c := range strings.Fields(string(out)) {
		if exec.Command(p, "display-message", "-c", c, "-d", "10000", msg).Run() == nil {
			sent = true
		}
	}
	return sent
}

// isWSL は WSL の中で動いているか。
func isWSL() bool {
	b, err := os.ReadFile("/proc/version")
	return err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft")
}

// wslToast は PowerShell から Windows のトーストを出す(追加のモジュールは要らない)。
const wslToast = `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$t = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$x = $t.GetElementsByTagName('text')
$x.Item(0).AppendChild($t.CreateTextNode($env:NJ_TITLE)) > $null
$x.Item(1).AppendChild($t.CreateTextNode($env:NJ_BODY)) > $null
$id = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe'
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($id).Show([Windows.UI.Notifications.ToastNotification]::new($t))`

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
	var due []notice
	for _, nt := range dueNotices(files, before) {
		if _, done := st[nt.key]; !done {
			due = append(due, nt)
		}
	}
	// -cmd が無ければ tmux とデスクトップの両方に出す。
	// tmux の表示は後のもので上書きされるので、1回分をまとめて1行で出す
	inTmux := false
	if cmd == "" && len(due) > 0 {
		var ms []string
		for _, nt := range due {
			ms = append(ms, nt.title+"  "+nt.body)
		}
		inTmux = tmuxNotify("nichiji: " + strings.Join(ms, "  |  "))
	}
	for _, nt := range due {
		var err error
		if inTmux {
			// tmux に出せたので、デスクトップが無くても失敗にせず、端末にも出さない
			_, err = desktop("nichiji: "+nt.title, nt.body)
		} else {
			err = send(cmd, nt)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "notify:", err)
			if !inTmux {
				continue
			}
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
