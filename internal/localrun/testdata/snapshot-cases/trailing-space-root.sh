set -e
. "$(dirname "$0")/lib.sh"
start_repo "$1/repo "
printf 'one\n' > a.txt
commit_all
printf '%s' "$repo"
