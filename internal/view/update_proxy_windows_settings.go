package view

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

type windowsProxySettings struct {
	autoDetect    bool
	autoConfigURL string
	proxy         string
	bypass        string
}

func proxyFromWindowsSettings(target *url.URL, settings windowsProxySettings) (*url.URL, error) {
	if target == nil || target.Hostname() == "" {
		return nil, errors.New("system proxy: target URL is missing a host")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, fmt.Errorf("system proxy: unsupported target scheme %q", target.Scheme)
	}
	if windowsProxyBypass(target, settings.bypass) {
		return nil, nil
	}
	address, kind := windowsProxyAddress(target.Scheme, settings.proxy)
	if address == "" {
		return nil, nil
	}
	return parseWindowsProxyURL(address, kind)
}

func windowsProxyAddress(scheme, value string) (string, string) {
	proxies := make(map[string]string)
	for _, entry := range splitWindowsProxyList(value) {
		kind, address, found := strings.Cut(entry, "=")
		if !found {
			if proxies[""] == "" {
				proxies[""] = entry
			}
			continue
		}
		kind = strings.ToLower(strings.TrimSpace(kind))
		if proxies[kind] == "" {
			proxies[kind] = strings.TrimSpace(address)
		}
	}
	if address := proxies[strings.ToLower(scheme)]; address != "" {
		return address, "http"
	}
	if address := proxies[""]; address != "" {
		kind := "http"
		if strings.HasPrefix(strings.ToLower(address), "socks://") || strings.HasPrefix(strings.ToLower(address), "socks5://") || strings.HasPrefix(strings.ToLower(address), "socks5h://") {
			kind = "socks5"
		}
		return address, kind
	}
	return proxies["socks"], "socks5"
}

func parseWindowsProxyURL(address, kind string) (*url.URL, error) {
	address = strings.TrimSpace(address)
	if !strings.Contains(address, "://") {
		address = kind + "://" + address
	}
	proxy, err := url.Parse(address)
	if err != nil || proxy.Hostname() == "" || proxy.Path != "" || proxy.RawQuery != "" || proxy.Fragment != "" {
		return nil, fmt.Errorf("system proxy: invalid Windows proxy address %q", address)
	}
	switch kind {
	case "http":
		if proxy.Scheme != "http" && proxy.Scheme != "https" {
			return nil, fmt.Errorf("system proxy: unsupported Windows HTTP proxy %q", address)
		}
		proxy.Scheme = "http"
	case "socks5":
		if proxy.Scheme != "socks" && proxy.Scheme != "socks5" && proxy.Scheme != "socks5h" {
			return nil, fmt.Errorf("system proxy: unsupported Windows SOCKS proxy %q", address)
		}
		proxy.Scheme = "socks5"
	}
	if port := proxy.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, fmt.Errorf("system proxy: invalid Windows proxy port %q", port)
		}
	}
	return proxy, nil
}

func windowsProxyBypass(target *url.URL, value string) bool {
	host := strings.ToLower(target.Hostname())
	port := target.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[target.Scheme]
	}
	for _, entry := range splitWindowsProxyList(value) {
		entry = strings.ToLower(entry)
		pattern, patternPort := splitProxyHostPort(entry)
		if patternPort != "" && patternPort != port {
			continue
		}
		if pattern == "<local>" && net.ParseIP(host) == nil && !strings.Contains(host, ".") {
			return true
		}
		if wildcardWindowsProxyMatch(pattern, host) {
			return true
		}
	}
	return false
}

func splitWindowsProxyList(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ';' || r == ',' || unicode.IsSpace(r)
	})
}

func wildcardWindowsProxyMatch(pattern, value string) bool {
	patternIndex, valueIndex, star, retry := 0, 0, -1, 0
	for valueIndex < len(value) {
		if patternIndex < len(pattern) && pattern[patternIndex] == value[valueIndex] {
			patternIndex++
			valueIndex++
		} else if patternIndex < len(pattern) && pattern[patternIndex] == '*' {
			star, retry = patternIndex, valueIndex
			patternIndex++
		} else if star >= 0 {
			patternIndex = star + 1
			retry++
			valueIndex = retry
		} else {
			return false
		}
	}
	return strings.Trim(pattern[patternIndex:], "*") == ""
}
