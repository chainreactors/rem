//go:build !tinygo

package core

import "net"

func (u *URL) IP() net.IP {
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return nil
	}

	return ips[0]
}
