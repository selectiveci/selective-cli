set -e
. "$(dirname "$0")/lib.sh"
start_repo "$1/repo"
printf 'one\n' > a.txt
commit_all
printf 'not an index\n' > .git/index
printf '%s' "$repo"
