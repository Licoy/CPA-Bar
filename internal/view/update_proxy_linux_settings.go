package view

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

type linuxProxySettings struct {
	mode                              string
	httpProxy, httpsProxy, socksProxy string
	bypass                            []string
	reverseBypass                     bool
}

// The four parameters map directly to the small, fixed GNOME proxy setting set.
func newGNOMEProxySettings(httpProxy, httpsProxy, socksProxy string, bypass []string) linuxProxySettings {
	if httpsProxy == "" {
		httpsProxy = httpProxy
	}
	return linuxProxySettings{mode: "manual", httpProxy: httpProxy, httpsProxy: httpsProxy, socksProxy: socksProxy, bypass: bypass}
}

// The four parameters map directly to one GNOME proxy endpoint's setting values.
func gnomeProxyEndpoint(host, portRaw, child, scheme string) (string, error) {
	port, err := strconv.Atoi(strings.TrimSpace(portRaw))
	if err != nil {
		return "", fmt.Errorf("system proxy: invalid GNOME %s proxy %q:%q", child, host, strings.TrimSpace(portRaw))
	}
	if host == "" || port == 0 {
		return "", nil
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("system proxy: invalid GNOME %s proxy %q:%q", child, host, strings.TrimSpace(portRaw))
	}
	return linuxProxyURL(scheme+"://"+net.JoinHostPort(host, strconv.Itoa(port)), scheme)
}

func (s linuxProxySettings) proxyForURL(target *url.URL) (*url.URL, error) {
	switch s.mode {
	case "", "none":
		return nil, nil
	case "auto":
		return nil, errors.New("system proxy: automatic proxy configuration (PAC) is not supported on Linux")
	case "manual":
	default:
		return nil, fmt.Errorf("system proxy: unsupported Linux proxy mode %q", s.mode)
	}
	matched := linuxProxyBypass(target, s.bypass)
	if matched != s.reverseBypass {
		return nil, nil
	}
	raw := map[string]string{"http": s.httpProxy, "https": s.httpsProxy}[target.Scheme]
	if raw == "" {
		raw = s.socksProxy
	}
	if raw == "" {
		return nil, nil
	}
	proxy, err := url.Parse(raw)
	if err != nil || proxy.Host == "" {
		return nil, fmt.Errorf("system proxy: invalid proxy URL %q", raw)
	}
	return proxy, nil
}

func linuxProxyURL(raw, defaultScheme string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parts := strings.Fields(raw)
	if len(parts) == 2 {
		port, err := strconv.Atoi(parts[1])
		if err != nil || port < 1 || port > 65535 {
			return "", fmt.Errorf("invalid proxy port %q", parts[1])
		}
		host := strings.Trim(parts[0], "[]")
		if strings.Contains(parts[0], "://") {
			endpoint, parseErr := url.Parse(parts[0])
			if parseErr != nil || endpoint.Hostname() == "" {
				return "", fmt.Errorf("invalid proxy address %q", raw)
			}
			defaultScheme, host = endpoint.Scheme, endpoint.Hostname()
		}
		raw = net.JoinHostPort(host, parts[1])
	} else if len(parts) != 1 {
		return "", fmt.Errorf("invalid proxy address %q", raw)
	}
	if !strings.Contains(raw, "://") {
		raw = defaultScheme + "://" + raw
	}
	proxy, err := url.Parse(raw)
	if err != nil || proxy.Hostname() == "" || proxy.Port() == "" {
		return "", fmt.Errorf("invalid proxy address %q", raw)
	}
	port, err := strconv.Atoi(proxy.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid proxy port %q", proxy.Port())
	}
	if proxy.Scheme == "socks" {
		proxy.Scheme = "socks5"
	}
	switch proxy.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", fmt.Errorf("unsupported proxy scheme %q", proxy.Scheme)
	}
	return proxy.String(), nil
}

func linuxProxyBypass(target *url.URL, entries []string) bool {
	host := strings.ToLower(target.Hostname())
	port := target.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[target.Scheme]
	}
	ip, ipErr := netip.ParseAddr(host)
	for _, raw := range entries {
		entry := strings.ToLower(strings.TrimSpace(raw))
		if entry == "" {
			continue
		}
		if entry == "*" || entry == "<local>" && !strings.Contains(host, ".") {
			return true
		}
		if prefix, err := netip.ParsePrefix(entry); err == nil && ipErr == nil && prefix.Contains(ip) {
			return true
		}
		entryHost, entryPort := splitProxyHostPort(entry)
		if entryPort != "" && entryPort != port {
			continue
		}
		if parsed, err := netip.ParseAddr(entryHost); err == nil {
			if ipErr == nil && parsed == ip {
				return true
			}
			continue
		}
		exact := !strings.HasPrefix(entryHost, ".") && !strings.HasPrefix(entryHost, "*.")
		entryHost = strings.TrimPrefix(entryHost, "*")
		if !strings.HasPrefix(entryHost, ".") {
			entryHost = "." + entryHost
		}
		if strings.HasSuffix(host, entryHost) || exact && host == entryHost[1:] {
			return true
		}
	}
	return false
}

func parseGSettingsStrings(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimSpace(strings.TrimPrefix(value, "@as"))
	if len(value) < 2 || value[0] != '[' || value[len(value)-1] != ']' {
		return nil, fmt.Errorf("invalid GSettings string list %q", value)
	}
	value = strings.TrimSpace(value[1 : len(value)-1])
	if value == "" {
		return nil, nil
	}
	var values []string
	for len(value) > 0 {
		text, rest, err := parseGSettingsQuotedString(value)
		if err != nil {
			return nil, err
		}
		values = append(values, text)
		value = strings.TrimSpace(rest)
		if value == "" {
			break
		}
		if value[0] != ',' {
			return nil, fmt.Errorf("invalid GSettings string list %q", value)
		}
		value = strings.TrimSpace(value[1:])
		if value == "" {
			return nil, errors.New("invalid trailing comma in GSettings string list")
		}
	}
	return values, nil
}

func parseGSettingsString(value string) (string, error) {
	parsed, rest, err := parseGSettingsQuotedString(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(rest) != "" {
		return "", fmt.Errorf("expected one GSettings string, got %q", value)
	}
	return parsed, nil
}

func parseGSettingsQuotedString(value string) (string, string, error) {
	if len(value) < 2 || value[0] != '\'' && value[0] != '"' {
		return "", "", fmt.Errorf("expected quoted GSettings string, got %q", value)
	}
	quote := value[0]
	var text strings.Builder
	for i := 1; i < len(value); i++ {
		if value[i] == '\\' {
			if i+1 == len(value) {
				break
			}
			i++
			text.WriteByte(value[i])
			continue
		}
		if value[i] == quote {
			return text.String(), value[i+1:], nil
		}
		text.WriteByte(value[i])
	}
	return "", "", fmt.Errorf("unterminated GSettings string %q", value)
}
