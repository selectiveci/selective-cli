set -e
. "$(dirname "$0")/lib.sh"
start_repo "$1/repo"
printf 'one\n' > a.txt
printf 'two\n' > b.txt
ln -s a.txt link
commit_all
rm link
ln -s b.txt link
printf '%s' "$repo"
