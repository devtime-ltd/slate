unalias dev 2>/dev/null || true
function dev {
	local dir
	dir=$(slate where -- "$@") || return
	cd -- "$dir"
}

# cobra ends its output with a :<directive> line, which only the last line is
function _dev_candidates {
	local -a lines
	lines=("${(@f)$(slate __complete where -- "$1" 2>/dev/null </dev/null)}")
	lines=("${(@)lines:#}")
	[[ ${lines[-1]} == :<-> ]] && lines=("${(@)lines[1,-2]}")
	(( $#lines )) && print -rl -- "${(@)lines%%$'\t'*}"
}

if (( $+functions[compdef] )); then
	function _dev {
		(( CURRENT == 2 )) || return 1
		local -a candidates
		candidates=("${(@f)$(_dev_candidates "$PREFIX")}")
		candidates=("${(@)candidates:#}")
		compadd -- "${candidates[@]}"
	}
	compdef _dev dev
else
	function _dev_compctl {
		reply=("${(@f)$(_dev_candidates "$1")}")
		reply=("${(@)reply:#}")
	}
	compctl -x 'p[1]' -K _dev_compctl -- dev
fi
