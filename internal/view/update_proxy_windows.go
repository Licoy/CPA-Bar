//go:build windows

package view

import (
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	winHTTPAccessTypeNoProxy    = 1
	winHTTPAccessTypeNamedProxy = 3
	winHTTPAutoProxyAutoDetect  = 1
	winHTTPAutoProxyConfigURL   = 2
	winHTTPAutoDetectTypeDHCP   = 1
	winHTTPAutoDetectTypeDNSA   = 2
	winHTTPAutoProxyNotFound    = syscall.Errno(12178)
	winHTTPAutoDetectionFailed  = syscall.Errno(12180)
	winHTTPAutoProxyTimeoutMS   = 5000
)

type winHTTPCurrentUserProxyConfig struct {
	autoDetect    int32
	autoConfigURL *uint16
	proxy         *uint16
	bypass        *uint16
}

type winHTTPAutoProxyOptions struct {
	flags                 uint32
	autoDetectFlags       uint32
	autoConfigURL         *uint16
	reserved              uintptr
	reservedFlags         uint32
	autoLogonIfChallenged int32
}

type winHTTPProxyInfo struct {
	accessType uint32
	proxy      *uint16
	bypass     *uint16
}

var (
	winHTTPDLL                  = windows.NewLazySystemDLL("winhttp.dll")
	getCurrentUserIEProxyConfig = winHTTPDLL.NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	winHTTPOpen                 = winHTTPDLL.NewProc("WinHttpOpen")
	winHTTPSetTimeouts          = winHTTPDLL.NewProc("WinHttpSetTimeouts")
	winHTTPGetProxyForURL       = winHTTPDLL.NewProc("WinHttpGetProxyForUrl")
	winHTTPCloseHandle          = winHTTPDLL.NewProc("WinHttpCloseHandle")
	globalFree                  = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalFree")
)

func systemProxyForURL(target *url.URL) (*url.URL, error) {
	settings, err := readWindowsProxySettings()
	if err != nil {
		return nil, err
	}
	// Prefer an explicit manual proxy when Windows also has its commonly
	// enabled auto-detect toggle set.
	if settings.proxy != "" {
		return proxyFromWindowsSettings(target, settings)
	}
	if !settings.autoDetect && settings.autoConfigURL == "" {
		return nil, nil
	}
	settings, direct, err := readWindowsAutoProxy(target, settings)
	if err != nil || direct {
		return nil, err
	}
	return proxyFromWindowsSettings(target, settings)
}

func readWindowsProxySettings() (windowsProxySettings, error) {
	if err := findWindowsProxyProcedures(getCurrentUserIEProxyConfig, globalFree); err != nil {
		return windowsProxySettings{}, err
	}
	var config winHTTPCurrentUserProxyConfig
	ok, _, callErr := getCurrentUserIEProxyConfig.Call(uintptr(unsafe.Pointer(&config)))
	if ok == 0 {
		freeErr := freeWindowsProxyStrings(config.autoConfigURL, config.proxy, config.bypass)
		return windowsProxySettings{}, errors.Join(fmt.Errorf("system proxy: read current user proxy settings: %w", callErr), freeErr)
	}
	settings := windowsProxySettings{
		autoDetect:    config.autoDetect != 0,
		autoConfigURL: windows.UTF16PtrToString(config.autoConfigURL),
		proxy:         windows.UTF16PtrToString(config.proxy),
		bypass:        windows.UTF16PtrToString(config.bypass),
	}
	return settings, freeWindowsProxyStrings(config.autoConfigURL, config.proxy, config.bypass)
}

func readWindowsAutoProxy(target *url.URL, settings windowsProxySettings) (windowsProxySettings, bool, error) {
	if target == nil || target.Hostname() == "" {
		return windowsProxySettings{}, false, errors.New("system proxy: target URL is missing a host")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return windowsProxySettings{}, false, fmt.Errorf("system proxy: unsupported target scheme %q", target.Scheme)
	}
	if err := findWindowsProxyProcedures(winHTTPOpen, winHTTPSetTimeouts, winHTTPGetProxyForURL, winHTTPCloseHandle, globalFree); err != nil {
		return windowsProxySettings{}, false, err
	}
	session, err := openWindowsAutoProxySession()
	if err != nil {
		return windowsProxySettings{}, false, err
	}
	resolved, direct, resolveErr := callWindowsAutoProxy(session, target, settings)
	closeErr := closeWindowsAutoProxySession(session)
	if resolveErr != nil || closeErr != nil {
		return windowsProxySettings{}, false, errors.Join(resolveErr, closeErr)
	}
	return resolved, direct, nil
}

func openWindowsAutoProxySession() (uintptr, error) {
	userAgent, err := windows.UTF16PtrFromString("CPA-Bar updater")
	if err != nil {
		return 0, fmt.Errorf("system proxy: encode WinHTTP user agent: %w", err)
	}
	session, _, callErr := winHTTPOpen.Call(uintptr(unsafe.Pointer(userAgent)), winHTTPAccessTypeNoProxy, 0, 0, 0)
	runtime.KeepAlive(userAgent)
	if session == 0 {
		return 0, fmt.Errorf("system proxy: open WinHTTP session: %w", callErr)
	}
	if ok, _, timeoutErr := winHTTPSetTimeouts.Call(session, winHTTPAutoProxyTimeoutMS, winHTTPAutoProxyTimeoutMS, winHTTPAutoProxyTimeoutMS, winHTTPAutoProxyTimeoutMS); ok == 0 {
		closeErr := closeWindowsAutoProxySession(session)
		return 0, errors.Join(fmt.Errorf("system proxy: set WinHTTP automatic proxy timeouts: %w", timeoutErr), closeErr)
	}
	return session, nil
}

func callWindowsAutoProxy(session uintptr, target *url.URL, settings windowsProxySettings) (windowsProxySettings, bool, error) {
	options, configURL, err := windowsAutoProxyOptions(settings)
	if err != nil {
		return windowsProxySettings{}, false, err
	}
	targetURL, err := windows.UTF16PtrFromString(target.String())
	if err != nil {
		return windowsProxySettings{}, false, fmt.Errorf("system proxy: encode target URL: %w", err)
	}
	var info winHTTPProxyInfo
	ok, _, callErr := winHTTPGetProxyForURL.Call(session, uintptr(unsafe.Pointer(targetURL)), uintptr(unsafe.Pointer(&options)), uintptr(unsafe.Pointer(&info)))
	runtime.KeepAlive(targetURL)
	runtime.KeepAlive(configURL)
	if ok == 0 {
		freeErr := freeWindowsProxyStrings(info.proxy, info.bypass)
		if settings.autoConfigURL == "" && (errors.Is(callErr, winHTTPAutoProxyNotFound) || errors.Is(callErr, winHTTPAutoDetectionFailed)) {
			return windowsProxySettings{}, true, freeErr
		}
		return windowsProxySettings{}, false, errors.Join(fmt.Errorf("system proxy: resolve automatic proxy: %w", callErr), freeErr)
	}
	resolved := windowsProxySettings{proxy: windows.UTF16PtrToString(info.proxy), bypass: windows.UTF16PtrToString(info.bypass)}
	freeErr := freeWindowsProxyStrings(info.proxy, info.bypass)
	if freeErr != nil {
		return windowsProxySettings{}, false, freeErr
	}
	switch info.accessType {
	case winHTTPAccessTypeNoProxy:
		return windowsProxySettings{}, true, nil
	case winHTTPAccessTypeNamedProxy:
		if resolved.proxy == "" {
			return windowsProxySettings{}, false, errors.New("system proxy: automatic proxy returned an empty proxy list")
		}
		return resolved, false, nil
	default:
		return windowsProxySettings{}, false, fmt.Errorf("system proxy: automatic proxy returned unsupported access type %d", info.accessType)
	}
}

func windowsAutoProxyOptions(settings windowsProxySettings) (winHTTPAutoProxyOptions, *uint16, error) {
	if settings.autoConfigURL != "" {
		configURL, err := windows.UTF16PtrFromString(settings.autoConfigURL)
		if err != nil {
			return winHTTPAutoProxyOptions{}, nil, fmt.Errorf("system proxy: encode PAC URL: %w", err)
		}
		return winHTTPAutoProxyOptions{flags: winHTTPAutoProxyConfigURL, autoConfigURL: configURL}, configURL, nil
	}
	return winHTTPAutoProxyOptions{
		flags:           winHTTPAutoProxyAutoDetect,
		autoDetectFlags: winHTTPAutoDetectTypeDHCP | winHTTPAutoDetectTypeDNSA,
	}, nil, nil
}

func closeWindowsAutoProxySession(session uintptr) error {
	if ok, _, callErr := winHTTPCloseHandle.Call(session); ok == 0 {
		return fmt.Errorf("system proxy: close WinHTTP session: %w", callErr)
	}
	return nil
}

func findWindowsProxyProcedures(procedures ...*windows.LazyProc) error {
	for _, procedure := range procedures {
		if err := procedure.Find(); err != nil {
			return fmt.Errorf("system proxy: load %s: %w", procedure.Name, err)
		}
	}
	return nil
}

func freeWindowsProxyStrings(values ...*uint16) error {
	var firstErr error
	for _, value := range values {
		if value == nil {
			continue
		}
		if result, _, _ := globalFree.Call(uintptr(unsafe.Pointer(value))); result != 0 && firstErr == nil {
			firstErr = errors.New("system proxy: GlobalFree failed")
		}
	}
	return firstErr
}
