_clipsend_complete() {
  local current="${COMP_WORDS[COMP_CWORD]}"
  local words='pair devices unpair rename doctor relay send --help --version'
  if (( COMP_CWORD == 1 )); then
    COMPREPLY=( $(compgen -W "$words" -- "$current") )
  elif [[ ${COMP_WORDS[1]} == pair ]]; then
    COMPREPLY=( $(compgen -W '--host --port --qr --code --help' -- "$current") )
  elif [[ ${COMP_WORDS[1]} == send ]]; then
    COMPREPLY=( $(compgen -W '--host --port --stdin --help' -- "$current") )
  else
    COMPREPLY=()
  fi
}
complete -F _clipsend_complete clipsend
