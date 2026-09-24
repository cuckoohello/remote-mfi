package transport

import (
	"bytes"
	"testing"

	"github.com/google/gousb"
)

func TestEncodeWriteReadWithRepeatedStartAndFinalReadNACK(t *testing.T) {
	got, err := encodeTransaction(0x11, []byte{0x30}, 2)
	if err != nil {
		t.Fatalf("encodeTransaction: %v", err)
	}
	want := []byte{0xaa, 0x74, 0x80, 0x22, 0x80, 0x30, 0x74, 0x80, 0x23, 0xc1, 0xc0, 0x75, 0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("stream mismatch\n got: % x\nwant: % x", got, want)
	}
}

func TestEncodeMaximumPureReadIsSegmentedAndStopsOnce(t *testing.T) {
	stream, err := encodeTransaction(0x11, nil, maxTransactionBytes)
	if err != nil {
		t.Fatalf("encodeTransaction: %v", err)
	}
	if got := bytes.Count(stream, []byte{streamI2CStop}); got != 1 {
		t.Fatalf("STOP count = %d, want 1", got)
	}
	if !bytes.Equal(stream[:4], []byte{0xaa, 0x74, 0x81, 0x23}) {
		t.Fatalf("prefix = % x", stream[:4])
	}
	if !bytes.Equal(stream[len(stream)-4:], []byte{0xde, 0xc0, 0x75, 0x00}) {
		t.Fatalf("suffix = % x", stream[len(stream)-4:])
	}
	for offset := 0; offset < len(stream)-maxStreamPacketBytes; offset += maxStreamPacketBytes {
		if stream[offset] != streamStart {
			t.Fatalf("segment at %d starts with 0x%02x", offset, stream[offset])
		}
		if stream[offset+maxStreamPacketBytes-1] != streamEnd {
			t.Fatalf("segment at %d does not end with stream marker", offset)
		}
	}
}

func TestEncodeRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name      string
		address   uint8
		writeData []byte
		readLen   int
	}{
		{name: "empty", address: 0x11},
		{name: "address", address: 0x80, writeData: []byte{1}},
		{name: "negative read", address: 0x11, readLen: -1},
		{name: "oversize read", address: 0x11, readLen: maxTransactionBytes + 1},
		{name: "oversize write", address: 0x11, writeData: make([]byte, maxTransactionBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := encodeTransaction(test.address, test.writeData, test.readLen); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestInstructionLengths(t *testing.T) {
	segment := []byte{0xaa, 0x74, 0x80, 0x22, 0x80, 0x30, 0x74, 0x81, 0x23, 0xc1, 0xc0, 0x75, 0x00}
	written, read, err := instructionLengths(segment)
	if err != nil {
		t.Fatalf("instructionLengths: %v", err)
	}
	if written != 2 || read != 2 {
		t.Fatalf("got written=%d read=%d, want 2/2", written, read)
	}
}

func TestCancelledUSBTransferIsClassifiedAsTimeout(t *testing.T) {
	err := classifyUSBError("test", gousb.TransferCancelled)
	if !IsKind(err, ErrorTimeout) {
		t.Fatalf("kind = %v, want timeout", err)
	}
	if !isTimeout(gousb.TransferCancelled) {
		t.Fatal("TransferCancelled should end an allowed quiet read")
	}
}
