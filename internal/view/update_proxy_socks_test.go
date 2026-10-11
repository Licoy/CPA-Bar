package view

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"testing"
)

func newSOCKSProxy(t *testing.T) (*url.URL, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	used := make(chan struct{}, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		header := make([]byte, 2)
		if _, err = io.ReadFull(reader, header); err != nil {
			return
		}
		methods := make([]byte, int(header[1]))
		if _, err = io.ReadFull(reader, methods); err != nil {
			return
		}
		_, _ = conn.Write([]byte{5, 0})
		request := make([]byte, 4)
		if _, err = io.ReadFull(reader, request); err != nil {
			return
		}
		host, err := readSOCKSHost(reader, request[3])
		if err != nil {
			return
		}
		portBytes := make([]byte, 2)
		if _, err = io.ReadFull(reader, portBytes); err != nil {
			return
		}
		upstream, err := net.Dial("tcp", net.JoinHostPort(host, fmt.Sprint(binary.BigEndian.Uint16(portBytes))))
		if err != nil {
			return
		}
		defer upstream.Close()
		_, _ = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
		used <- struct{}{}
		go func() { _, _ = io.Copy(upstream, reader) }()
		_, _ = io.Copy(conn, upstream)
	}()
	return &url.URL{Scheme: "socks5", Host: listener.Addr().String()}, used
}

func readSOCKSHost(reader io.Reader, addressType byte) (string, error) {
	switch addressType {
	case 1:
		value := make([]byte, 4)
		_, err := io.ReadFull(reader, value)
		return net.IP(value).String(), err
	case 3:
		length := []byte{0}
		if _, err := io.ReadFull(reader, length); err != nil {
			return "", err
		}
		value := make([]byte, int(length[0]))
		_, err := io.ReadFull(reader, value)
		return string(value), err
	case 4:
		value := make([]byte, 16)
		_, err := io.ReadFull(reader, value)
		return net.IP(value).String(), err
	default:
		return "", fmt.Errorf("unsupported SOCKS address type %d", addressType)
	}
}
