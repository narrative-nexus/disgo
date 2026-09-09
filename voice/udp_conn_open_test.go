package voice

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
)

// newUnopenedTestConn builds a udpConnImpl in the state NewConn leaves it in:
// constructed, but with no net.Conn, because Open only runs once the voice
// gateway has reached Ready and handed over the UDP endpoint.
func newUnopenedTestConn() *udpConnImpl {
	return &udpConnImpl{
		config:        udpConnConfig{Logger: slog.New(slog.DiscardHandler)},
		encrypter:     passthroughEncrypter{},
		daveSession:   godave.NewNoopSession(slog.New(slog.DiscardHandler), "1", nil),
		ssrcLookup:    func(uint32) snowflake.ID { return snowflake.ID(1) },
		receiveBuffer: make([]byte, 1400),
		decryptBuffer: make([]byte, 1400),
	}
}

// Conn.SetOpusFrameReceiver starts the audio receiver immediately, and that
// goroutine calls ReadPacket. Setting a receiver before Conn.Open has completed
// therefore reached ReadPacket with a nil net.Conn and took the whole process
// down with a nil-pointer dereference on a goroutine the caller cannot recover
// from. It must report an error instead.
func TestReadPacketOnUnopenedConn(t *testing.T) {
	u := newUnopenedTestConn()

	packet, err := u.ReadPacket()
	if err == nil {
		t.Fatalf("ReadPacket on an unopened conn returned a packet (%v), want an error", packet)
	}
	if !errors.Is(err, ErrUDPConnNotOpen) {
		t.Errorf("ReadPacket error = %v, want ErrUDPConnNotOpen", err)
	}
}

// The sender path has the same shape: SetOpusFrameProvider starts a goroutine
// that writes to the same conn.
func TestWriteOnUnopenedConn(t *testing.T) {
	u := newUnopenedTestConn()

	n, err := u.Write([]byte{0xF8, 0xFF, 0xFE})
	if err == nil {
		t.Fatalf("Write on an unopened conn wrote %d bytes, want an error", n)
	}
	if !errors.Is(err, ErrUDPConnNotOpen) {
		t.Errorf("Write error = %v, want ErrUDPConnNotOpen", err)
	}
}

// An unopened conn is not a closed one: the receiver must idle until the
// handshake completes rather than tear itself down, or a caller that attaches a
// receiver before Open gets silence instead of audio.
func TestUnopenedConnIsNotReportedAsClosed(t *testing.T) {
	u := newUnopenedTestConn()

	_, err := u.ReadPacket()
	if errors.Is(err, net.ErrClosed) {
		t.Error("ReadPacket on an unopened conn reported net.ErrClosed, which stops the audio receiver for good")
	}
}

// noopOpusReceiver is the smallest possible OpusFrameReceiver.
type noopOpusReceiver struct{}

func (noopOpusReceiver) ReceiveOpusFrame(snowflake.ID, *Packet) error { return nil }
func (noopOpusReceiver) CleanupUser(snowflake.ID)                     {}
func (noopOpusReceiver) Close()                                       {}

// noopOpusProvider is the smallest possible OpusFrameProvider.
type noopOpusProvider struct{}

func (noopOpusProvider) ProvideOpusFrame() ([]byte, error) { return nil, nil }
func (noopOpusProvider) Close()                            {}

// This is the reproducer that motivated the guards above, in full. NewConn
// builds the Gateway and the UDPConn eagerly, so a Conn whose handshake has not
// run is reachable with no credentials and no network. Attaching a receiver or a
// provider there starts a goroutine the library owns, and the caller cannot
// recover from a panic on it: an unguarded read or write took the whole process
// down.
func TestSetOpusFrameHandlersBeforeOpen(t *testing.T) {
	conn := NewConn(1, 2,
		func(context.Context, snowflake.ID, *snowflake.ID, bool, bool) error { return nil },
		func() {},
		WithConnLogger(slog.New(slog.DiscardHandler)),
		WithConnDaveSessionLogger(slog.New(slog.DiscardHandler)),
	)

	conn.SetOpusFrameReceiver(noopOpusReceiver{})
	conn.SetOpusFrameProvider(noopOpusProvider{})

	// The goroutines are the library's, so give the scheduler a chance to run
	// them. A regression crashes the test binary here rather than failing.
	time.Sleep(100 * time.Millisecond)
}
