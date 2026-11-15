#!/bin/sh
set -e
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
export GIT_AUTHOR_NAME=fixture GIT_AUTHOR_EMAIL=fixture@example.com GIT_AUTHOR_DATE="2026-01-01T00:00:00Z"
export GIT_COMMITTER_NAME=fixture GIT_COMMITTER_EMAIL=fixture@example.com GIT_COMMITTER_DATE="2026-01-01T00:00:00Z"
repo=$1
mkdir -p "$repo/sub"
cd "$repo"
git init -q -b main
printf 'one\n' > a.txt
printf 'two\n' > b.txt
printf 'three\n' > sub/c.txt
printf 'four\n' > 'sp ace.txt'
printf 'five\n' > 'ünï.txt'
printf 'six\n' > gone.txt
printf '#!/bin/sh\n' > run.sh
chmod +x run.sh
git add -A
git commit -q -m fixture
printf 'edited\n' > a.txt
rm b.txt
git rm -q gone.txt
printf 'new\n' > staged.txt
git add staged.txt
printf 'loose\n' > untracked.txt
printf 'edited\n' > sub/c.txt
