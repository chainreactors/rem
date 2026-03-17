package wireguard

import (
	"bufio"
	"bytes"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// bringUpWGInterface creates a netstack TUN device and configures the
// WireGuard peer. The caller is responsible for calling dev.Up().
func bringUpWGInterface(cfg wgConfig) (*device.Device, *netstack.Net, error) {
	dnsAddrs := parseDNSAddrs(cfg.dns)
	tunDev, tNet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(cfg.tunIP)},
		dnsAddrs,
		cfg.mtu,
	)
	if err != nil {
		return nil, nil, err
	}

	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), device.NewLogger(cfg.logLevel, "[wg] "))

	wgConf := bytes.NewBuffer(nil)
	fmt.Fprintf(wgConf, "private_key=%s\n", cfg.privateKey)
	fmt.Fprintf(wgConf, "public_key=%s\n", cfg.peerPublicKey)
	fmt.Fprintf(wgConf, "endpoint=%s:%d\n", cfg.endpoint, cfg.port)
	fmt.Fprintf(wgConf, "allowed_ip=%s\n", cfg.allowedIPs)
	if cfg.keepalive > 0 {
		fmt.Fprintf(wgConf, "persistent_keepalive_interval=%d\n", cfg.keepalive)
	}
	if cfg.psk != "" {
		fmt.Fprintf(wgConf, "preshared_key=%s\n", cfg.psk)
	}

	if err := dev.IpcSetOperation(bufio.NewReader(wgConf)); err != nil {
		dev.Close()
		return nil, nil, err
	}
	return dev, tNet, nil
}

// parseDeviceStats reads IPC stats from a WireGuard device and returns
// per-peer statistics.
func parseDeviceStats(dev *device.Device) ([]PeerStats, error) {
	var buf bytes.Buffer
	if err := dev.IpcGetOperation(&buf); err != nil {
		return nil, err
	}

	var stats []PeerStats
	var current *PeerStats
	var handshakeSec int64

	scanner := bufio.NewScanner(&buf)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, val := parts[0], parts[1]

		switch key {
		case "public_key":
			stats = append(stats, PeerStats{PublicKey: val})
			current = &stats[len(stats)-1]
			handshakeSec = 0
		case "endpoint":
			if current != nil {
				current.Endpoint = val
			}
		case "tx_bytes":
			if current != nil {
				current.TxBytes, _ = strconv.ParseUint(val, 10, 64)
			}
		case "rx_bytes":
			if current != nil {
				current.RxBytes, _ = strconv.ParseUint(val, 10, 64)
			}
		case "last_handshake_time_sec":
			if current != nil {
				handshakeSec, _ = strconv.ParseInt(val, 10, 64)
			}
		case "last_handshake_time_nsec":
			if current != nil {
				nsec, _ := strconv.ParseInt(val, 10, 64)
				if handshakeSec > 0 || nsec > 0 {
					current.LastHandshake = time.Unix(handshakeSec, nsec)
				}
			}
		case "persistent_keepalive_interval":
			if current != nil {
				current.Keepalive, _ = strconv.Atoi(val)
			}
		case "allowed_ip":
			if current != nil {
				current.AllowedIPs = append(current.AllowedIPs, val)
			}
		}
	}
	return stats, nil
}

// pingViaTNet sends an ICMP echo through the netstack tunnel.
func pingViaTNet(tNet *netstack.Net, laddr, raddr netip.Addr, timeout time.Duration) (time.Duration, error) {
	if tNet == nil {
		return 0, fmt.Errorf("wireguard not connected")
	}
	pc, err := tNet.DialPingAddr(laddr, raddr)
	if err != nil {
		return 0, fmt.Errorf("ping dial failed: %w", err)
	}
	defer pc.Close()

	pc.SetDeadline(time.Now().Add(timeout))

	start := time.Now()
	if _, err := pc.Write([]byte("ping")); err != nil {
		return 0, fmt.Errorf("ping write failed: %w", err)
	}
	buf := make([]byte, 256)
	if _, err := pc.Read(buf); err != nil {
		return 0, fmt.Errorf("ping read failed: %w", err)
	}
	return time.Since(start), nil
}

// checkHealthFromDevice evaluates tunnel health based on peer handshake times.
func checkHealthFromDevice(dev *device.Device) (*HealthStatus, error) {
	if dev == nil {
		return nil, fmt.Errorf("wireguard device not initialized")
	}
	stats, err := parseDeviceStats(dev)
	if err != nil {
		return nil, err
	}

	hs := &HealthStatus{Healthy: true}
	now := time.Now()

	if len(stats) == 0 {
		hs.Healthy = false
		return hs, nil
	}

	for _, ps := range stats {
		if ps.LastHandshake.IsZero() {
			hs.Healthy = false
			continue
		}
		stale := now.Sub(ps.LastHandshake)
		if stale > hs.StaleDuration {
			hs.StaleDuration = stale
			hs.LastHandshake = ps.LastHandshake
		}
		threshold := time.Duration(ps.Keepalive) * 5 * time.Second
		if threshold == 0 {
			threshold = 125 * time.Second
		}
		if stale > threshold {
			hs.Healthy = false
		}
	}
	return hs, nil
}
