package sshbootstrap

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

const tlsHandshakeTimeout = 10 * time.Second

type tlsHandshakeListener struct {
	net.Listener
	config *tls.Config
	now    func() time.Time
}

func (l *tlsHandshakeListener) handshakeDeadline() time.Time {
	if l.now != nil {
		return l.now().Add(tlsHandshakeTimeout)
	}
	return time.Now().Add(tlsHandshakeTimeout)
}

func (l *tlsHandshakeListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		tlsConnection := tls.Server(connection, l.config)
		_ = tlsConnection.SetDeadline(l.handshakeDeadline())
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
