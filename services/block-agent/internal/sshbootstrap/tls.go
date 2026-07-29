package sshbootstrap

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

type tlsHandshakeListener struct {
	net.Listener
	config *tls.Config
}

func (l *tlsHandshakeListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		tlsConnection := tls.Server(connection, l.config)
		_ = tlsConnection.SetDeadline(time.Now().Add(10 * time.Second))
		if err := tlsConnection.HandshakeContext(context.Background()); err != nil {
			_ = connection.Close()
			continue
		}
		_ = tlsConnection.SetDeadline(time.Time{})
		return tlsConnection, nil
	}
}

func ServeTLSOnly(server *http.Server, certificatePath, privateKeyPath string) error {
	certificate, err := tls.LoadX509KeyPair(certificatePath, privateKeyPath)
	if err != nil {
		return err
	}
	config := server.TLSConfig.Clone()
	config.Certificates = []tls.Certificate{certificate}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	return server.Serve(&tlsHandshakeListener{Listener: listener, config: config})
}
