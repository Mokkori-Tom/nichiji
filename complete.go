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
	# set / done / rm: そのまま書いた語は -match と同じ。-all があれば済んだ予定も候補に出す
	local closed= other= w= n=0
	case $sub in set | done | rm)
		for ((i = 2; i < cword; i++)); do
			case ${words[i]} in
			-f) ((i++)) ;;
			-all) closed=all ;;
			-match) w=${words[i+1]} n=1 && ((i++)) ;;
			-*) other=1 ;;
			*) w=${words[i]} && ((n++)) ;;
			esac
		done
		[[ -z $other && $n == 0 && $cur != -* && $prev != -f ]] && prev=-match
		# 語の後ろにスペースを空けて Tab を押したら、続きの -d -text -m を今の値で出す
		if [[ $sub == set && -z $cur && -z $other && $n == 1 ]] && ((cword == ${#words[@]} - 1)); then
			w=${w#[\'\"]} w=${w%[\'\"]} w=${w//\\/}
			if [[ $w ]] && w=$(nichiji __complete rest "$file" "$w" 2>/dev/null) && [[ $w ]]; then
				compopt +o filenames +o default -o nospace -o noquote 2>/dev/null
				COMPREPLY=("$w")
				return
			fi
		fi
		;;
	esac
	local typed=${cur#[\'\"]}
	typed=${typed//\\/}
	case $prev in
	-match | -at)
		[[ $prev == -match ]] && c=match || c=at
		compopt -o filenames +o default 2>/dev/null
		local all=() hit=()
		while IFS= read -r i; do all+=("$i"); done < <(nichiji __complete "$c" "$file" $closed 2>/dev/null)
		for i in "${all[@]}"; do [[ $i == "$typed"* ]] && hit+=("$i"); done
		# -match は部分一致なので、先頭で当たらなければ途中も探す
		if ((${#hit[@]} == 0)); then
			for i in "${all[@]}"; do [[ $i == *"$typed"* ]] && hit+=("$i"); done
		fi
		COMPREPLY=("${hit[@]}")
		# set の候補は今の日時・内容・メモまで含める。メニューで選んでも全部入るように
		if [[ $sub == set && $c == match && $cur != [\'\"]* && $typed != *:* ]] && ((cword == ${#words[@]} - 1)) && ((${#hit[@]})); then
			local full=()
			while IFS= read -r i; do full+=("$i"); done < <(nichiji __complete expand "$file" "${hit[@]}" 2>/dev/null)
			if ((${#full[@]})); then
				compopt +o filenames -o nospace -o noquote 2>/dev/null
				COMPREPLY=("${full[@]}")
				return
			fi
		fi
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
	# set / done / rm はファイル名を引数に取らない(-f の値は上で扱う)
	case $sub in set | done | rm)
		compopt +o default 2>/dev/null
		compopt -o ble/no-default 2>/dev/null # ble.sh は +o default だけではファイル名を出す
		;;
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
	case "expand", "rest":
		if len(args) > 2 {
			expand(path, args[2:], args[0] == "expand", shWord, shQuote)
		}
	case "fexpand", "frest":
		if len(args) > 2 {
			expand(path, args[2:], args[0] == "fexpand", fishQuote, fishQuote)
		}
	case "match", "matchdesc", "at":
		f, err := load(path)
		if err != nil {
			return
		}
		seen := map[string]bool{}
		showClosed := len(args) > 2 && args[2] == "all"
		for _, e := range f.Entries() {
			if isDone(e) && !showClosed { // 済んだ予定は -all のときだけ
				continue
			}
			c := ""
			if args[0] != "at" {
				// 複数行の内容は1行目だけ(-match は部分一致なので当たる)
				c, _, _ = strings.Cut(e.Text, "\n")
			} else if e.Kind == kDate || e.Kind == kTime { // -at で選べるのは日時のある予定だけ
				c = formatWhen(e.At, e.Kind)
			}
			if c != "" && !seen[c] {
				seen[c] = true
				if args[0] == "matchdesc" { // fish の候補の説明: 今の日時とメモ
					fmt.Println(c + "\t" + entryDesc(e))
				} else {
					fmt.Println(c)
				}
			}
		}
	}
}

// expand は語ごとに、1件に決まる予定を set の続きとして今の値で1行ずつ書き出す。
// 内容の1行目が同じものを優先し、無ければ部分一致で探す(set の選び方と同じ)。
// withWord なら先頭に語も付ける(語の途中で Tab を押したときは語ごと置き換えるため)。
// 決まらない語は、候補が複数なら語だけを出し、1語だけなら何も出さない。
func expand(path string, words []string, withWord bool, qWord, q func(string) string) {
	f, err := load(path)
	if err != nil {
		return
	}
	for _, word := range words {
		e, err := findEntry(f, word, "")
		if err != nil {
			if withWord && len(words) > 1 {
				fmt.Println(qWord(word))
			}
			continue
		}
		if withWord {
			fmt.Print(qWord(word), " ")
		}
		fmt.Println("-d", q(e.Raw), "-text", q(e.Text), "-m", q(e.Memo))
	}
}

// entryDesc は補完の候補に添える説明。1行に収める。
func entryDesc(e *Entry) string {
	d := e.Raw
	if d == "" {
		d = "undated"
	}
	if e.Memo != "" {
		d += " | " + e.Memo
	}
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(d)
}

// shWord は語を \\ で1語にする。引用符で始めないので、打ちかけの語と先頭が揃い、共通部分の補完が効く。
func shWord(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(" \t'\"\\$`&|;<>()*?[]#~=%!{},^", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// shQuote はシェルの1語に引用する。改行は1行に収まるよう $'...' で書く。
func shQuote(s string) string {
	if strings.ContainsAny(s, "\n\r\t") {
		return "$'" + strings.NewReplacer("\\", "\\\\", "'", "\\'", "\n", "\\n", "\r", "\\r", "\t", "\\t").Replace(s) + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func completion(args []string) {
	switch {
	case len(args) == 1 && args[0] == "bash":
		os.Stdout.WriteString(bashCompletion)
	case len(args) == 1 && args[0] == "fish":
		os.Stdout.WriteString(fishCompletion)
	default:
		die(fmt.Errorf("usage: nichiji completion bash|fish"))
	}
}
