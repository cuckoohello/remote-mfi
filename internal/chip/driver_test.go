package chip

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/cuckoohello/remote-mfi-for-xcertplay/internal/transport"
)

func TestReadCertificateUsesIncrementing128ByteWindows(t *testing.T) {
	certificate := makeBytes(300, func(index int) byte { return byte(index) })
	i2c := newScriptedTransport(
		selectStep(0x30), readStep([]byte{1, 44}),
		selectStep(0x31), readStep(certificate[:128]),
		selectStep(0x32), readStep(certificate[128:256]),
		selectStep(0x33), readStep(certificate[256:]),
	)
	driver, err := NewDriver(i2c, 0x11)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}

	got, err := driver.ReadCertificate(context.Background())
	if err != nil {
		t.Fatalf("ReadCertificate: %v", err)
	}
	if !bytes.Equal(got, certificate) {
		t.Fatalf("certificate mismatch")
	}
	i2c.assertConsumed(t)
}

func TestSignChallengeUsesDocumentedRegisterSequence(t *testing.T) {
	challenge := makeBytes(20, func(index int) byte { return byte(index + 1) })
	signature := makeBytes(128, func(index int) byte { return byte(0xa0 + index) })
	i2c := newScriptedTransport(
		writeStep(0x20, []byte{0, 20}),
		writeStep(0x21, challenge),
		writeStep(0x10, []byte{1}),
		selectStep(0x10), readStep([]byte{0x10}),
		selectStep(0x11), readStep([]byte{0, 128}),
		selectStep(0x12), readStep(signature),
	)
	driver, err := NewDriver(i2c, 0x11)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}

	got, err := driver.SignChallenge(context.Background(), challenge)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}
	if !bytes.Equal(got, signature) {
		t.Fatalf("signature mismatch")
	}
	i2c.assertConsumed(t)
}

func TestProtocolMajorRetriesWholeSelectReadPair(t *testing.T) {
	i2c := newScriptedTransport(
		scriptStep{write: []byte{0x02}, err: &transport.Error{Kind: transport.ErrorNACK, Operation: "test"}},
		selectStep(0x02),
		readStep([]byte{3}),
	)
	driver, err := NewDriver(i2c, 0x11)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}

	got, err := driver.ProtocolMajor(context.Background())
	if err != nil {
		t.Fatalf("ProtocolMajor: %v", err)
	}
	if got != 3 {
		t.Fatalf("protocol major = %d, want 3", got)
	}
	i2c.assertConsumed(t)
}

type scriptStep struct {
	write    []byte
	readLen  int
	response []byte
	err      error
}

type scriptedTransport struct {
	mu    sync.Mutex
	steps []scriptStep
}

func newScriptedTransport(steps ...scriptStep) *scriptedTransport {
	return &scriptedTransport{steps: append([]scriptStep(nil), steps...)}
}

func (t *scriptedTransport) Transaction(address uint8, writeData []byte, readLength int) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.steps) == 0 {
		return nil, fmt.Errorf("unexpected transaction: address=0x%02x write=%x read=%d", address, writeData, readLength)
	}
	step := t.steps[0]
	t.steps = t.steps[1:]
	if address != 0x11 {
		return nil, fmt.Errorf("address=0x%02x, want 0x11", address)
	}
	if !bytes.Equal(writeData, step.write) {
		return nil, fmt.Errorf("write=%x, want %x", writeData, step.write)
	}
	if readLength != step.readLen {
		return nil, fmt.Errorf("read=%d, want %d", readLength, step.readLen)
	}
	return append([]byte(nil), step.response...), step.err
}

func (t *scriptedTransport) Close() error {
	return nil
}

func (t *scriptedTransport) assertConsumed(tb testing.TB) {
	tb.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.steps) != 0 {
		tb.Fatalf("%d scripted transactions remain", len(t.steps))
	}
}

func selectStep(register byte) scriptStep {
	return scriptStep{write: []byte{register}}
}

func readStep(response []byte) scriptStep {
	return scriptStep{readLen: len(response), response: append([]byte(nil), response...)}
}

func writeStep(register byte, payload []byte) scriptStep {
	request := append([]byte{register}, payload...)
	return scriptStep{write: request}
}

func makeBytes(length int, value func(int) byte) []byte {
	result := make([]byte, length)
	for index := range result {
		result[index] = value(index)
	}
	return result
}
