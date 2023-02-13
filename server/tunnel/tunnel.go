package tunnel

import (
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/azimjohn/jprq/server/events"
	"github.com/azimjohn/jprq/server/server"
	"io"
	"net"
	"sync"
)

type Tunnel interface {
	Open()
	Close()
	Hostname() string
	Protocol() string
	PublicServerPort() uint16
	PrivateServerPort() uint16
}

type tunnel struct {
	hostname      string
	maxConsLimit  int
	eventWriter   io.Writer
	eventWriterMx sync.Mutex
	privateServer server.TCPServer
	publicCons    map[uint16]net.Conn
	initialBuffer map[uint16][]byte
}

func newTunnel(hostname string, eventWriter io.Writer, maxConsLimit int) tunnel {
	return tunnel{
		hostname:      hostname,
		maxConsLimit:  maxConsLimit,
		eventWriter:   eventWriter,
		publicCons:    make(map[uint16]net.Conn),
		initialBuffer: make(map[uint16][]byte),
	}
}

func (t *tunnel) Close() {
	t.privateServer.Stop()
	for port, con := range t.publicCons {
		con.Close()
		delete(t.publicCons, port)
		delete(t.initialBuffer, port)
	}
}

func (t *tunnel) Hostname() string {
	return t.hostname
}

func (t *tunnel) PrivateServerPort() uint16 {
	return t.privateServer.Port()
}

func (t *tunnel) publicConnectionHandler(publicCon net.Conn) error {
	ip := publicCon.RemoteAddr().(*net.TCPAddr).IP
	port := uint16(publicCon.RemoteAddr().(*net.TCPAddr).Port)

	t.eventWriterMx.Lock()
	defer t.eventWriterMx.Unlock()

	if len(t.publicCons) >= t.maxConsLimit {
		event := events.Event[events.ConnectionReceived]{
			Data: &events.ConnectionReceived{
				ClientIP:    ip,
				RateLimited: true,
			},
		}
		publicCon.Close()
		event.Write(t.eventWriter)
		return errors.New(fmt.Sprintf("[connections-limit-reached]: %s", t.hostname))
	}

	event := events.Event[events.ConnectionReceived]{
		Data: &events.ConnectionReceived{
			ClientIP:    ip,
			ClientPort:  port,
			RateLimited: false,
		},
	}
	if err := event.Write(t.eventWriter); err != nil {
		return publicCon.Close()
	}
	t.publicCons[port] = publicCon
	return nil
}

func (t *tunnel) privateConnectionHandler(privateCon net.Conn) error {
	defer privateCon.Close()
	buffer := make([]byte, 2)
	if _, err := privateCon.Read(buffer); err != nil {
		return err
	}

	port := binary.LittleEndian.Uint16(buffer)
	publicCon, found := t.publicCons[port]
	if !found {
		return errors.New("public connection not found, cannot pair")
	}

	defer publicCon.Close()
	delete(t.publicCons, port)
	defer delete(t.initialBuffer, port)

	if len(t.initialBuffer[port]) > 0 {
		if _, err := privateCon.Write(t.initialBuffer[port]); err != nil {
			return err
		}
	}

	go io.Copy(publicCon, privateCon)
	io.Copy(privateCon, publicCon)
	return nil
}