export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
export GIT_AUTHOR_NAME=fixture GIT_AUTHOR_EMAIL=fixture@example.com GIT_AUTHOR_DATE="2026-01-01T00:00:00Z"
export GIT_COMMITTER_NAME=fixture GIT_COMMITTER_EMAIL=fixture@example.com GIT_COMMITTER_DATE="2026-01-01T00:00:00Z"

start_repo() {
	repo=$1
	mkdir -p "$repo"
	cd "$repo"
	git init -q -b main
}

commit_all() {
	git add -A
	git commit -q -m fixture
}
