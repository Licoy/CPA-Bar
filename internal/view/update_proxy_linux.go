//go:build linux

package view

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

const linuxProxyCommandTimeout = 2 * time.Second

func systemProxyForURL(target *url.URL) (*url.URL, error) {
	desktop := strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP") + ":" + os.Getenv("DESKTOP_SESSION"))
	switch {
	case strings.Contains(desktop, "kde"), strings.Contains(desktop, "plasma"):
		settings, err := readKDEProxySettings()
		if err != nil {
			return nil, err
		}
		return settings.proxyForURL(target)
	case strings.Contains(desktop, "gnome"):
		settings, err := readGNOMEProxySettings()
		if err != nil {
			return nil, err
		}
		return settings.proxyForURL(target)
	default:
		return nil, nil
	}
}

func readGNOMEProxySettings() (linuxProxySettings, error) {
	mode, err := gsettingsString("org.gnome.system.proxy", "mode")
	if err != nil || mode == "none" {
		return linuxProxySettings{mode: mode}, err
	}
	if mode == "auto" {
		return linuxProxySettings{mode: mode}, nil
	}
	if mode != "manual" {
		return linuxProxySettings{}, fmt.Errorf("system proxy: unsupported GNOME proxy mode %q", mode)
	}
	bypassRaw, err := linuxProxyCommand("gsettings", "get", "org.gnome.system.proxy", "ignore-hosts")
	if err != nil {
		return linuxProxySettings{}, err
	}
	bypass, err := parseGSettingsStrings(bypassRaw)
	if err != nil {
		return linuxProxySettings{}, fmt.Errorf("system proxy: parse GNOME ignore-hosts: %w", err)
	}
	httpProxy, err := readGNOMEProxyEndpoint("http", "http")
	if err != nil {
		return linuxProxySettings{}, err
	}
	httpsProxy, err := readGNOMEProxyEndpoint("https", "http")
	if err != nil {
		return linuxProxySettings{}, err
	}
	socksProxy, err := readGNOMEProxyEndpoint("socks", "socks5")
	if err != nil {
		return linuxProxySettings{}, err
	}
	return newGNOMEProxySettings(httpProxy, httpsProxy, socksProxy, bypass), nil
}

func readGNOMEProxyEndpoint(child, scheme string) (string, error) {
	schema := "org.gnome.system.proxy." + child
	host, err := gsettingsString(schema, "host")
	if err != nil {
		return "", err
	}
	portRaw, err := linuxProxyCommand("gsettings", "get", schema, "port")
	if err != nil {
		return "", err
	}
	return gnomeProxyEndpoint(host, portRaw, child, scheme)
}

func gsettingsString(schema, key string) (string, error) {
	value, err := linuxProxyCommand("gsettings", "get", schema, key)
	if err != nil {
		return "", err
	}
	parsed, err := parseGSettingsString(value)
	if err != nil {
		return "", fmt.Errorf("system proxy: parse GNOME %s: %w", key, err)
	}
	return parsed, nil
}

func readKDEProxySettings() (linuxProxySettings, error) {
	command, err := kdeReadConfigCommand()
	if err != nil {
		return linuxProxySettings{}, err
	}
	proxyType, err := kreadconfig(command, "ProxyType", "")
	if err != nil || proxyType == "" || proxyType == "0" {
		return linuxProxySettings{}, err
	}
	if proxyType == "2" || proxyType == "3" {
		return linuxProxySettings{mode: "auto"}, nil
	}
	if proxyType == "4" {
		return readKDEEnvironmentProxySettings(command)
	}
	if proxyType != "1" {
		return linuxProxySettings{}, fmt.Errorf("system proxy: unsupported KDE ProxyType %q", proxyType)
	}
	httpProxy, err := readKDEProxyEndpoint(command, "httpProxy", "http")
	if err != nil {
		return linuxProxySettings{}, err
	}
	httpsProxy, err := readKDEProxyEndpoint(command, "httpsProxy", "http")
	if err != nil {
		return linuxProxySettings{}, err
	}
	socksProxy, err := readKDEProxyEndpoint(command, "socksProxy", "socks5")
	if err != nil {
		return linuxProxySettings{}, err
	}
	bypassRaw, err := kreadconfig(command, "NoProxyFor", "")
	if err != nil {
		return linuxProxySettings{}, err
	}
	reverseRaw, err := kreadconfig(command, "ReversedException", "false")
	if err != nil {
		return linuxProxySettings{}, err
	}
	bypass := strings.FieldsFunc(bypassRaw, func(r rune) bool { return r == ',' || r == ';' })
	return linuxProxySettings{mode: "manual", httpProxy: httpProxy, httpsProxy: httpsProxy, socksProxy: socksProxy,
		bypass: bypass, reverseBypass: strings.EqualFold(reverseRaw, "true") || reverseRaw == "1"}, nil
}

func readKDEEnvironmentProxySettings(command string) (linuxProxySettings, error) {
	httpProxy, err := readKDEEnvironmentProxyEndpoint(command, "httpProxy", "http")
	if err != nil {
		return linuxProxySettings{}, err
	}
	httpsProxy, err := readKDEEnvironmentProxyEndpoint(command, "httpsProxy", "http")
	if err != nil {
		return linuxProxySettings{}, err
	}
	socksProxy, err := readKDEEnvironmentProxyEndpoint(command, "socksProxy", "socks5")
	if err != nil {
		return linuxProxySettings{}, err
	}
	bypassName, err := kreadconfig(command, "NoProxyFor", "")
	if err != nil {
		return linuxProxySettings{}, err
	}
	bypass := strings.FieldsFunc(os.Getenv(strings.TrimPrefix(bypassName, "$")), func(r rune) bool { return r == ',' || r == ';' })
	reverseRaw, err := kreadconfig(command, "ReversedException", "false")
	if err != nil {
		return linuxProxySettings{}, err
	}
	return linuxProxySettings{mode: "manual", httpProxy: httpProxy, httpsProxy: httpsProxy, socksProxy: socksProxy,
		bypass: bypass, reverseBypass: strings.EqualFold(reverseRaw, "true") || reverseRaw == "1"}, nil
}

func readKDEEnvironmentProxyEndpoint(command, key, scheme string) (string, error) {
	name, err := kreadconfig(command, key, "")
	if err != nil || name == "" {
		return "", err
	}
	proxy, err := linuxProxyURL(os.Getenv(strings.TrimPrefix(name, "$")), scheme)
	if err != nil {
		return "", fmt.Errorf("system proxy: invalid KDE environment variable %s: %w", name, err)
	}
	return proxy, nil
}

func readKDEProxyEndpoint(command, key, scheme string) (string, error) {
	raw, err := kreadconfig(command, key, "")
	if err != nil {
		return "", err
	}
	proxy, err := linuxProxyURL(raw, scheme)
	if err != nil {
		return "", fmt.Errorf("system proxy: invalid KDE %s: %w", key, err)
	}
	return proxy, nil
}

func kdeReadConfigCommand() (string, error) {
	for _, name := range []string{"kreadconfig6", "kreadconfig5"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("system proxy: KDE requires kreadconfig6 or kreadconfig5")
}

func kreadconfig(command, key, fallback string) (string, error) {
	return linuxProxyCommand(command, "--file", "kioslaverc", "--group", "Proxy Settings", "--key", key, "--default", fallback)
}

func linuxProxyCommand(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), linuxProxyCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if ctx.Err() != nil {
		return "", fmt.Errorf("system proxy: %s timed out: %w", name, ctx.Err())
	}
	if err != nil {
		return "", fmt.Errorf("system proxy: %s failed: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}
