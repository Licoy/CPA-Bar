package view

import (
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/licoy/cpa-bar/internal/store"
)

// Privacy mode hides the middle of what identifies a server or an account
// (the CPA addresses, the account emails and names) wherever the interface
// shows them, for screenshots and screen sharing. It changes only what is
// shown: searching, copying an address and every request use the real
// values.

var (
	privacy atomic.Bool
	// privacyHosts are the hosts of the configured instances, hidden in
	// free text such as error messages.
	privacyHosts atomic.Value // []string
)

// SetPrivacy turns privacy mode on or off.
func SetPrivacy(on bool) { privacy.Store(on) }

// Private reports whether privacy mode is on.
func Private() bool { return privacy.Load() }

// setPrivacyHosts records the hosts of the instances to hide in text.
func setPrivacyHosts(instances []store.InstanceProfile) {
	var hosts []string
	for _, p := range instances {
		if h := urlHost(p.BaseURL); h != "" {
			hosts = append(hosts, h)
		}
	}
	privacyHosts.Store(hosts)
}

func urlHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// maskMiddle keeps the ends of s and puts *** in between: "example" →
// "e***e". Longer values keep more, up to four characters each side.
func maskMiddle(s string) string {
	r := []rune(s)
	n := len(r)
	switch {
	case n == 0:
		return ""
	case n <= 2:
		return string(r[:1]) + "***"
	case n <= 5:
		return string(r[:1]) + "***" + string(r[n-1:])
	}
	k := min(4, max(1, n/4))
	return string(r[:k]) + "***" + string(r[n-k:])
}

// maskEmail keeps only the first letter of the name and of the domain, and
// the top-level domain: "example@qq.com" → "e****@q***.com".
func maskEmail(s string) string {
	at := strings.LastIndex(s, "@")
	if at <= 0 || at == len(s)-1 {
		return maskMiddle(s)
	}
	local, domain := s[:at], s[at+1:]
	tld := ""
	if dot := strings.LastIndex(domain, "."); dot > 0 {
		domain, tld = domain[:dot], domain[dot:]
	}
	return firstRune(local) + "****@" + firstRune(domain) + "***" + tld
}

// maskHost keeps the first letter of each name of a host and its
// top-level domain: "example.com" → "e****.com", "example.cdn.com" →
// "e****.c**.com". An IP address keeps the first digit: "1**.*.*.*".
func maskHost(h string) string {
	if h == "" {
		return ""
	}
	if ip := net.ParseIP(h); ip != nil {
		if ip.To4() == nil {
			return firstRune(h) + "****"
		}
		return firstRune(h) + "**.*.*.*"
	}
	labels := strings.Split(h, ".")
	if len(labels) == 1 {
		return firstRune(h) + "****"
	}
	n := len(labels)
	for i, l := range labels[:n-1] {
		stars := "**"
		if i == 0 {
			stars = "****"
		}
		labels[i] = firstRune(l) + stars
	}
	return strings.Join(labels, ".")
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

// PrivName is an account's email or name as privacy mode shows it.
func PrivName(s string) string {
	if !Private() || s == "" {
		return s
	}
	if strings.Contains(s, "@") {
		return maskEmail(s)
	}
	return maskMiddle(s)
}

// PrivURL is an address as privacy mode shows it: the host is masked
// (maskHost); the scheme, port and path stay.
func PrivURL(raw string) string {
	if !Private() || raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return maskMiddle(raw)
	}
	host := maskHost(u.Hostname())
	if p := u.Port(); p != "" {
		host = net.JoinHostPort(host, p)
	}
	// Rebuild by hand: url.URL would escape the asterisks.
	out := u.Scheme + "://" + host + u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

var emailRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// PrivText hides, in free text such as an error or a status message, the
// emails and the hosts of the instances.
func PrivText(s string) string {
	if !Private() || s == "" {
		return s
	}
	s = emailRE.ReplaceAllStringFunc(s, maskEmail)
	hosts, _ := privacyHosts.Load().([]string)
	for _, h := range hosts {
		if h != "" {
			s = strings.ReplaceAll(s, h, maskHost(h))
		}
	}
	return s
}
