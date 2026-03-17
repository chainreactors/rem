package socks5

import (
	"fmt"
	"io"
	"net"
	"strconv"
)

// ClientConnect performs a standard SOCKS5 CONNECT handshake on an already-established
// connection. It negotiates authentication (NoAuth or Username/Password), sends the
// CONNECT request for the given address, and reads the server reply.
// address must be in "host:port" format.
func ClientConnect(conn io.ReadWriter, address, user, pass string) error {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("socks5 client: invalid address %q: %w", address, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("socks5 client: invalid port %q: %w", portStr, err)
	}

	// Step 1: Method negotiation
	useAuth := user != "" || pass != ""
	if useAuth {
		// Offer both NoAuth and UserPass
		_, err = conn.Write([]byte{socks5Version, 2, NoAuth, UserPassAuth})
	} else {
		_, err = conn.Write([]byte{socks5Version, 1, NoAuth})
	}
	if err != nil {
		return fmt.Errorf("socks5 client: method negotiation write: %w", err)
	}

	// Read server's chosen method
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		return fmt.Errorf("socks5 client: method negotiation read: %w", err)
	}
	if methodReply[0] != socks5Version {
		return fmt.Errorf("socks5 client: unexpected version %d", methodReply[0])
	}

	// Step 2: Authentication
	switch methodReply[1] {
	case NoAuth:
		// No authentication required
	case UserPassAuth:
		if !useAuth {
			return fmt.Errorf("socks5 client: server requires auth but no credentials provided")
		}
		// RFC 1929: username/password sub-negotiation
		authMsg := make([]byte, 0, 3+len(user)+len(pass))
		authMsg = append(authMsg, userAuthVersion)
		authMsg = append(authMsg, byte(len(user)))
		authMsg = append(authMsg, []byte(user)...)
		authMsg = append(authMsg, byte(len(pass)))
		authMsg = append(authMsg, []byte(pass)...)
		if _, err := conn.Write(authMsg); err != nil {
			return fmt.Errorf("socks5 client: auth write: %w", err)
		}
		authReply := make([]byte, 2)
		if _, err := io.ReadFull(conn, authReply); err != nil {
			return fmt.Errorf("socks5 client: auth read: %w", err)
		}
		if authReply[1] != authSuccess {
			return UserAuthFailed
		}
	case noAcceptable:
		return NoSupportedAuth
	default:
		return fmt.Errorf("socks5 client: unsupported auth method %d", methodReply[1])
	}

	// Step 3: Send CONNECT request
	req := make([]byte, 0, 10+len(host))
	req = append(req, socks5Version, ConnectCommand, 0x00) // VER, CMD, RSV

	ip := net.ParseIP(host)
	if ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			req = append(req, ipv4Address)
			req = append(req, ip4...)
		} else {
			req = append(req, ipv6Address)
			req = append(req, ip.To16()...)
		}
	} else {
		req = append(req, fqdnAddress)
		req = append(req, byte(len(host)))
		req = append(req, []byte(host)...)
	}
	req = append(req, byte(port>>8), byte(port))

	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("socks5 client: connect request write: %w", err)
	}

	// Step 4: Read reply
	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("socks5 client: connect reply read: %w", err)
	}
	if reply[0] != socks5Version {
		return fmt.Errorf("socks5 client: unexpected reply version %d", reply[0])
	}
	if reply[1] != SuccessReply {
		return fmt.Errorf("socks5 client: connect failed with code %d", reply[1])
	}

	// Consume the bound address (we don't need it but must read it)
	switch reply[3] {
	case ipv4Address:
		buf := make([]byte, 4+2) // IPv4 + port
		_, err = io.ReadFull(conn, buf)
	case ipv6Address:
		buf := make([]byte, 16+2) // IPv6 + port
		_, err = io.ReadFull(conn, buf)
	case fqdnAddress:
		fqdnLen := make([]byte, 1)
		if _, err = io.ReadFull(conn, fqdnLen); err == nil {
			buf := make([]byte, int(fqdnLen[0])+2) // FQDN + port
			_, err = io.ReadFull(conn, buf)
		}
	default:
		return fmt.Errorf("socks5 client: unsupported bind address type %d", reply[3])
	}
	if err != nil {
		return fmt.Errorf("socks5 client: reading bind address: %w", err)
	}

	return nil
}
