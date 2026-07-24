package mqtt5

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

type oneByteReader struct {
	reader io.Reader
}

func (r oneByteReader) Read(target []byte) (int, error) {
	if len(target) > 1 {
		target = target[:1]
	}
	return r.reader.Read(target)
}

func TestReadPacketHandlesTCPFragmentationAndCoalescing(t *testing.T) {
	firstBody := bytes.Repeat([]byte{0x5a}, 300)
	var wire bytes.Buffer
	if err := writePacket(&wire, 0x30, firstBody, DefaultMaximumPacketSize); err != nil {
		t.Fatal(err)
	}
	if err := writePacket(&wire, 0xd0, nil, DefaultMaximumPacketSize); err != nil {
		t.Fatal(err)
	}
	reader := oneByteReader{reader: bytes.NewReader(wire.Bytes())}
	first, err := readPacket(reader, DefaultMaximumPacketSize)
	if err != nil {
		t.Fatal(err)
	}
	second, err := readPacket(reader, DefaultMaximumPacketSize)
	if err != nil {
		t.Fatal(err)
	}
	if first.header != 0x30 || !bytes.Equal(first.body, firstBody) ||
		second.header != 0xd0 || len(second.body) != 0 {
		t.Fatalf("decoded packets = %#v / %#v", first, second)
	}
}

func TestRemainingLengthVarIntBoundariesAndMalformedForms(t *testing.T) {
	for _, value := range []int{0, 127, 128, 16383, 16384, maxRemainingLength} {
		encoded, err := encodeVarInt(value)
		if err != nil {
			t.Fatal(err)
		}
		decoded, read, err := readVarInt(bytes.NewReader(encoded))
		if err != nil || decoded != value || read != len(encoded) {
			t.Fatalf("%d -> %x -> %d/%d, err=%v", value, encoded, decoded, read, err)
		}
	}
	for _, malformed := range [][]byte{
		{0x80, 0x00},
		{0xff, 0xff, 0xff, 0xff, 0x01},
		{0x80},
	} {
		if _, _, err := readVarInt(bytes.NewReader(malformed)); err == nil {
			t.Fatalf("malformed varint %x unexpectedly passed", malformed)
		}
	}
}

func TestPacketSizeLimitRejectsInboundAndOutboundBeforePayloadIO(t *testing.T) {
	tooLarge := bytes.Repeat([]byte{0}, DefaultMaximumPacketSize)
	if err := writePacket(io.Discard, 0x30, tooLarge, DefaultMaximumPacketSize); err == nil {
		t.Fatal("oversized outbound packet unexpectedly passed")
	}
	remaining, _ := encodeVarInt(DefaultMaximumPacketSize)
	wire := append([]byte{0x30}, remaining...)
	if _, err := readPacket(bytes.NewReader(wire), DefaultMaximumPacketSize); err == nil {
		t.Fatal("oversized inbound packet unexpectedly passed")
	}
}

func TestConnackSubackAndPubackReasonAndProperties(t *testing.T) {
	connackPropertiesBody := []byte{0x13, 0x00, 0x1e, 0x1f, 0x00, 0x02, 'o', 'k'}
	connack := packet{header: 0x20, body: append([]byte{0x00, 0x00, byte(len(connackPropertiesBody))}, connackPropertiesBody...)}
	present, err := parseConnack(connack)
	if err != nil || present {
		t.Fatal(err)
	}
	resumed := connack
	resumed.body = append([]byte{}, connack.body...)
	resumed.body[0] = 0x01
	present, err = parseConnack(resumed)
	if err != nil || !present {
		t.Fatalf("resumed CONNACK sessionPresent=%v, err=%v", present, err)
	}
	rejected := connack
	rejected.body = append([]byte{}, connack.body...)
	rejected.body[1] = 0x87
	if _, err := parseConnack(rejected); err == nil {
		t.Fatal("negative CONNACK reason unexpectedly passed")
	}

	properties := []byte{0x1f, 0x00, 0x02, 'o', 'k'}
	subBody := binary.BigEndian.AppendUint16(nil, 7)
	subBody = append(subBody, byte(len(properties)))
	subBody = append(subBody, properties...)
	subBody = append(subBody, 0x01)
	if err := parseSuback(packet{header: 0x90, body: subBody}, 7); err != nil {
		t.Fatal(err)
	}
	subBody[len(subBody)-1] = 0x87
	if err := parseSuback(packet{header: 0x90, body: subBody}, 7); err == nil {
		t.Fatal("negative SUBACK reason unexpectedly passed")
	}

	pubBody := binary.BigEndian.AppendUint16(nil, 9)
	pubBody = append(pubBody, 0x00, byte(len(properties)))
	pubBody = append(pubBody, properties...)
	id, reason, err := parsePuback(packet{header: 0x40, body: pubBody})
	if err != nil || id != 9 || reason != 0 {
		t.Fatalf("PUBACK = id %d reason %x err %v", id, reason, err)
	}
	if _, _, err := parsePuback(packet{header: 0x40, body: []byte{0, 9, 0, 1, 0xff}}); err == nil {
		t.Fatal("unknown PUBACK property unexpectedly passed")
	}
}

func TestReadPacketReportsTruncation(t *testing.T) {
	_, err := readPacket(bytes.NewReader([]byte{0x30, 0x02, 0x01}), 1024)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated packet error = %v", err)
	}
}
