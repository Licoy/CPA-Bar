package cpa

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ManagementPath is the only management API version CPA-Bar speaks.
const ManagementPath = "/v8/management"

// NormalizeManagementURL accepts the root address of a CPA server, with or
// without a reverse proxy prefix, or the full v8 management address, and
// returns the v8 management base URL without a trailing slash.
//
//	http://127.0.0.1:8317                  -> http://127.0.0.1:8317/v8/management
//	https://host/cpa/                      -> https://host/cpa/v8/management
//	https://host/cpa/v8/management/config  -> https://host/cpa/v8/management
//
// A scheme-less address is treated as http. Other management versions are
// rejected so that a v0 address is never silently rewritten.
func NormalizeManagementURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("address is empty")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid address: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("address has no host")
	}
	if u.User != nil {
		return "", errors.New("credentials in the address are not supported; use the management key")
	}
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""

	path := strings.TrimRight(u.EscapedPath(), "/")
	if i := strings.Index(path, ManagementPath); i >= 0 && (len(path) == i+len(ManagementPath) || path[i+len(ManagementPath)] == '/') {
		path = path[:i]
	} else if j := strings.Index(path, "/management"); j >= 0 {
		if k := strings.LastIndex(path[:j], "/"); k >= 0 && strings.HasPrefix(path[k:], "/v") {
			return "", fmt.Errorf("only %s is supported, got %s", ManagementPath, path[k:])
		}
	}
	return u.Scheme + "://" + u.Host + path + ManagementPath, nil
}

// RootURL returns the server address in front of the management path, for
// display and for opening the server's own pages.
func RootURL(managementURL string) string {
	return strings.TrimSuffix(managementURL, ManagementPath)
}
