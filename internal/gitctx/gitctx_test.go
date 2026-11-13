package gitctx

import "testing"

func TestParseSlug(t *testing.T) {
	cases := map[string]string{
		"git@github.com:acme/web.git":                            "acme/web",
		"https://github.com/acme/web.git":                        "acme/web",
		"https://github.com/acme/web":                            "acme/web",
		"ssh://git@github.com/acme/web.git":                      "acme/web",
		"git@gitlab.com:acme/platform/web.git":                   "acme/platform/web",
		"https://gitlab.com/acme/platform/web.git":               "acme/platform/web",
		"https://gitlab-ci-token:x@gitlab.example.com/a/b/c.git": "a/b/c",
		"ssh://git@gitlab.example.com:2222/a/b.git":              "a/b",
		"https://git.example.com/gitlab/acme/web/":               "gitlab/acme/web",
	}
	for url, want := range cases {
		got, err := parseSlug(url)
		if err != nil || got != want {
			t.Errorf("parseSlug(%q) = %q, %v; want %q", url, got, err, want)
		}
	}

	for _, bad := range []string{"https://github.com/acme", "nonsense"} {
		if got, err := parseSlug(bad); err == nil {
			t.Errorf("parseSlug(%q) = %q, want an error", bad, got)
		}
	}
}

func TestProviderOf(t *testing.T) {
	cases := map[string]string{
		"git@github.com:acme/web.git":                  "github",
		"https://github.com/acme/web":                  "github",
		"git@gitlab.com:acme/web.git":                  "gitlab",
		"https://token:x@gitlab.acme.io/a/b.git":       "gitlab",
		"ssh://git@gitlab.acme.io:2222/a/b.git":        "gitlab",
		"https://git.acme.io/a/b.git":                  "",
		"https://github.com.evil.example/acme/web.git": "",
	}
	for url, want := range cases {
		if got := providerOf(url); got != want {
			t.Errorf("providerOf(%q) = %q, want %q", url, got, want)
		}
	}
}
