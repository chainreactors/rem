package socks5

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
)

const (
	socks5Version = uint8(5)
)

var requestReaderPool = sync.Pool{
	New: func() interface{} {
		return bufio.NewReaderSize(nil, 512)
	},
}

// Config is used to setup and configure a Server
type Config struct {
	// AuthMethods can be provided to implement custom authentication
	// By default, "auth-less" mode is enabled.
	// For password-based auth use UserPassAuthenticator.
	AuthMethods []Authenticator

	// If provided, username/password authentication is enabled,
	// by appending a UserPassAuthenticator to AuthMethods. If not provided,
	// and AUthMethods is nil, then "auth-less" mode is enabled.
	Credentials CredentialStore

	// Resolver can be provided to do custom name resolution.
	// Defaults to DNSResolver if not provided.
	Resolver NameResolver

	// Rules is provided to enable custom logic around permitting
	// various commands. If not provided, PermitAll is used.
	Rules RuleSet

	// Rewriter can be used to transparently rewrite addresses.
	// This is invoked before the RuleSet is invoked.
	// Defaults to NoRewrite.
	Rewriter AddressRewriter

	// BindIP is used for bind or udp associate
	BindIP net.IP

	// Logger can be used to provide a custom log target.
	// Defaults to stdout.
	Logger *log.Logger

	// Optional function for dialing out
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)

	SendReply func(w io.Writer, resp uint8, addr *AddrSpec) error
}

// Server is reponsible for accepting connections and handling
// the details of the SOCKS5 protocol
type Server struct {
	*Config
	authMethods map[uint8]Authenticator
}

// New creates a new Server and potentially returns an error
func New(conf *Config) (*Server, error) {
	// Ensure we have at least one authentication method enabled
	if len(conf.AuthMethods) == 0 {
		if conf.Credentials != nil {
			conf.AuthMethods = []Authenticator{&UserPassAuthenticator{conf.Credentials}}
		} else {
			conf.AuthMethods = []Authenticator{&NoAuthAuthenticator{}}
		}
	}

	// Ensure we have a DNS resolver
	if conf.Resolver == nil {
		conf.Resolver = DNSResolver{}
	}

	// Ensure we have a rule set
	if conf.Rules == nil {
		conf.Rules = PermitAll()
	}

	// Ensure we have a log target
	if conf.Logger == nil {
		conf.Logger = log.New(os.Stdout, "", log.LstdFlags)
	}

	if conf.SendReply == nil {
		conf.SendReply = DefaultSendReply
	}

	server := &Server{
		Config: conf,
	}

	server.authMethods = make(map[uint8]Authenticator)

	for _, a := range conf.AuthMethods {
		server.authMethods[a.GetCode()] = a
	}

	return server, nil
}

// ListenAndServe is used to create a listener and serve on it
func (s *Server) ListenAndServe(network, addr string) error {
	l, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	return s.Serve(l)
}

// Serve is used to serve connections from a listener
func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					s.Config.Logger.Printf("[ERR] socks5: panic in ServeConn: %v", r)
				}
			}()
			if err := s.ServeConn(conn); err != nil {
				s.Config.Logger.Printf("[ERR] socks5: %v", err)
			}
		}()
	}
}

// ServeConn is used to serve a single connection.
func (s *Server) ServeConn(conn net.Conn) error {
	defer conn.Close()
	ctx := context.Background()

	req, err := s.ParseConn(ctx, conn)
	if err != nil {
		return err
	}

	// Switch on the command
	switch req.Command {
	case ConnectCommand:
		target, err := s.Dial(ctx, req, conn)
		if err != nil {
			return err
		}
		return s.HandleConnect(conn, target)
	default:
		if err := s.SendReply(conn, CommandNotSupported, nil); err != nil {
			return fmt.Errorf("Failed to send reply: %v", err)
		}
		return fmt.Errorf("Unsupported command: %v", req.Command)
	}
}

func (s *Server) ParseRequest(conn net.Conn) (*Request, error) {
	bufConn := requestReaderPool.Get().(*bufio.Reader)
	bufConn.Reset(conn)
	defer func() {
		bufConn.Reset(nil)
		requestReaderPool.Put(bufConn)
	}()

	// Read the version byte
	version := []byte{0}
	if _, err := bufConn.Read(version); err != nil {
		return nil, err
	}

	// Ensure we are compatible
	if version[0] != socks5Version {
		err := fmt.Errorf("Unsupported SOCKS version: %v", version)
		return nil, err
	}

	// Authenticate the connection
	authContext, err := s.authenticate(conn, bufConn)
	if err != nil {
		err = fmt.Errorf("Failed to authenticate: %v", err)
		return nil, err
	}

	request, err := NewRequest(bufConn)
	if err != nil {
		if err == unrecognizedAddrType {
			if err := s.SendReply(conn, AddrTypeNotSupported, nil); err != nil {
				return nil, err
			}
		}
		return nil, fmt.Errorf("Failed to read destination address: %v", err)
	}
	request.AuthContext = authContext
	if client, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		request.RemoteAddr = &AddrSpec{IP: client.IP, Port: client.Port}
	}
	return request, nil
}

func (s *Server) ParseConn(ctx context.Context, conn net.Conn) (*Request, error) {
	request, err := s.ParseRequest(conn)
	if err != nil {
		return nil, err
	}
	// Resolve the address if we have a FQDN
	dest := request.DestAddr
	if dest.FQDN != "" {
		var err error
		var addr net.IP
		ctx, addr, err = s.Config.Resolver.Resolve(ctx, dest.FQDN)
		if err != nil {
			if err := s.SendReply(conn, HostUnreachable, nil); err != nil {
				return nil, fmt.Errorf("Failed to send reply: %v", err)
			}
			return nil, fmt.Errorf("Failed to resolve destination '%v': %v", dest.FQDN, err)
		}
		dest.IP = addr
	}

	// Apply any address rewrites
	request.realDestAddr = request.DestAddr
	if s.Config.Rewriter != nil {
		_, request.realDestAddr = s.Config.Rewriter.Rewrite(ctx, request)
	}

	return request, nil
}
