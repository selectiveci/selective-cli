set -e
. "$(dirname "$0")/lib.sh"
start_repo "$1/repo"
printf 'one\n' > d
printf 'two\n' > keep.txt
commit_all
rm d
mkdir d
printf 'inner\n' > d/inner.txt
printf '%s' "$repo"
