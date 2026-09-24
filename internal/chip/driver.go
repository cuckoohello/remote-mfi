package chip

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/cuckoohello/remote-mfi/internal/transport"
)

const (
	protocolMajorRegister     = 0x02
	errorRegister             = 0x05
	authControlStatusRegister = 0x10
	responseLengthRegister    = 0x11
	responseDataRegister      = 0x12
	challengeLengthRegister   = 0x20
	challengeDataRegister     = 0x21
	certificateLengthRegister = 0x30
	certificateDataRegister   = 0x31
	certificateWindowBytes    = 128
	authStartCommand          = 0x01
	authSuccessStatus         = 0x10
	maximumDataBytes          = 65_525
	maximumChallengeBytes     = 128
	ioRetryTimeout            = 2 * time.Second
	ioRetryDelay              = 20 * time.Millisecond
	initialAuthDelay          = 10 * time.Millisecond
	authPollInterval          = 10 * time.Millisecond
	authTimeout               = 3 * time.Second
)

type ErrorKind string

const (
	ErrorInvalidData ErrorKind = "invalid_data"
	ErrorAuthFailed  ErrorKind = "auth_failed"
)

type Error struct {
	Kind      ErrorKind
	Operation string
	Code      *uint8
	Err       error
}

func (e *Error) Error() string {
	if e.Code != nil {
		return fmt.Sprintf("%s failed with MFi error 0x%02x", e.Operation, *e.Code)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Operation, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Operation, e.Kind)
}

func (e *Error) Unwrap() error {
	return e.Err
}

func IsKind(err error, kind ErrorKind) bool {
	var chipError *Error
	return errors.As(err, &chipError) && chipError.Kind == kind
}

type Driver struct {
	transport   transport.I2CTransport
	address7Bit uint8
}

func NewDriver(i2cTransport transport.I2CTransport, address7Bit uint8) (*Driver, error) {
	if i2cTransport == nil {
		return nil, fmt.Errorf("I2C transport is required")
	}
	if address7Bit > 0x7f {
		return nil, fmt.Errorf("I2C address must be a 7-bit value")
	}
	return &Driver{transport: i2cTransport, address7Bit: address7Bit}, nil
}

func (d *Driver) ProtocolMajor(ctx context.Context) (uint8, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	value, err := d.readByte(protocolMajorRegister)
	return uint8(value), err
}

func (d *Driver) ReadCertificate(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	length, err := d.readUnsignedBigEndianShort(certificateLengthRegister)
	if err != nil {
		return nil, err
	}
	if length < 1 || length > maximumDataBytes {
		return nil, &Error{
			Kind:      ErrorInvalidData,
			Operation: "read certificate",
			Err:       fmt.Errorf("certificate length %d is outside 1..%d", length, maximumDataBytes),
		}
	}

	certificate := make([]byte, length)
	for offset, register := 0, certificateDataRegister; offset < length; register++ {
		count := min(certificateWindowBytes, length-offset)
		window, err := d.readRegister(register, count)
		if err != nil {
			return nil, err
		}
		copy(certificate[offset:], window)
		offset += count
	}
	return certificate, nil
}

func (d *Driver) SignChallenge(ctx context.Context, challenge []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(challenge) < 1 || len(challenge) > maximumChallengeBytes {
		return nil, &Error{
			Kind:      ErrorInvalidData,
			Operation: "sign challenge",
			Err:       fmt.Errorf("challenge must be 1..%d bytes", maximumChallengeBytes),
		}
	}

	if err := d.writeUnsignedBigEndianShort(challengeLengthRegister, len(challenge)); err != nil {
		return nil, err
	}
	if err := d.writeRegister(challengeDataRegister, append([]byte(nil), challenge...)); err != nil {
		return nil, err
	}
	if err := d.writeRegister(authControlStatusRegister, []byte{authStartCommand}); err != nil {
		return nil, err
	}

	time.Sleep(initialAuthDelay)
	deadline := time.Now().Add(authTimeout)
	for {
		status, err := d.readByte(authControlStatusRegister)
		if err == nil && status == authSuccessStatus {
			break
		}
		if time.Now().After(deadline) {
			code := d.bestEffortErrorCode()
			return nil, &Error{
				Kind:      ErrorAuthFailed,
				Operation: "MFi authentication",
				Code:      code,
				Err:       fmt.Errorf("timed out after %s", authTimeout),
			}
		}
		time.Sleep(authPollInterval)
	}

	length, err := d.readUnsignedBigEndianShort(responseLengthRegister)
	if err != nil {
		return nil, err
	}
	if length < 1 || length > maximumDataBytes {
		return nil, &Error{
			Kind:      ErrorInvalidData,
			Operation: "read signature",
			Err:       fmt.Errorf("signature length %d is outside 1..%d", length, maximumDataBytes),
		}
	}
	return d.readRegister(responseDataRegister, length)
}

func (d *Driver) bestEffortErrorCode() *uint8 {
	if err := d.selectRegister(errorRegister); err != nil {
		return nil
	}
	result, err := d.transport.Transaction(d.address7Bit, nil, 1)
	if err != nil || len(result) != 1 {
		return nil
	}
	value := result[0]
	return &value
}

func (d *Driver) readUnsignedBigEndianShort(register int) (int, error) {
	data, err := d.readRegister(register, 2)
	if err != nil {
		return 0, err
	}
	return int(binary.BigEndian.Uint16(data)), nil
}

func (d *Driver) writeUnsignedBigEndianShort(register, value int) error {
	if value < 0 || value > 0xffff {
		return &Error{Kind: ErrorInvalidData, Operation: "write length", Err: fmt.Errorf("value %d is outside 0..65535", value)}
	}
	data := []byte{byte(value >> 8), byte(value)}
	return d.writeRegister(register, data)
}

func (d *Driver) readByte(register int) (int, error) {
	data, err := d.readRegister(register, 1)
	if err != nil {
		return 0, err
	}
	return int(data[0]), nil
}

func (d *Driver) readRegister(register, length int) ([]byte, error) {
	if length < 1 || length > maximumDataBytes {
		return nil, &Error{Kind: ErrorInvalidData, Operation: "read register", Err: fmt.Errorf("length %d is outside 1..%d", length, maximumDataBytes)}
	}
	return retryIO(fmt.Sprintf("read register 0x%02x", register&0xff), func() ([]byte, error) {
		if err := d.selectRegisterOnce(register); err != nil {
			return nil, err
		}
		result, err := d.transport.Transaction(d.address7Bit, nil, length)
		if err != nil {
			return nil, err
		}
		if len(result) != length {
			return nil, &Error{
				Kind:      ErrorInvalidData,
				Operation: fmt.Sprintf("read register 0x%02x", register&0xff),
				Err:       fmt.Errorf("returned %d bytes; expected %d", len(result), length),
			}
		}
		return append([]byte(nil), result...), nil
	})
}

func (d *Driver) selectRegister(register int) error {
	_, err := retryIO(fmt.Sprintf("select register 0x%02x", register&0xff), func() (struct{}, error) {
		return struct{}{}, d.selectRegisterOnce(register)
	})
	return err
}

func (d *Driver) selectRegisterOnce(register int) error {
	result, err := d.transport.Transaction(d.address7Bit, []byte{byte(register)}, 0)
	if err != nil {
		return err
	}
	if len(result) != 0 {
		return &Error{
			Kind:      ErrorInvalidData,
			Operation: fmt.Sprintf("select register 0x%02x", register&0xff),
			Err:       fmt.Errorf("returned unexpected data"),
		}
	}
	return nil
}

func (d *Driver) writeRegister(register int, data []byte) error {
	request := make([]byte, len(data)+1)
	request[0] = byte(register)
	copy(request[1:], data)
	_, err := retryIO(fmt.Sprintf("write register 0x%02x", register&0xff), func() (struct{}, error) {
		result, err := d.transport.Transaction(d.address7Bit, request, 0)
		if err != nil {
			return struct{}{}, err
		}
		if len(result) != 0 {
			return struct{}{}, &Error{
				Kind:      ErrorInvalidData,
				Operation: fmt.Sprintf("write register 0x%02x", register&0xff),
				Err:       fmt.Errorf("returned unexpected data"),
			}
		}
		return struct{}{}, nil
	})
	return err
}

func retryIO[T any](operation string, action func() (T, error)) (T, error) {
	deadline := time.Now().Add(ioRetryTimeout)
	var zero T
	var lastError error
	for {
		result, err := action()
		if err == nil {
			return result, nil
		}
		lastError = err
		if !isRetryableTransportError(err) || time.Now().After(deadline) {
			return zero, fmt.Errorf("%s: %w", operation, lastError)
		}
		time.Sleep(ioRetryDelay)
	}
}

func isRetryableTransportError(err error) bool {
	var transportError *transport.Error
	return errors.As(err, &transportError)
}
