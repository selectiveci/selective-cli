package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const missingSuiteID = "00000000-0000-0000-0000-000000000000"

type suitesStub struct {
	mu      sync.Mutex
	patched []string
}

func (s *suitesStub) start(t *testing.T, patchStatus int, patchBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/orgs/acme/suites":
			_, _ = w.Write([]byte(`{"suites":[]}`))
		case r.Method == "PATCH":
			s.mu.Lock()
			s.patched = append(s.patched, r.URL.Path)
			s.mu.Unlock()
			w.WriteHeader(patchStatus)
			_, _ = w.Write([]byte(patchBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func assertRefusal(t *testing.T, res cliResult, exit int, code string) {
	t.Helper()
	want := `"error":"` + code + `"`
	if res.exit != exit || !strings.Contains(res.stdout, want) {
		t.Errorf("exit %d stdout %q, want exit %d with %s", res.exit, res.stdout, exit, want)
	}
}

func TestConfigureUnknownSuiteIdLetsTheServerAnswerSuiteNotFound(t *testing.T) {
	stub := &suitesStub{}
	srv := stub.start(t, 404, `{"error":"suite_not_found","message":"No such suite in this organization."}`)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "configure", missingSuiteID, "--selection", "off", "--org", "acme", "--json")

	assertRefusal(t, res, 3, "suite_not_found")
	if len(stub.patched) != 1 || !strings.HasSuffix(stub.patched[0], "/"+missingSuiteID) {
		t.Errorf("PATCH requests = %v, want one for %s", stub.patched, missingSuiteID)
	}
}

func TestConfigureArchivedSuiteIdLetsTheServerAnswerSuiteArchived(t *testing.T) {
	stub := &suitesStub{}
	srv := stub.start(t, 409, `{"error":"suite_archived","message":"That suite is archived."}`)
	archivedID := "9f71dd4c-90d2-4685-a2bf-b983b46e89e5"

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "configure", archivedID, "--selection", "off", "--org", "acme", "--json")

	assertRefusal(t, res, 5, "suite_archived")
	if len(stub.patched) != 1 {
		t.Errorf("PATCH requests = %v, want one", stub.patched)
	}
}

func TestConfigureWithNothingToChangeIsAUsageError(t *testing.T) {
	stub := &suitesStub{}
	srv := stub.start(t, 200, `{}`)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "configure", missingSuiteID, "--org", "acme", "--json")

	assertRefusal(t, res, 5, "usage_error")
	if len(stub.patched) != 0 {
		t.Errorf("PATCH requests = %v, want none", stub.patched)
	}
}

func TestLogoutWithAnEnvTokenIsAUsageError(t *testing.T) {
	srv := (&suitesStub{}).start(t, 200, `{}`)

	res := runBinary(t, t.TempDir(), srv.URL, "auth", "logout", "--json")

	assertRefusal(t, res, 5, "usage_error")
}

func TestConfigureWithABadDefaultBranchModeIsAUsageError(t *testing.T) {
	srv := (&suitesStub{}).start(t, 200, `{}`)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "configure", missingSuiteID, "--default-branch", "sometimes", "--org", "acme", "--json")

	assertRefusal(t, res, 5, "usage_error")
}

func uncodedErrorConstructors(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					pkgName, ok := sel.X.(*ast.Ident)
					if !ok {
						return true
					}
					switch pkgName.Name + "." + sel.Sel.Name {
					case "errors.New":
						found = append(found, fset.Position(call.Pos()).String())
					case "fmt.Errorf":
						format, _ := call.Args[0].(*ast.BasicLit)
						if format == nil || !strings.Contains(format.Value, "%w") {
							found = append(found, fset.Position(call.Pos()).String())
						}
					}
					return true
				})
			}
		}
	}
	return found
}

func TestCommandPackagesCodeEveryRefusalAtItsSource(t *testing.T) {
	for _, dir := range []string{".", "../../internal/setup", "../../internal/auth", "../../internal/config", "../../internal/runs", "../../internal/tail", "../../internal/wait"} {
		for _, at := range uncodedErrorConstructors(t, dir) {
			t.Errorf("%s builds an uncoded error that would exit 9; use contract.Errorf, or wrap a cause with %%w", at)
		}
	}
}
