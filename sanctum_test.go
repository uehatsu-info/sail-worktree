package main

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrIs(t *testing.T) {
	for _, c := range []struct {
		pattern, value string
		want           bool
	}{
		{"h:8082/*", "h:8082/", true},
		{"h:8082/*", "h:8082/a/b", true},
		{"*/*", "h.test:8082/", true},
		{"*.nip.io:*/*", "store.127.0.0.1.nip.io:8082/", true},
		{"h/*", "h:8082/", false},
		{"h.test/*", "hxtest/", false},
		{"[::1]:8082/*", "[::1]:8082/", true},
		{"H/*", "h/", false},
		{"h/*", "xh/", false},
	} {
		if got := strIs(c.pattern, c.value); got != c.want {
			t.Errorf("strIs(%q, %q) = %v", c.pattern, c.value, got)
		}
	}
}

func TestStatefulDomain(t *testing.T) {
	for in, want := range map[string]string{
		"http://h:8082":                  "h:8082",
		"http://H.Test:8082/x":           "h.test:8082",
		"http://h:80":                    "h",
		"https://h:443":                  "h",
		"https://h:80":                   "h:80",
		"http://my_app.test:8082":        "my_app.test:8082",
		"http://[::1]:8082":              "[::1]:8082",
		"http://[0:0::1]:8082":           "[::1]:8082",
		"http://a,b.test:8082":           "",
		"http://*:8082":                  "",
		"http://a$(id).test:8082":        "",
		"http://a'b:8082":                "",
		"http://[fe80::1%25en0]:8082":    "",
		"http://[::ffff:127.0.0.1]:8082": "",
	} {
		u, err := url.Parse(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		got, ok := statefulDomain(u)
		if want == "" {
			if ok {
				t.Errorf("%s: accepted as %q", in, got)
			}
		} else if !ok || got != want {
			t.Errorf("%s: %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestAddStatefulDomain(t *testing.T) {
	u, _ := url.Parse("http://store.test:8082")
	const ph = sanctumCurrentHost
	cases := []struct {
		name, lines, want string // want: the expected .env text; "" means unchanged
		warn              bool
	}{
		{"absent", "A=1", "", false},
		{"empty", "S=", "", false},
		{"empty quotes", `S=""`, "", false},
		{"null", "S=null", "", false},
		{"(null)", "S=(null)", "", false},
		{"False", "S=False", "", false},
		{"true", "S=true", "", false},
		{"(true)", "S=(true)", "", false},
		{"(empty)", "S=(empty)", "", false},
		{"only commas", "S= , ,", "", false},
		{"plain list", "S=store.test,localhost:5173", "S=store.test,localhost:5173,store.test:8082", false},
		{"already there", "S=localhost,store.test:8082", "", false},
		{"spaces around", `S="localhost, store.test:8082 "`, "", false},
		{"wildcard host", "S=*.test:*", "", false},
		{"wildcard all", "S=*", "", false},
		{"placeholder", "S=" + ph, "", false},
		{"placeholder after a space is literal", `S="a, ` + ph + `"`, `S="a, ` + ph + `,store.test:8082"`, false},
		{"spaces inside quotes are kept", `S=" a , b "`, `S=" a , b ,store.test:8082"`, false},
		{"double quotes", `S="a,b"`, `S="a,b,store.test:8082"`, false},
		{"single quotes", `S='a,b'`, `S='a,b,store.test:8082'`, false},
		{"trailing comma", "S=a,", "S=a,store.test:8082", false},
		{"underscore", "S=my_app.test", "S=my_app.test,store.test:8082", false},
		{"export", "export S=a", "S=a,store.test:8082", false},
		{"duplicate line", "S=a\nS=${X}", "S=a,store.test:8082", false},
		{"duplicate line, already there", "S=store.test:8082\nS=${X}", "", false},
		{"lone quote", `S="`, "", true},
		{"variable", "S=a,${APP_URL}", "", true},
		{"dollar", "S=a$b", "", true},
		{"comment", "S=a,b # c", "", true},
		{"quoted comment", `S="a,b" # c`, "", true},
		{"backslash", `S=a\b`, "", true},
		{"inner quote", `S="a"b"`, "", true},
		{"unquoted space", "S=a, b", "", true},
		{"paren", "S=a(b)", "", true},
		{"carriage return inside", "S=a\rb", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines := strings.Split(strings.ReplaceAll(c.lines, "S=", sanctumKey+"="), "\n")
			e := &envFile{lines: append([]string{}, lines...)}
			added, warning := addStatefulDomain(e, u)
			want := strings.ReplaceAll(c.want, "S=", sanctumKey+"=")
			if c.want == "" {
				if added != "" || strings.Join(e.lines, "\n") != strings.Join(lines, "\n") {
					t.Errorf("changed: added %q, lines %q", added, e.lines)
				}
			} else if added != "store.test:8082" || strings.Join(e.lines, "\n") != want {
				t.Errorf("added %q, lines %q; want %q", added, e.lines, want)
			}
			if (warning != "") != c.warn {
				t.Errorf("warning = %q", warning)
			}
			if c.warn && !strings.Contains(warning, "store.test:8082") {
				t.Errorf("the warning does not say what to add: %q", warning)
			}
		})
	}
}

// The placeholder stands for the request's host with its port, so it already covers the worktree's URL; only the
// exact element is the placeholder (Sanctum compares before trimming).
func TestAddStatefulDomainPlaceholder(t *testing.T) {
	for _, c := range []struct {
		url, value string
		added      bool
	}{
		{"http://store.test:8082", sanctumCurrentHost, false},
		{"http://store.test", sanctumCurrentHost, false},
		{"http://[::1]:8082", sanctumCurrentHost, false},
		{"http://store.test:8082", "a," + sanctumCurrentHost, false},
		{"http://store.test:8082", `"a, ` + sanctumCurrentHost + `"`, true},
	} {
		u, _ := url.Parse(c.url)
		e := &envFile{lines: []string{sanctumKey + "=" + c.value}}
		if added, warning := addStatefulDomain(e, u); (added != "") != c.added || warning != "" {
			t.Errorf("%s with %q: added %q, warning %q", c.url, c.value, added, warning)
		}
	}
}

func TestGetKeepsTrimmingMismatchedQuotes(t *testing.T) {
	e := &envFile{lines: []string{`A="x'`}}
	if v, _ := e.Get("A"); v != "x" {
		t.Errorf("Get = %q", v)
	}
}

func captureStdout(t *testing.T) *strings.Builder {
	t.Helper()
	var b strings.Builder
	old := stdout
	stdout = &b
	t.Cleanup(func() { stdout = old })
	return &b
}

func upWithMainEnv(t *testing.T, mainEnv string) (wt string, out, errOut *strings.Builder) {
	t.Helper()
	main, wt := setupWorktreeRepo(t)
	writeFile(t, filepath.Join(main, ".env"), mainEnv)
	writeFakeSail(t, wt)
	captureRunner(t)
	out, errOut = captureStdout(t), captureStderr(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	return wt, out, errOut
}

func TestUpAddsStatefulDomain(t *testing.T) {
	wt, out, errOut := upWithMainEnv(t, "APP_URL=http://store.127.0.0.1.nip.io\r\n"+sanctumKey+"=store.127.0.0.1.nip.io,localhost:5173\r\n")
	e, err := readEnv(filepath.Join(wt, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := e.Get("APP_PORT")
	entry := "store.127.0.0.1.nip.io:" + port
	if v, _ := e.Get(sanctumKey); v != "store.127.0.0.1.nip.io,localhost:5173,"+entry {
		t.Errorf("%s=%q", sanctumKey, v)
	}
	if !strings.Contains(out.String(), sanctumKey+": added "+entry) || errOut.Len() != 0 {
		t.Errorf("stdout %q, stderr %q", out.String(), errOut.String())
	}
	// A second up adds nothing.
	out.Reset()
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	e, _ = readEnv(filepath.Join(wt, ".env"))
	if v, _ := e.Get(sanctumKey); strings.Count(v, entry) != 1 || strings.Contains(out.String(), "added") {
		t.Errorf("second up: %s=%q, stdout %q", sanctumKey, v, out.String())
	}
}

func TestUpLeavesUnsafeStatefulDomains(t *testing.T) {
	wt, out, errOut := upWithMainEnv(t, "APP_URL=http://store.test\n"+sanctumKey+"=${APP_HOST},localhost\n")
	e, _ := readEnv(filepath.Join(wt, ".env"))
	port, _ := e.Get("APP_PORT")
	if v, _ := e.Raw(sanctumKey); v != "${APP_HOST},localhost" {
		t.Errorf("%s=%q", sanctumKey, v)
	}
	if !strings.Contains(errOut.String(), "warning: "+sanctumKey) || !strings.Contains(errOut.String(), "add store.test:"+port) || strings.Contains(out.String(), "added") {
		t.Errorf("stdout %q, stderr %q", out.String(), errOut.String())
	}
}

func TestUpWarnsForUnsafeAppURLHost(t *testing.T) {
	wt, _, errOut := upWithMainEnv(t, "APP_URL='http://a$(id).test'\n"+sanctumKey+"=localhost\n")
	e, _ := readEnv(filepath.Join(wt, ".env"))
	if v, _ := e.Get(sanctumKey); v != "localhost" {
		t.Errorf("%s=%q", sanctumKey, v)
	}
	if !strings.Contains(errOut.String(), "warning: the host of APP_URL") {
		t.Errorf("stderr %q", errOut.String())
	}
}

func TestUpWithoutStatefulDomainsAddsNothing(t *testing.T) {
	wt, out, _ := upWithMainEnv(t, "APP_URL=http://store.test\n")
	e, _ := readEnv(filepath.Join(wt, ".env"))
	if _, ok := e.Get(sanctumKey); ok || strings.Contains(out.String(), sanctumKey) {
		t.Errorf("%s was added", sanctumKey)
	}
}

func TestUpAddsIPv6StatefulDomain(t *testing.T) {
	wt, _, _ := upWithMainEnv(t, "APP_URL=http://[::1]\n"+sanctumKey+"=localhost\n")
	e, _ := readEnv(filepath.Join(wt, ".env"))
	port, _ := e.Get("APP_PORT")
	if v, _ := e.Get(sanctumKey); v != fmt.Sprintf("localhost,[::1]:%s", port) {
		t.Errorf("%s=%q", sanctumKey, v)
	}
}
