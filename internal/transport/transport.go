package transport

import (
	"errors"
	"fmt"
)

type ErrorKind string

const (
	ErrorInvalidRequest ErrorKind = "invalid_request"
	ErrorDeviceMissing  ErrorKind = "device_missing"
	ErrorPermission     ErrorKind = "permission_denied"
	ErrorBusy           ErrorKind = "device_busy"
	ErrorTimeout        ErrorKind = "timeout"
	ErrorNACK           ErrorKind = "nack"
	ErrorProtocol       ErrorKind = "protocol"
	ErrorIO             ErrorKind = "io"
)

type Error struct {
	Kind      ErrorKind
	Operation string
	Err       error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s: %s", e.Operation, e.Kind)
	}
	return fmt.Sprintf("%s: %v", e.Operation, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

func IsKind(err error, kind ErrorKind) bool {
	var transportError *Error
	return errors.As(err, &transportError) && transportError.Kind == kind
}

type I2CTransport interface {
	Transaction(address7Bit uint8, writeData []byte, readLength int) ([]byte, error)
	Close() error
}

type USBDeviceInfo struct {
	Bus          int    `json:"bus"`
	Device       int    `json:"device"`
	Vendor       string `json:"vid"`
	ProductID    string `json:"pid"`
	Manufacturer string `json:"manufacturer"`
	Product      string `json:"product"`
	Speed        string `json:"speed"`
	Class        int    `json:"class"`
	Candidate    bool   `json:"candidate"`
}

type USBStatus struct {
	State   string
	Reason  string
	VIDPID  string
	Devices []USBDeviceInfo
}

type USBInspector interface {
	InspectUSB(includeDeviceStrings bool) USBStatus
}
