//go:build !darwin && !windows && !linux

package view

import "net/url"

func systemProxyForURL(*url.URL) (*url.URL, error) { return nil, nil }
