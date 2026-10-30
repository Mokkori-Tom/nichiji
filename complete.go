package main

import (
	"fmt"
	"os"
	"strings"
)

const commands = "ls add set done rm find free check notify completion"

// bashCompletion は bash のタブ補完。候補はその場で nichiji 自身に聞くので、コマンドや予定が増えても直さなくてよい。
const bashCompletion = `_nichiji() {
	local cur prev words cword
	if declare -F _get_comp_words_by_ref >/dev/null; then
		_get_comp_words_by_ref -n : cur prev words cword
	else
		words=("${COMP_WORDS[@]}") cword=$COMP_CWORD
		cur=${COMP_WORDS[COMP_CWORD]} prev=${COMP_WORDS[COMP_CWORD-1]}
	fi
	local sub= file= i c
	case " ` + commands + ` " in *" ${words[1]} "*) ((cword > 1)) && sub=${words[1]} ;; esac
	for ((i = 1; i < cword; i++)); do
		[[ ${words[i]} == -f ]] && file=${words[i+1]}
	done
	file=${file/#\~/$HOME}
	local typed=${cur#[\'\"]}
	typed=${typed//\\/}
	case $prev in
	-match | -at)
		[[ $prev == -match ]] && c=match || c=at
		compopt -o filenames +o default 2>/dev/null
		local all=() hit=()
		while IFS= read -r i; do all+=("$i"); done < <(nichiji __complete "$c" "$file" 2>/dev/null)
		for i in "${all[@]}"; do [[ $i == "$typed"* ]] && hit+=("$i"); done
		# -match は部分一致なので、先頭で当たらなければ途中も探す
		if ((${#hit[@]} == 0)); then
			for i in "${all[@]}"; do [[ $i == *"$typed"* ]] && hit+=("$i"); done
		fi
		COMPREPLY=("${hit[@]}")
		declare -F __ltrim_colon_completions >/dev/null && __ltrim_colon_completions "$typed"
		return
		;;
	-status)
		COMPREPLY=($(compgen -W "reserved running done canceled" -- "$cur"))
		return
		;;
	-d | -m | -text | -note | -end | -now | -days | -past | -len | -from | -to | -before)
		compopt +o default 2>/dev/null
		return
		;;
	-f | -cmd) return ;; # ファイル名の補完に任せる
	esac
	if [[ $cur == -* ]]; then
		COMPREPLY=($(compgen -W "$(nichiji __complete flags "$sub" 2>/dev/null)" -- "$cur"))
	elif ((cword == 1)); then
		COMPREPLY=($(compgen -W "` + commands + `" -- "$cur"))
	fi
}
complete -o default -F _nichiji nichiji
`

// complete はタブ補完の候補を1行に1つ出す。補完の途中なので、読めないファイルなどはエラーにしない。
func complete(args []string) {
	if len(args) == 0 {
		return
	}
	path := defaultFile()
	if len(args) > 1 && args[1] != "" {
		path = args[1]
	}
	switch args[0] {
	case "flags":
		sub := ""
		if len(args) > 1 {
			sub = args[1]
		}
		fmt.Println("-now")
		for _, name := range strings.Fields(allowed[sub]) {
			fmt.Println("-" + name)
		}
	case "match", "at":
		f, err := load(path)
		if err != nil {
			return
		}
		seen := map[string]bool{}
		for _, e := range f.Entries() {
			c := ""
			if args[0] == "match" {
				// 複数行の内容は1行目だけ(-match は部分一致なので当たる)
				c, _, _ = strings.Cut(e.Text, "\n")
			} else if e.Kind == kDate || e.Kind == kTime { // -at で選べるのは日時のある予定だけ
				c = formatWhen(e.At, e.Kind)
			}
			if c != "" && !seen[c] {
				seen[c] = true
				fmt.Println(c)
			}
		}
	}
}

func completion(args []string) {
	if len(args) != 1 || args[0] != "bash" {
		die(fmt.Errorf("usage: nichiji completion bash"))
	}
	fmt.Fprint(os.Stdout, bashCompletion)
}
