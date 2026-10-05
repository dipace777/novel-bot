#!/usr/bin/env bash
# Development commands load shell-format .env defaults. Explicit environment
# overrides (including empty values) retain precedence. No values are printed.
set -eo pipefail

novelbot_env_file="${NOVELBOT_ENV_FILE:-.env}"
# Source the selected file directly, without searching PATH.
if [[ "$novelbot_env_file" != /* ]]; then novelbot_env_file="./$novelbot_env_file"; fi
novelbot_env_keys=()
novelbot_env_values=()

if [[ -f "$novelbot_env_file" ]]; then
  while IFS= read -r novelbot_env_line || [[ -n "$novelbot_env_line" ]]; do
    if [[ "$novelbot_env_line" =~ ^[[:space:]]*(export[[:space:]]+)?([a-zA-Z_][a-zA-Z0-9_]*)= ]]; then
      novelbot_env_key="${BASH_REMATCH[2]}"
      if printenv "$novelbot_env_key" >/dev/null; then
        novelbot_env_keys+=("$novelbot_env_key")
        novelbot_env_values+=("${!novelbot_env_key}")
      fi
    fi
  done < "$novelbot_env_file"

  set -a
  # .env follows the same trusted shell-assignment format as manually sourcing it.
  source "$novelbot_env_file"
  set +a

  for ((novelbot_env_index=0; novelbot_env_index<${#novelbot_env_keys[@]}; novelbot_env_index++)); do
    export "${novelbot_env_keys[$novelbot_env_index]}=${novelbot_env_values[$novelbot_env_index]}"
  done
fi

exec "$@"
