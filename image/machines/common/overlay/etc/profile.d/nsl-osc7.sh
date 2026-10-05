# nsl: report the working directory to the terminal at each prompt.
#
# VTE terminals, such as GNOME Console, Ptyxis and Igloo, open a new tab in
# the directory a shell reports with OSC 7, as VTE's vte.sh does. Some
# distributions ship vte.sh only inside the VTE library package, so every
# machine image carries this instead, and it stands aside whenever vte.sh is
# loaded too. Like vte.sh, it acts only in interactive bash and zsh under a
# VTE terminal.

# POSIX shells read this file too; return before any bash or zsh syntax.
[ -n "${BASH_VERSION:-}${ZSH_VERSION:-}" ] || return 0
case $- in *i*) ;; *) return 0 ;; esac
[ "${VTE_VERSION:-0}" -ge 3405 ] 2>/dev/null || return 0

__nsl_osc7() {
	# vte.sh, sourced after this file or from a user's rc, reports it itself.
	typeset -f __vte_osc7 >/dev/null 2>&1 && return 0
	# Percent-encode the directory byte by byte as a file URI path.
	local LC_ALL=C dir="$PWD" encoded="" char code i
	for (( i = 0; i < ${#dir}; i++ )); do
		char="${dir:$i:1}"
		case "$char" in
		[-/._~A-Za-z0-9]) encoded+="$char" ;;
		*)
			printf -v code '%%%02X' "'$char"
			encoded+="$code"
			;;
		esac
	done
	printf '\033]7;file://%s%s\033\\' "${HOSTNAME:-${HOST:-}}" "$encoded"
}

if [ -n "${ZSH_VERSION:-}" ]; then
	[[ " ${precmd_functions[*]} " == *" __nsl_osc7 "* ]] || precmd_functions+=(__nsl_osc7)
else
	[[ ";${PROMPT_COMMAND:-};" == *";__nsl_osc7;"* ]] || PROMPT_COMMAND="__nsl_osc7${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
fi
