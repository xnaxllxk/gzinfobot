package main

// Защита от SSRF: ссылки, которые админ вводит из чата, не должны позволять боту ходить
// во внутреннюю сеть сервера (localhost, 192.168.x.x, метаданные облака 169.254.169.254).
// Защита двойная: ссылка проверяется при сохранении, а сам HTTP-клиент отказывается
// подключаться к нерегулярным адресам — это ловит и редиректы, и подмену DNS.

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// strictURLs выключается только в тестах (там сервер слушает на 127.0.0.1).
var strictURLs = true

var blockedNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range blockedNets {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// validateURL: только https, без логина в ссылке, порт 443, не внутренние адреса и имена.
func validateURL(s string) (string, error) {
	if len(s) > 500 {
		return "", errors.New("ссылка слишком длинная")
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", errors.New("некорректная ссылка")
	}
	if u.Scheme != "https" {
		return "", errors.New("разрешены только ссылки https://")
	}
	if u.User != nil {
		return "", errors.New("логин и пароль в ссылке не допускаются")
	}
	if !strictURLs {
		return u.String(), nil
	}
	if p := u.Port(); p != "" && p != "443" {
		return "", errors.New("нестандартный порт не допускается")
	}
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err == nil {
		if !publicIP(ip) {
			return "", errors.New("адрес внутренней сети не допускается")
		}
		return u.String(), nil
	}
	if !strings.Contains(host, ".") || host == "localhost" {
		return "", errors.New("внутренние имена хостов не допускаются")
	}
	for _, suf := range []string{".localhost", ".local", ".internal", ".lan", ".home", ".corp", ".intranet"} {
		if strings.HasSuffix(host, suf) {
			return "", errors.New("внутренние имена хостов не допускаются")
		}
	}
	return u.String(), nil
}

// newSafeClient — HTTP-клиент, который не подключается к внутренним адресам.
// Прокси из окружения не используются: иначе проверка адреса шла бы к прокси, а не к цели.
func newSafeClient(timeout time.Duration) *http.Client {
	d := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !publicIP(ip) {
				return errors.New("подключение к внутреннему адресу запрещено")
			}
			return nil
		},
	}
	tr := &http.Transport{
		DialContext:         d.DialContext,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        10,
		IdleConnTimeout:     60 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("слишком много перенаправлений")
			}
			if req.URL.Scheme != "https" {
				return errors.New("перенаправление на не-https запрещено")
			}
			return nil
		},
	}
}
