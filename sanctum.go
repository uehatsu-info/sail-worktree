package main

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

// Laravel Sanctum adds APP_URL's host:port to its stateful domains only when SANCTUM_STATEFUL_DOMAINS is not set.
// A project that sets it lists its own URL there, so after up moves APP_URL to another port the worktree's URL is
// missing and Sanctum's cookie authentication fails. up appends the new entry, following Sanctum's own matching rule.

const sanctumKey = "SANCTUM_STATEFUL_DOMAINS"

// sanctumCurrentHost is Sanctum's placeholder for the current request's host, which it replaces with getHttpHost():
// the Host header, with the port unless it is the scheme's default, i.e. the same text as the entry.
const sanctumCurrentHost = "__SANCTUM_CURRENT_REQUEST_HOST__"

var (
	// Hosts and values are written to .env, which Sail sources as shell code, and a "," or "*" in an entry would widen
	// the stateful list, so both are limited to characters that are inert in a shell assignment.
	statefulHostRe  = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_.-]*[a-z0-9_])?$`)
	statefulValueRe = regexp.MustCompile(`^[A-Za-z0-9_.:,*\-\[\] ]*$`)
)

// statefulDomain returns the entry Sanctum needs for u: the lower-case host, plus ":port" unless the port is the
// scheme's default (browsers omit it from Referer and Origin). ok is false when the host is not plainly safe to write.
func statefulDomain(u *url.URL) (entry string, ok bool) {
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		a, err := netip.ParseAddr(host)
		// A zone cannot be in a browser's Origin, and browsers write an IPv4-mapped address in another form.
		if err != nil || a.Zone() != "" || a.Is4In6() {
			return "", false
		}
		host = "[" + a.String() + "]"
	} else if !statefulHostRe.MatchString(host) {
		return "", false
	}
	port := u.Port()
	for _, c := range port {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	if port == "" || u.Scheme == "http" && port == "80" || u.Scheme == "https" && port == "443" {
		return host, true
	}
	return host + ":" + port, true
}

// strIs is Laravel's Str::is: "*" matches any sequence of characters, everything else is literal, and the whole
// value must match (case-sensitive).
func strIs(pattern, value string) bool {
	re := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
	return regexp.MustCompile(re).MatchString(value)
}

// sanctumUnquote trims spaces and tabs and removes one pair of matching quotes around the value. Spaces inside the
// quotes are kept, as phpdotenv keeps them (Sanctum compares the placeholder before trimming). quote is the removed
// quote ("" when there was none).
func sanctumUnquote(raw string) (value, quote string) {
	v := strings.Trim(raw, " \t")
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] && !strings.ContainsAny(v[1:len(v)-1], `"'`) {
		return v[1 : len(v)-1], v[:1]
	}
	return v, ""
}

// statefulDisabled reports whether the value turns Sanctum's stateful domains off: Laravel's env() maps these words
// to null, false or "", true becomes "1", and Sanctum drops empty entries.
func statefulDisabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "null", "(null)", "false", "(false)", "true", "(true)", "empty", "(empty)":
		return true
	}
	return strings.Trim(v, ", ") == ""
}

// addStatefulDomain appends u's entry to SANCTUM_STATEFUL_DOMAINS when the key holds a plain list that no entry
// matches yet. added is the appended entry; warning is set when the value or the URL cannot be handled safely. Both
// empty means no change and nothing to report.
func addStatefulDomain(env *envFile, u *url.URL) (added, warning string) {
	raw, ok := env.Raw(sanctumKey)
	if !ok {
		return "", "" // Sanctum's default already follows APP_URL
	}
	value, quote := sanctumUnquote(raw)
	if statefulDisabled(value) {
		return "", ""
	}
	entry, ok := statefulDomain(u)
	if !ok {
		return "", fmt.Sprintf("the host of APP_URL %+q has characters that up does not write to %s (or is an IPv6 zone or IPv4-mapped address); add it yourself if you need it", u.String(), sanctumKey)
	}
	if !statefulValueRe.MatchString(value) || quote == "" && strings.Contains(value, " ") {
		return "", fmt.Sprintf("%s %+q is not a plain list, so it is left unchanged; add %s to it yourself", sanctumKey, raw, entry)
	}
	for _, e := range strings.Split(value, ",") {
		if e == sanctumCurrentHost {
			e = entry
		}
		if e = strings.TrimSpace(e); e != "" && strIs(e+"/*", entry+"/") {
			return "", ""
		}
	}
	if t := strings.TrimRight(value, " "); t != "" && !strings.HasSuffix(t, ",") {
		value += ","
	}
	env.Set(sanctumKey, quote+value+entry+quote)
	return entry, ""
}
