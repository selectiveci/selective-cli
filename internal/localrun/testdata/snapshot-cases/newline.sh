set -e
. "$(dirname "$0")/lib.sh"
start_repo "$1/repo"
printf 'one\n' > ok.txt
printf 'two\n' > 'a
b.txt'
commit_all
printf 'changed\n' > ok.txt
printf 'changed\n' > 'a
b.txt'
printf '%s' "$repo"
