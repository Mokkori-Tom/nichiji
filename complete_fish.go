package main

import "strings"

// fishCompletion は fish の補完。fish は候補を必ず引用して挿入するので、-d -text -m まで入れた行を候補にできない。
// 候補には今の値を説明として添え、展開は Tab と、メニューを閉じる Enter の割り当てで行う。
const fishCompletion = `function __nichiji_parse -S
	set t (commandline -opc)
	set cur (commandline -ct)
	set sub
	set file
	set closed
	set w
	set n 0
	set other
	if set -q t[2]; and contains -- $t[2] ` + commands + `
		set sub $t[2]
	end
	set -l i 3
	while test $i -le (count $t)
		switch $t[$i]
			case -f
				set i (math $i + 1)
				set -q t[$i]; and set file (string replace -r '^~' $HOME -- $t[$i])
			case -all
				set closed all
			case -match
				set i (math $i + 1)
				set -q t[$i]; and set w $t[$i]; and set n 1
			case '-*'
				set other 1
			case '*'
				set w $t[$i]
				set n (math $n + 1)
		end
		set i (math $i + 1)
	end
	test -n "$w"; and set w (string unescape -- $w)
end

function __nichiji_candidates
	set -l t; set -l cur; set -l sub; set -l file; set -l closed; set -l w; set -l n; set -l other
	__nichiji_parse
	switch "$t[-1]"
		case -f -cmd
			__fish_complete_path $cur
			return
		case -status
			printf '%s\n' reserved running done canceled
			return
		case -d -m -text -note -end -now -days -past -len -from -to -before
			return
		case -at
			nichiji __complete at "$file" $closed 2>/dev/null
			return
		case -match
			nichiji __complete matchdesc "$file" $closed 2>/dev/null
			return
	end
	if string match -q -- '-*' "$cur"
		nichiji __complete flags "$sub" 2>/dev/null
		return
	end
	switch "$sub"
		case set done rm
			test $n -eq 0; and test -z "$other"; and nichiji __complete matchdesc "$file" $closed 2>/dev/null
		case add find free notify completion
		case '*'
			test (count $t) -eq 1; and printf '%s\n' ` + commands + `
			__fish_complete_path $cur
	end
end

# __nichiji_expand は set の行を今の値で展開する。展開したら 0 を返す。
# 引数 exact はメニューで選んだあとで、打たれた語を候補の絞り込みに使わず、そのまま選ぶ。
function __nichiji_expand
	set -l t; set -l cur; set -l sub; set -l file; set -l closed; set -l w; set -l n; set -l other
	__nichiji_parse
	test "$t[1]" = nichiji; and test "$sub" = set; and test -z "$other"; or return 1
	test (commandline -C) -eq (string length -- (commandline | string collect)); or return 1
	set -l out
	if test -z "$cur"; and test $n -eq 1
		set out (nichiji __complete frest "$file" "$w" 2>/dev/null)
		test -n "$out"; or return 1
		commandline -i -- $out
		return 0
	end
	test $n -eq 0; or test "$t[-1]" = -match; or return 1
	test -n "$cur"; or return 1
	string match -q -- '-*' "$cur"; and return 1
	set -l typed (string unescape -- $cur); or return 1
	set -l one $typed
	if test "$argv[1]" != exact
		set -l all (nichiji __complete match "$file" $closed 2>/dev/null)
		set -l hit (string match -- "$typed*" $all)
		test (count $hit) -gt 0; or set hit (string match -- "*$typed*" $all)
		test (count $hit) -eq 1; or return 1
		set one $hit
	end
	set out (nichiji __complete fexpand "$file" "$one" 2>/dev/null)
	test -n "$out"; or return 1
	commandline -rt -- $out
	return 0
end

function __nichiji_tab
	if commandline --paging-mode
		commandline -f complete
		return
	end
	__nichiji_expand; or commandline -f complete
end

# Enter でメニューを閉じたら、選んだ候補を展開する。メニューが無ければふつうの Enter のまま
function __nichiji_enter_before
	commandline --paging-mode; and set -g __nichiji_picked 1
end
function __nichiji_enter_after
	if set -q __nichiji_picked
		set -e __nichiji_picked
		commandline -t -- (string trim -- (commandline -ct))
		__nichiji_expand exact
		commandline -f repaint
	end
end

complete -c nichiji -f -a '(__nichiji_candidates)'
bind \t __nichiji_tab
bind \r __nichiji_enter_before execute __nichiji_enter_after
`

// fishQuote は fish の1語に引用する。単引用符の中でも \ と ' は特別で、改行は引用の外に \n で書く。
func fishQuote(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(l) + "'"
	}
	return strings.Join(lines, `\n`)
}
