unalias dev 2>/dev/null || true
function dev {
	local dir
	dir=$(slate where -- "$@") || return
	cd -- "$dir"
}

# cobra ends its output with a :<directive> line, which only the last line is
function _dev_candidates {
	local -a lines
	local line
	while IFS= read -r line; do lines[${#lines[@]}]=$line; done < <(slate __complete where -- "$1" 2>/dev/null </dev/null)
	[ "${#lines[@]}" -gt 0 ] && [[ ${lines[$((${#lines[@]}-1))]} =~ ^:[0-9]+$ ]] && unset "lines[$((${#lines[@]}-1))]"
	for line in "${lines[@]}"; do printf '%s\n' "${line%%$'\t'*}"; done
}

# the word is tokenised like the shell would, so quotes and escapes anywhere in
# it count; readline replaces the text after an open quote verbatim, otherwise
# the text after the last unquoted COMP_WORDBREAKS character (a @ or $ break
# stays part of it), whatever COMP_WORDS says
function _dev_complete {
	local line n k c q word text before quoted inword argn redir
	line="${COMP_LINE:0:$COMP_POINT}"
	n=${#line}
	q=""; k=0; word=""; text=""; before=""; inword=""; argn=0; redir=""
	while [ "$k" -lt "$n" ]; do
		c="${line:$k:1}"
		if [ -n "$q" ]; then
			if [ "$c" = "$q" ]; then
				q=""
			else
				if [ "$q" = '"' ] && [ "$c" = "\\" ] && [ "$k" -lt "$((n-1))" ]; then
					case "${line:$((k+1)):1}" in
						\$|\`|\"|\\) k=$((k+1)); c="${line:$k:1}" ;;
					esac
				fi
				word="$word$c"; text="$text$c"
			fi
		else
			case "$c" in
				\\) k=$((k+1)); c="${line:$k:1}"; word="$word$c"; text="$text$c"; inword=1 ;;
				\"|\') q=$c; before=$word; inword=1 ;;
				" "|$'\t')
					if [ -n "$inword" ]; then
						if [ -n "$redir" ]; then
							redir=""
						else
							case "$word" in
								[0-9]\>*|[0-9]\<*|\>*|\<*|\&\>*)
									case "$word" in [0-9]\>|[0-9]\<|\>|\<|\>\>|[0-9]\>\>|\&\>|\&\>\>) redir=1 ;; esac ;;
								*) argn=$((argn+1)) ;;
							esac
						fi
					fi
					word=""; text=""; inword="" ;;
				"&")
					if { [ "$k" -gt 0 ] && case "${line:$((k-1)):1}" in \>|\<) true ;; *) false ;; esac; } || [ "${line:$((k+1)):1}" = ">" ]; then
						word="$word$c"; inword=1
					else
						argn=0; word=""; text=""; inword=""
					fi ;;
				";"|"|"|"("|")"|$'\n') argn=0; word=""; text=""; inword="" ;;
				*)
					word="$word$c"; inword=1
					case "$COMP_WORDBREAKS" in
						*"$c"*) case "$c" in @|\$) text=$c ;; *) text="" ;; esac ;;
						*) text="$text$c" ;;
					esac ;;
			esac
		fi
		k=$((k+1))
	done
	COMPREPLY=()
	[ "$argn" -eq 1 ] || return
	if [ -n "$q" ]; then
		quoted=1
		text="${word#"$before"}"
	else
		quoted=""
	fi
	while IFS= read -r c; do
		[ -n "$c" ] || continue
		case "$c" in "$word"*)
			c="${c#"${word%"$text"}"}"
			if [ -z "$quoted" ]; then
				c=$(printf '%q' "$c")
			elif [ "$q" = '"' ]; then
				c=${c//\\/\\\\}; c=${c//\$/\\\$}; c=${c//\`/\\\`}; c=${c//\"/\\\"}; c=${c//!/\"\'!\'\"}
			else
				c=${c//\'/\'\\\'\'}
			fi
			COMPREPLY[${#COMPREPLY[@]}]=$c ;;
		esac
	done <<CANDIDATES
$(_dev_candidates "$word")
CANDIDATES
}
complete -F _dev_complete dev
