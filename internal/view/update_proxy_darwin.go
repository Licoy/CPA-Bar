//go:build darwin

package view

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const cfStringEncodingUTF8 = 0x08000100

type cfProxyAPI struct {
	copySettings func() uintptr
	copyForURL   func(uintptr, uintptr) uintptr
	urlWithBytes func(uintptr, unsafe.Pointer, int64, uint32, uintptr) uintptr
	arrayCount   func(uintptr) int64
	arrayValue   func(uintptr, int64) uintptr
	dictValue    func(uintptr, uintptr) uintptr
	equal        func(uintptr, uintptr) uint8
	string       func(uintptr, unsafe.Pointer, int64, uint32) uint8
	number       func(uintptr, int64, unsafe.Pointer) uint8
	release      func(uintptr)

	typeKey, hostKey, portKey                uintptr
	typeNone, typeHTTP, typeHTTPS, typeSOCKS uintptr
	typePACURL, typePACJavaScript            uintptr
}

var (
	cfProxyOnce sync.Once
	cfProxy     cfProxyAPI
	cfProxyErr  error
)

func systemProxyForURL(target *url.URL) (*url.URL, error) {
	api, err := loadCFProxyAPI()
	if err != nil {
		return nil, err
	}
	settings := api.copySettings()
	if settings == 0 {
		return nil, errors.New("system proxy: CFNetwork returned no proxy settings")
	}
	defer api.release(settings)
	return api.proxyForSettings(target, settings)
}

func (api *cfProxyAPI) proxyForSettings(target *url.URL, settings uintptr) (*url.URL, error) {
	raw := []byte(target.String())
	if len(raw) == 0 {
		return nil, errors.New("system proxy: empty target URL")
	}
	cfURL := api.urlWithBytes(0, unsafe.Pointer(&raw[0]), int64(len(raw)), cfStringEncodingUTF8, 0)
	if cfURL == 0 {
		return nil, fmt.Errorf("system proxy: cannot create URL %q", target)
	}
	defer api.release(cfURL)
	proxies := api.copyForURL(cfURL, settings)
	if proxies == 0 {
		return nil, errors.New("system proxy: CFNetwork could not resolve the proxy")
	}
	defer api.release(proxies)
	if api.arrayCount(proxies) == 0 {
		return nil, errors.New("system proxy: CFNetwork returned an empty proxy list")
	}
	return api.proxyURL(api.arrayValue(proxies, 0))
}

func (api *cfProxyAPI) proxyURL(dict uintptr) (*url.URL, error) {
	kind := api.dictValue(dict, api.typeKey)
	switch {
	case kind == 0:
		return nil, errors.New("system proxy: proxy type is missing")
	case api.equal(kind, api.typeNone) != 0:
		return nil, nil
	case api.equal(kind, api.typePACURL) != 0 || api.equal(kind, api.typePACJavaScript) != 0:
		return nil, errors.New("system proxy: automatic proxy configuration (PAC) is not supported")
	}
	scheme := ""
	switch {
	case api.equal(kind, api.typeHTTP) != 0, api.equal(kind, api.typeHTTPS) != 0:
		scheme = "http"
	case api.equal(kind, api.typeSOCKS) != 0:
		scheme = "socks5"
	default:
		return nil, errors.New("system proxy: CFNetwork returned an unsupported proxy type")
	}
	host, err := api.stringValue(api.dictValue(dict, api.hostKey))
	if err != nil {
		return nil, err
	}
	var port int64
	if number := api.dictValue(dict, api.portKey); number == 0 || api.number(number, 4, unsafe.Pointer(&port)) == 0 {
		return nil, errors.New("system proxy: proxy port is missing or invalid")
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("system proxy: invalid proxy port %d", port)
	}
	return &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, fmt.Sprint(port))}, nil
}

func (api *cfProxyAPI) stringValue(value uintptr) (string, error) {
	if value == 0 {
		return "", errors.New("system proxy: proxy host is missing")
	}
	buf := make([]byte, 4096)
	if api.string(value, unsafe.Pointer(&buf[0]), int64(len(buf)), cfStringEncodingUTF8) == 0 {
		return "", errors.New("system proxy: proxy host is invalid")
	}
	for i, b := range buf {
		if b == 0 {
			if i == 0 {
				return "", errors.New("system proxy: proxy host is empty")
			}
			return string(buf[:i]), nil
		}
	}
	return "", errors.New("system proxy: proxy host is too long")
}

func loadCFProxyAPI() (*cfProxyAPI, error) {
	cfProxyOnce.Do(func() {
		cfNetwork, err := purego.Dlopen("/System/Library/Frameworks/CFNetwork.framework/CFNetwork", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if err != nil {
			cfProxyErr = fmt.Errorf("system proxy: load CFNetwork: %w", err)
			return
		}
		coreFoundation, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if err != nil {
			cfProxyErr = fmt.Errorf("system proxy: load CoreFoundation: %w", err)
			return
		}
		for _, fn := range []struct {
			dst  any
			lib  uintptr
			name string
		}{
			{&cfProxy.copySettings, cfNetwork, "CFNetworkCopySystemProxySettings"},
			{&cfProxy.copyForURL, cfNetwork, "CFNetworkCopyProxiesForURL"},
			{&cfProxy.urlWithBytes, coreFoundation, "CFURLCreateWithBytes"},
			{&cfProxy.arrayCount, coreFoundation, "CFArrayGetCount"},
			{&cfProxy.arrayValue, coreFoundation, "CFArrayGetValueAtIndex"},
			{&cfProxy.dictValue, coreFoundation, "CFDictionaryGetValue"},
			{&cfProxy.equal, coreFoundation, "CFEqual"},
			{&cfProxy.string, coreFoundation, "CFStringGetCString"},
			{&cfProxy.number, coreFoundation, "CFNumberGetValue"},
			{&cfProxy.release, coreFoundation, "CFRelease"},
		} {
			if cfProxyErr = registerCFFunction(fn.dst, fn.lib, fn.name); cfProxyErr != nil {
				return
			}
		}
		for _, value := range []struct {
			dst  *uintptr
			name string
		}{
			{&cfProxy.typeKey, "kCFProxyTypeKey"}, {&cfProxy.hostKey, "kCFProxyHostNameKey"},
			{&cfProxy.portKey, "kCFProxyPortNumberKey"}, {&cfProxy.typeNone, "kCFProxyTypeNone"},
			{&cfProxy.typeHTTP, "kCFProxyTypeHTTP"}, {&cfProxy.typeHTTPS, "kCFProxyTypeHTTPS"},
			{&cfProxy.typeSOCKS, "kCFProxyTypeSOCKS"}, {&cfProxy.typePACURL, "kCFProxyTypeAutoConfigurationURL"},
			{&cfProxy.typePACJavaScript, "kCFProxyTypeAutoConfigurationJavaScript"},
		} {
			if *value.dst, cfProxyErr = cfStringConstant(cfNetwork, value.name); cfProxyErr != nil {
				return
			}
		}
	})
	return &cfProxy, cfProxyErr
}

func registerCFFunction(dst any, lib uintptr, name string) error {
	symbol, err := purego.Dlsym(lib, name)
	if err != nil {
		return fmt.Errorf("system proxy: load %s: %w", name, err)
	}
	purego.RegisterFunc(dst, symbol)
	return nil
}

func cfStringConstant(lib uintptr, name string) (uintptr, error) {
	symbol, err := purego.Dlsym(lib, name)
	if err != nil {
		return 0, fmt.Errorf("system proxy: load %s: %w", name, err)
	}
	return *(*uintptr)(unsafe.Pointer(symbol)), nil
}
