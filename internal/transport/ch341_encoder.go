package transport

import "fmt"

const (
	maxStreamPacketBytes    = 32
	maxTransactionBytes     = 0xffff
	maxReadBlockBytes       = 32
	maxWriteCommandBytes    = 0x3f
	streamStart             = 0xaa
	streamEnd               = 0x00
	streamI2CStart          = 0x74
	streamI2CStop           = 0x75
	streamWrite             = 0x80
	streamRead              = 0xc0
	streamEndBytes          = 1
	streamStopAndEndBytes   = 2
	streamStartAddressBytes = 3
)

type I2CSpeed int

const (
	I2CSpeed20KHz  I2CSpeed = 20
	I2CSpeed100KHz I2CSpeed = 100
	I2CSpeed400KHz I2CSpeed = 400
	I2CSpeed750KHz I2CSpeed = 750
)

func (s I2CSpeed) command() (byte, error) {
	switch s {
	case I2CSpeed20KHz:
		return 0x60, nil
	case I2CSpeed100KHz:
		return 0x61, nil
	case I2CSpeed400KHz:
		return 0x62, nil
	case I2CSpeed750KHz:
		return 0x63, nil
	default:
		return 0, fmt.Errorf("unsupported CH341 I2C speed %d kHz", s)
	}
}

func encodeConfiguration(speed I2CSpeed) ([]byte, error) {
	command, err := speed.command()
	if err != nil {
		return nil, err
	}
	return []byte{streamStart, command, streamEnd}, nil
}

func encodeTransaction(address7Bit uint8, writeData []byte, readLength int) ([]byte, error) {
	if address7Bit > 0x7f {
		return nil, &Error{Kind: ErrorInvalidRequest, Operation: "encode transaction", Err: fmt.Errorf("I2C address must be a 7-bit value")}
	}
	if readLength < 0 {
		return nil, &Error{Kind: ErrorInvalidRequest, Operation: "encode transaction", Err: fmt.Errorf("read length must not be negative")}
	}
	if len(writeData) == 0 && readLength == 0 {
		return nil, &Error{Kind: ErrorInvalidRequest, Operation: "encode transaction", Err: fmt.Errorf("transaction must read or write data")}
	}
	if len(writeData) > maxTransactionBytes || readLength > maxTransactionBytes {
		return nil, &Error{Kind: ErrorInvalidRequest, Operation: "encode transaction", Err: fmt.Errorf("transaction supports at most %d bytes per read or write", maxTransactionBytes)}
	}
	if legacy := encodeLegacyTransaction(address7Bit, writeData, readLength); legacy != nil {
		return legacy, nil
	}

	stream := newSegmentedStream()
	if len(writeData) > 0 {
		stream.startAndWrite(addressByte(address7Bit, false), writeData)
	}
	if readLength > 0 {
		stream.startAndWrite(addressByte(address7Bit, true), nil)
		stream.read(readLength)
	}
	return stream.finish(), nil
}

func encodeLegacyTransaction(address7Bit uint8, writeData []byte, readLength int) []byte {
	if readLength > maxReadBlockBytes || len(writeData)+1 > maxWriteCommandBytes {
		return nil
	}

	bytes := make([]byte, 0, maxStreamPacketBytes)
	write := func(address byte, data []byte) {
		bytes = append(bytes, streamWrite, address)
		for _, value := range data {
			bytes = append(bytes, streamWrite, value)
		}
	}

	bytes = append(bytes, streamStart)
	if len(writeData) > 0 {
		bytes = append(bytes, streamI2CStart)
		write(addressByte(address7Bit, false), writeData)
	}
	if readLength > 0 {
		bytes = append(bytes, streamI2CStart)
		write(addressByte(address7Bit, true), nil)
		if readLength > 1 {
			bytes = append(bytes, byte(streamRead+readLength-1))
		}
		bytes = append(bytes, streamRead)
	}
	bytes = append(bytes, streamI2CStop, streamEnd)
	if len(bytes) > maxStreamPacketBytes {
		return nil
	}
	return bytes
}

func addressByte(address7Bit uint8, read bool) byte {
	value := address7Bit << 1
	if read {
		value |= 1
	}
	return value
}

type segmentedStream struct {
	result  []byte
	segment []byte
}

func newSegmentedStream() *segmentedStream {
	return &segmentedStream{segment: []byte{streamStart}}
}

func (s *segmentedStream) startAndWrite(address byte, data []byte) {
	if len(s.segment)+streamStartAddressBytes+streamEndBytes > maxStreamPacketBytes {
		s.finishIntermediate()
	}
	s.segment = append(s.segment, streamI2CStart)
	s.write(address, data)
}

func (s *segmentedStream) write(address byte, data []byte) {
	if len(data) == 0 {
		s.command([]byte{streamWrite + 1, address})
		return
	}
	s.command([]byte{streamWrite, address})
	for _, value := range data {
		s.command([]byte{streamWrite, value})
	}
}

func (s *segmentedStream) read(length int) {
	remaining := length
	for remaining > maxReadBlockBytes {
		s.command([]byte{streamRead + maxReadBlockBytes})
		s.finishIntermediate()
		remaining -= maxReadBlockBytes
	}
	if remaining > 1 {
		s.command([]byte{byte(streamRead + remaining - 1)})
	}
	s.command([]byte{streamRead})
}

func (s *segmentedStream) command(command []byte) {
	if len(s.segment)+len(command)+streamEndBytes > maxStreamPacketBytes {
		s.finishIntermediate()
	}
	s.segment = append(s.segment, command...)
}

func (s *segmentedStream) finishIntermediate() {
	s.segment = append(s.segment, streamEnd)
	for len(s.segment) < maxStreamPacketBytes {
		s.segment = append(s.segment, 0)
	}
	s.result = append(s.result, s.segment...)
	s.segment = []byte{streamStart}
}

func (s *segmentedStream) finish() []byte {
	if len(s.segment)+streamStopAndEndBytes > maxStreamPacketBytes {
		s.finishIntermediate()
	}
	s.segment = append(s.segment, streamI2CStop, streamEnd)
	s.result = append(s.result, s.segment...)
	return s.result
}
