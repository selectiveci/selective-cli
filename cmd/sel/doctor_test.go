package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/doctor"
	"github.com/zalando/go-keyring"
)

const (
	doctorToken = "sk_doctor_wiring_secret_5d2e"
	revokedBody = `{"error":"unauthorized","message":"Token revoked."}`
)

type doctorDocument struct {
	V      int    `json:"v"`
	Error  string `json:"error"`
	Doctor struct {
		Status string `json:"status"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Code   string `json:"code"`
		} `json:"checks"`
	} `json:"doctor"`
}

func doctorServer(t *testing.T, meStatus int, meBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/me":
			w.WriteHeader(meStatus)
			_, _ = w.Write([]byte(meBody))
		case "/api/v1/orgs":
			_, _ = w.Write([]byte(`{"organizations":[{"slug":"acme","name":"Acme","role":"owner"}]}`))
		case "/api/v1/orgs/acme/suites":
			_, _ = w.Write([]byte(`{"suites":[{"id":"s-1","name":"RSpec","type":"rspec","repo":"acme/web","last_run_at":null}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const meGreen = `{"user_id":"u1","email":"ben@example.com","org_slugs":["acme"]}`

func checkoutOfAcmeWeb(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://github.com/acme/web.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func cleanDoctorEnv(t *testing.T) {
	t.Helper()
	keyring.MockInit()
	t.Setenv("SELECTIVE_ORG", "")
	t.Setenv("SELECTIVE_HOST", "")
}

func oneLineDocument(t *testing.T, stdout string) doctorDocument {
	t.Helper()
	if strings.Count(stdout, "\n") != 1 || !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("stdout is not exactly one line: %q", stdout)
	}
	var doc doctorDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v: %q", err, stdout)
	}
	return doc
}

func TestDoctorPassingRunPrintsOneDocumentAndExitsZero(t *testing.T) {
	cleanDoctorEnv(t)
	srv := doctorServer(t, 200, meGreen)

	res := runBinary(t, checkoutOfAcmeWeb(t), srv.URL, "doctor", "--json")

	doc := oneLineDocument(t, res.stdout)
	counts := map[string]int{}
	for _, c := range doc.Doctor.Checks {
		counts[c.Status]++
	}
	if res.exit != 0 || doc.V != 1 || doc.Doctor.Status != "pass" || counts["ok"] != 5 || counts["skip"] != 3 {
		t.Errorf("exit %d, doc %+v, counts %v\nstderr %q", res.exit, doc, counts, res.stderr)
	}
}

func TestDoctorFailingRunPrintsOneErrorObjectCarryingEveryCheck(t *testing.T) {
	cleanDoctorEnv(t)
	srv := doctorServer(t, 401, revokedBody)

	res := runBinary(t, checkoutOfAcmeWeb(t), srv.URL, "doctor", "--json")

	doc := oneLineDocument(t, res.stdout)
	names := make([]string, len(doc.Doctor.Checks))
	for i, c := range doc.Doctor.Checks {
		names[i] = c.Name + "=" + c.Status
	}
	want := "config=ok server=ok auth=fail org=skip suite=skip socket=skip runner=skip cli_version=skip"
	if res.exit != 4 || doc.V != 1 || doc.Error != "unauthorized" || doc.Doctor.Status != "fail" || strings.Join(names, " ") != want {
		t.Errorf("exit %d, doc %+v, checks %v", res.exit, doc, names)
	}
	if !strings.HasPrefix(res.stderr, "Error: ") {
		t.Errorf("stderr = %q", res.stderr)
	}
}

func TestDoctorNeverPrintsTheToken(t *testing.T) {
	for name, status := range map[string]int{"green": 200, "revoked": 401} {
		t.Run(name, func(t *testing.T) {
			cleanDoctorEnv(t)
			t.Setenv("SELECTIVE_TOKEN", doctorToken)
			t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())
			body := meGreen
			if status == 401 {
				body = revokedBody
			}
			srv := doctorServer(t, status, body)

			_, stdout, stderr := execute("doctor", "--hostname", srv.URL, "--org", "acme", "--json")
			report := doctor.Run(doctor.Options{Hostname: srv.URL, Org: "acme", Timeout: time.Minute})
			var checklist bytes.Buffer
			if err := writeChecklist(&checklist, report); err != nil {
				t.Fatal(err)
			}

			if len(report.Checks) != 8 || !strings.Contains(stdout, `"doctor"`) {
				t.Fatalf("the probe didn't run doctor: stdout %q, %d checks", stdout, len(report.Checks))
			}
			for label, text := range map[string]string{"stdout": stdout, "stderr": stderr, "checklist": checklist.String()} {
				if strings.Contains(text, doctorToken) {
					t.Errorf("%s contains the token: %s", label, text)
				}
			}
		})
	}
}

func TestChecklistMarksEachStatus(t *testing.T) {
	report := &doctor.Report{Checks: []doctor.Check{
		{Name: "config", Status: doctor.OK, Message: "using http://x"},
		{Name: "suite", Status: doctor.Warn, Message: "no git remote"},
		{Name: "auth", Status: doctor.Fail, Message: "rejected", Fix: "sel auth login --setup"},
		{Name: "socket", Status: doctor.Skip, Message: "added in a later release"},
	}}
	var out bytes.Buffer

	if err := writeChecklist(&out, report); err != nil {
		t.Fatal(err)
	}

	want := "✓ config       using http://x\n" +
		"⚠ suite        no git remote\n" +
		"✗ auth         rejected\n" +
		"               fix: sel auth login --setup\n" +
		"– socket       added in a later release\n"
	if out.String() != want {
		t.Errorf("checklist:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestDoctorTimeoutMustBeLongerThanZero(t *testing.T) {
	code, stdout, _ := execute("doctor", "--timeout", "0s", "--json")

	if code != contract.ExitUsage || !strings.Contains(stdout, `"error":"usage_error"`) || !strings.Contains(stdout, "--timeout must be longer than zero") {
		t.Errorf("exit %d, stdout %q", code, stdout)
	}
}

func TestHelpJSONListsDoctorWithItsExitCodes(t *testing.T) {
	_, stdout, _ := execute("help", "doctor", "--json")

	var out struct {
		Command struct {
			Path      string `json:"path"`
			ExitCodes []int  `json:"exit_codes"`
			Mutates   bool   `json:"mutates"`
			Flags     []struct {
				Name    string `json:"name"`
				Default string `json:"default"`
			} `json:"flags"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	flags := map[string]string{}
	for _, f := range out.Command.Flags {
		flags[f.Name] = f.Default
	}
	if out.Command.Path != "doctor" || len(out.Command.ExitCodes) != 6 || out.Command.Mutates || flags["timeout"] != "30s" {
		t.Errorf("help doctor = %+v, flags %v", out.Command, flags)
	}
	for _, name := range []string{"org", "hostname", "json"} {
		if _, ok := flags[name]; !ok {
			t.Errorf("doctor lacks --%s", name)
		}
	}
}
