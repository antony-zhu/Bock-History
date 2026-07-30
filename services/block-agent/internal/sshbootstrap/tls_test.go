package sshbootstrap

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type singleConnectionListener struct {
	connection net.Conn
	nextError  error
	accepted   bool
}

func (l *singleConnectionListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.connection, nil
	}
	return nil, l.nextError
}

func (l *singleConnectionListener) Close() error {
	return nil
}

func (l *singleConnectionListener) Addr() net.Addr {
	return l.connection.LocalAddr()
}

type deadlineRecordingConn struct {
	net.Conn
	deadlines []time.Time
}

func (c *deadlineRecordingConn) SetDeadline(deadline time.Time) error {
	c.deadlines = append(c.deadlines, deadline)
	return c.Conn.SetDeadline(deadline)
}

func TestTLSHandshakeTimeoutAppliesToSilentConnection(t *testing.T) {
	serverConnection, clientConnection := net.Pipe()
	defer clientConnection.Close()

	recordingConnection := &deadlineRecordingConn{Conn: serverConnection}
	listenerClosed := errors.New("listener closed after injected connection")
	fixedNow := time.Unix(1, 0)
	listener := &tlsHandshakeListener{
		Listener: &singleConnectionListener{
			connection: recordingConnection,
			nextError:  listenerClosed,
		},
		config: &tls.Config{MinVersion: tls.VersionTLS12},
		now:    func() time.Time { return fixedNow },
	}

	connection, err := listener.Accept()
	if connection != nil {
		t.Fatal("silent connection unexpectedly completed a TLS handshake")
	}
	if !errors.Is(err, listenerClosed) {
		t.Fatalf("Accept error = %v, want injected listener error", err)
	}
	if len(recordingConnection.deadlines) != 1 {
		t.Fatalf("deadline calls = %d, want exactly one handshake deadline", len(recordingConnection.deadlines))
	}
	if got, want := recordingConnection.deadlines[0], fixedNow.Add(10*time.Second); !got.Equal(want) {
		t.Fatalf("handshake deadline = %v, want %v", got, want)
	}

	_ = clientConnection.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 1)
	if _, err := clientConnection.Read(buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("silent connection was not closed after handshake timeout: %v", err)
	}
}
