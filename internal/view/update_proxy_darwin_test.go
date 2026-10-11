//go:build darwin

package view

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"unsafe"

	"github.com/ebitengine/purego"
)

func TestCFNetworkSystemProxySettings(t *testing.T) {
	api, err := loadCFProxyAPI()
	if err != nil {
		t.Fatal(err)
	}
	b := newCFSettingsBuilder(t)
	tests := []struct {
		name     string
		target   string
		settings func() uintptr
		want     string
		wantErr  string
	}{
		{"HTTP", "http://updates.example/file", func() uintptr {
			return b.proxySettings("HTTP", "127.0.0.1", 8080, "")
		}, "http://127.0.0.1:8080", ""},
		{"HTTPS CONNECT", "https://updates.example/file", func() uintptr {
			return b.proxySettings("HTTPS", "proxy.example", 8443, "")
		}, "http://proxy.example:8443", ""},
		{"SOCKS", "https://updates.example/file", func() uintptr {
			return b.proxySettings("SOCKS", "127.0.0.1", 1080, "")
		}, "socks5://127.0.0.1:1080", ""},
		{"disabled", "http://updates.example/file", func() uintptr {
			return b.proxySettings("HTTP", "127.0.0.1", 8080, "disabled")
		}, "", ""},
		{"exception bypass", "http://updates.example/file", func() uintptr {
			return b.proxySettings("HTTP", "127.0.0.1", 8080, "*.example")
		}, "", ""},
		{"PAC", "http://updates.example/file", b.pacSettings, "", "PAC"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := test.settings()
			defer api.release(settings)
			target, _ := url.Parse(test.target)
			got, err := api.proxyForSettings(target, settings)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("proxy error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got == nil {
				if test.want != "" {
					t.Fatalf("proxy = nil, want %s", test.want)
				}
				return
			}
			if got.String() != test.want {
				t.Fatalf("proxy = %s, want %s", got, test.want)
			}
		})
	}
}

func TestSystemProxyForURLSmoke(t *testing.T) {
	if os.Getenv("CPA_TEST_SYSTEM_PROXY") != "1" {
		t.Skip("set CPA_TEST_SYSTEM_PROXY=1 to read the current macOS proxy settings")
	}
	target, _ := url.Parse("https://github.com/licoy/CPA-Bar/releases/latest")
	proxy, err := systemProxyForURL(target)
	if err != nil {
		t.Fatal(err)
	}
	if proxy != nil && proxy.Scheme != "http" && proxy.Scheme != "socks5" {
		t.Fatalf("unexpected system proxy %s", proxy)
	}
}

type cfSettingsBuilder struct {
	t                *testing.T
	api              *cfProxyAPI
	lib              uintptr
	dictionary       func(uintptr, int64, uintptr, uintptr) uintptr
	set              func(uintptr, uintptr, uintptr)
	string           func(uintptr, unsafe.Pointer, int64, uint32, uint8) uintptr
	number           func(uintptr, int64, unsafe.Pointer) uintptr
	array            func(uintptr, unsafe.Pointer, int64, uintptr) uintptr
	dictionaryKeys   uintptr
	dictionaryValues uintptr
	arrayValues      uintptr
}

func newCFSettingsBuilder(t *testing.T) *cfSettingsBuilder {
	t.Helper()
	api, err := loadCFProxyAPI()
	if err != nil {
		t.Fatal(err)
	}
	lib, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_LAZY|purego.RTLD_LOCAL)
	if err != nil {
		t.Fatal(err)
	}
	b := &cfSettingsBuilder{t: t, api: api, lib: lib}
	for _, fn := range []struct {
		dst  any
		name string
	}{
		{&b.dictionary, "CFDictionaryCreateMutable"}, {&b.set, "CFDictionarySetValue"},
		{&b.string, "CFStringCreateWithBytes"}, {&b.number, "CFNumberCreate"}, {&b.array, "CFArrayCreate"},
	} {
		if err := registerCFFunction(fn.dst, lib, fn.name); err != nil {
			t.Fatal(err)
		}
	}
	b.dictionaryKeys = b.symbol("kCFTypeDictionaryKeyCallBacks")
	b.dictionaryValues = b.symbol("kCFTypeDictionaryValueCallBacks")
	b.arrayValues = b.symbol("kCFTypeArrayCallBacks")
	return b
}

func (b *cfSettingsBuilder) proxySettings(kind, host string, port int64, exception string) uintptr {
	dict := b.newDictionary()
	prefix := "kCFNetworkProxies" + kind
	enabled := int64(1)
	if exception == "disabled" {
		enabled = 0
	}
	b.put(dict, prefix+"Enable", b.cfNumber(enabled))
	b.put(dict, prefix+"Proxy", b.cfString(host))
	b.put(dict, prefix+"Port", b.cfNumber(port))
	if exception != "" && exception != "disabled" {
		value := b.cfString(exception)
		array := b.array(0, unsafe.Pointer(&value), 1, b.arrayValues)
		b.put(dict, "kCFNetworkProxiesExceptionsList", array)
		b.api.release(value)
	}
	return dict
}

func (b *cfSettingsBuilder) pacSettings() uintptr {
	dict := b.newDictionary()
	b.put(dict, "kCFNetworkProxiesProxyAutoConfigEnable", b.cfNumber(1))
	b.put(dict, "kCFNetworkProxiesProxyAutoConfigURLString", b.cfString("https://proxy.example/config.pac"))
	return dict
}

func (b *cfSettingsBuilder) newDictionary() uintptr {
	return b.dictionary(0, 0, b.dictionaryKeys, b.dictionaryValues)
}

func (b *cfSettingsBuilder) put(dict uintptr, keyName string, value uintptr) {
	keyLib, err := purego.Dlopen("/System/Library/Frameworks/CFNetwork.framework/CFNetwork", purego.RTLD_LAZY|purego.RTLD_LOCAL)
	if err != nil {
		b.t.Fatal(err)
	}
	key, err := cfStringConstant(keyLib, keyName)
	if err != nil {
		b.t.Fatal(err)
	}
	b.set(dict, key, value)
	b.api.release(value)
}

func (b *cfSettingsBuilder) cfString(value string) uintptr {
	raw := []byte(value)
	if len(raw) == 0 {
		b.t.Fatal("empty CFString")
	}
	return b.string(0, unsafe.Pointer(&raw[0]), int64(len(raw)), cfStringEncodingUTF8, 0)
}

func (b *cfSettingsBuilder) cfNumber(value int64) uintptr {
	return b.number(0, 4, unsafe.Pointer(&value))
}

func (b *cfSettingsBuilder) symbol(name string) uintptr {
	symbol, err := purego.Dlsym(b.lib, name)
	if err != nil {
		b.t.Fatal(fmt.Errorf("load %s: %w", name, err))
	}
	return symbol
}
