unalias {{NAME}} 2>/dev/null || true
function {{NAME}} {
	local dir
	dir=$(slate where -- "$@") || return
	cd -- "$dir"
}

# cobra ends its output with a :<directive> line, which only the last line is
function _slate_where_candidates {
	local -a lines
	lines=("${(@f)$(slate __complete where -- "$1" 2>/dev/null </dev/null)}")
	lines=("${(@)lines:#}")
	[[ ${lines[-1]} == :<-> ]] && lines=("${(@)lines[1,-2]}")
	(( $#lines )) && print -rl -- "${(@)lines%%$'\t'*}"
}

if (( $+functions[compdef] )); then
	function _slate_where {
		(( CURRENT == 2 )) || return 1
		local -a candidates
		candidates=("${(@f)$(_slate_where_candidates "$PREFIX")}")
		candidates=("${(@)candidates:#}")
		compadd -- "${candidates[@]}"
	}
	compdef _slate_where {{NAME}}
else
	function _slate_where_compctl {
		reply=("${(@f)$(_slate_where_candidates "$1")}")
		reply=("${(@)reply:#}")
	}
	compctl -x 'p[1]' -K _slate_where_compctl -- {{NAME}}
fi
