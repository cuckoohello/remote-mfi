package transport

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/gousb"

	"github.com/cuckoohello/remote-mfi-for-xcertplay/internal/config"
)

const (
	defaultTransferTimeout = time.Second
	responseQuietTimeout   = 25 * time.Millisecond
	selectToReadGap        = 5 * time.Millisecond
	maxUSBPacketBytes      = 32
	lengthMask             = 0x3f
	ackBit                 = 0x80
)

type Ch341Transport struct {
	context *gousb.Context
	usbIDs  []config.USBID
	speed   I2CSpeed

	ioMu      sync.Mutex
	sessionMu sync.Mutex
	session   *ch341Session
	closed    bool
}

type ch341Session struct {
	device   *gousb.Device
	config   *gousb.Config
	iface    *gousb.Interface
	input    *gousb.InEndpoint
	output   *gousb.OutEndpoint
	identity string
}

func NewCh341(usbIDs []config.USBID, speed I2CSpeed) (*Ch341Transport, error) {
	if len(usbIDs) == 0 {
		return nil, fmt.Errorf("at least one CH341 USB identity is required")
	}
	if _, err := speed.command(); err != nil {
		return nil, err
	}
	return &Ch341Transport{
		context: gousb.NewContext(),
		usbIDs:  append([]config.USBID(nil), usbIDs...),
		speed:   speed,
	}, nil
}

func (t *Ch341Transport) Transaction(address7Bit uint8, writeData []byte, readLength int) ([]byte, error) {
	stream, err := encodeTransaction(address7Bit, writeData, readLength)
	if err != nil {
		return nil, err
	}

	t.ioMu.Lock()
	defer t.ioMu.Unlock()

	session, err := t.currentSession()
	if err != nil {
		return nil, err
	}
	response, err := t.execute(session, stream, len(writeData), readLength)
	if err != nil && shouldInvalidateSession(err) {
		t.invalidateSession(session)
	}
	return response, err
}

func (t *Ch341Transport) InspectUSB(includeDeviceStrings bool) USBStatus {
	devices, candidateFound := t.enumerateUSB(includeDeviceStrings)
	if !candidateFound {
		return USBStatus{
			State:   "missing",
			Reason:  "no matching USB device",
			Devices: devices,
		}
	}

	t.sessionMu.Lock()
	defer t.sessionMu.Unlock()
	if t.closed {
		return USBStatus{State: "error", Reason: "USB context is closed", Devices: devices}
	}
	session, err := t.ensureSessionLocked()
	if err != nil {
		return USBStatus{
			State:   "error",
			Reason:  err.Error(),
			Devices: devices,
		}
	}
	return USBStatus{
		State:   "ready",
		VIDPID:  session.identity,
		Devices: devices,
	}
}

func (t *Ch341Transport) Close() error {
	t.ioMu.Lock()
	defer t.ioMu.Unlock()
	t.sessionMu.Lock()
	defer t.sessionMu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	var closeError error
	if t.session != nil {
		closeError = t.session.close()
		t.session = nil
	}
	if err := t.context.Close(); closeError == nil {
		closeError = err
	}
	return closeError
}

func (t *Ch341Transport) currentSession() (*ch341Session, error) {
	t.sessionMu.Lock()
	defer t.sessionMu.Unlock()
	if t.closed {
		return nil, &Error{Kind: ErrorDeviceMissing, Operation: "open CH341", Err: fmt.Errorf("USB context is closed")}
	}
	return t.ensureSessionLocked()
}

func (t *Ch341Transport) ensureSessionLocked() (*ch341Session, error) {
	if t.session != nil {
		return t.session, nil
	}
	var lastError error
	for _, id := range t.usbIDs {
		device, err := t.context.OpenDeviceWithVIDPID(gousb.ID(id.Vendor), gousb.ID(id.Product))
		if err != nil {
			lastError = classifyUSBError("open CH341 "+id.String(), err)
			continue
		}
		if device == nil {
			continue
		}
		session, err := openSession(device, id.String())
		if err != nil {
			_ = device.Close()
			lastError = err
			continue
		}
		t.session = session
		return session, nil
	}
	if lastError != nil {
		return nil, lastError
	}
	return nil, &Error{Kind: ErrorDeviceMissing, Operation: "open CH341", Err: fmt.Errorf("no matching USB device")}
}

func openSession(device *gousb.Device, identity string) (*ch341Session, error) {
	configNumber, interfaceNumber, alternate, inputNumber, outputNumber, ok := findBulkEndpoints(device.Desc)
	if !ok {
		return nil, &Error{Kind: ErrorProtocol, Operation: "open CH341", Err: fmt.Errorf("no USB interface has both bulk IN and OUT endpoints")}
	}

	usbConfig, err := device.Config(configNumber)
	if err != nil {
		return nil, classifyUSBError("claim CH341 configuration", err)
	}
	iface, err := usbConfig.Interface(interfaceNumber, alternate)
	if err != nil {
		_ = usbConfig.Close()
		return nil, classifyUSBError("claim CH341 interface", err)
	}
	input, err := iface.InEndpoint(inputNumber)
	if err != nil {
		iface.Close()
		_ = usbConfig.Close()
		return nil, &Error{Kind: ErrorProtocol, Operation: "open CH341 input endpoint", Err: err}
	}
	output, err := iface.OutEndpoint(outputNumber)
	if err != nil {
		iface.Close()
		_ = usbConfig.Close()
		return nil, &Error{Kind: ErrorProtocol, Operation: "open CH341 output endpoint", Err: err}
	}
	return &ch341Session{
		device:   device,
		config:   usbConfig,
		iface:    iface,
		input:    input,
		output:   output,
		identity: identity,
	}, nil
}

func findBulkEndpoints(desc *gousb.DeviceDesc) (
	configNumber int,
	interfaceNumber int,
	alternate int,
	inputNumber int,
	outputNumber int,
	ok bool,
) {
	configNumbers := make([]int, 0, len(desc.Configs))
	for number := range desc.Configs {
		configNumbers = append(configNumbers, number)
	}
	sort.Ints(configNumbers)
	for _, number := range configNumbers {
		usbConfig := desc.Configs[number]
		for _, interfaceDesc := range usbConfig.Interfaces {
			for _, setting := range interfaceDesc.AltSettings {
				input, output := -1, -1
				for _, endpoint := range setting.Endpoints {
					if endpoint.TransferType != gousb.TransferTypeBulk {
						continue
					}
					switch endpoint.Direction {
					case gousb.EndpointDirectionIn:
						if input < 0 {
							input = endpoint.Number
						}
					case gousb.EndpointDirectionOut:
						if output < 0 {
							output = endpoint.Number
						}
					}
				}
				if input >= 0 && output >= 0 {
					return number, setting.Number, setting.Alternate, input, output, true
				}
			}
		}
	}
	return 0, 0, 0, 0, 0, false
}

func (t *Ch341Transport) execute(session *ch341Session, stream []byte, writeLength, readLength int) ([]byte, error) {
	configuration, err := encodeConfiguration(t.speed)
	if err != nil {
		return nil, err
	}
	timeout := minimumTransferTimeout(writeLength, readLength, t.speed)
	if err := bulkWrite(session.output, configuration, timeout); err != nil {
		return nil, err
	}
	if writeLength == 0 && readLength > 0 {
		time.Sleep(selectToReadGap)
	}

	response := make([]byte, 0, readLength)
	for offset := 0; offset < len(stream); {
		end := min(offset+maxStreamPacketBytes, len(stream))
		segment := stream[offset:end]
		if err := bulkWrite(session.output, segment, timeout); err != nil {
			return nil, err
		}
		writtenBytes, dataBytes, err := instructionLengths(segment)
		if err != nil {
			return nil, err
		}
		if writtenBytes+dataBytes > 0 {
			firstTimeout := responseQuietTimeout
			if dataBytes > 0 {
				firstTimeout = timeout
			}
			received, err := bulkReadAtMost(
				session.input,
				writtenBytes+dataBytes,
				firstTimeout,
				responseQuietTimeout,
				dataBytes == 0,
			)
			if err != nil {
				return nil, err
			}
			if len(received) < dataBytes {
				return nil, &Error{
					Kind:      ErrorDeviceMissing,
					Operation: "read CH341 stream response",
					Err:       fmt.Errorf("answered %d bytes; expected %d data bytes", len(received), dataBytes),
				}
			}
			dataStart := len(received) - dataBytes
			if err := checkStatusBytes(received[:dataStart]); err != nil {
				return nil, err
			}
			response = append(response, received[dataStart:]...)
		}
		offset = end
	}
	if len(response) != readLength {
		return nil, &Error{
			Kind:      ErrorProtocol,
			Operation: "CH341 transaction",
			Err:       fmt.Errorf("returned %d bytes; expected %d", len(response), readLength),
		}
	}
	return response, nil
}

func bulkWrite(endpoint *gousb.OutEndpoint, data []byte, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	transferred, err := endpoint.WriteContext(ctx, data)
	if err != nil {
		return classifyUSBError("CH341 bulk write", err)
	}
	if transferred != len(data) {
		return &Error{
			Kind:      ErrorProtocol,
			Operation: "CH341 bulk write",
			Err:       fmt.Errorf("transferred %d of %d bytes", transferred, len(data)),
		}
	}
	return nil
}

func bulkReadAtMost(
	endpoint *gousb.InEndpoint,
	maxLength int,
	firstPacketTimeout time.Duration,
	quietTimeout time.Duration,
	allowEmpty bool,
) ([]byte, error) {
	if maxLength == 0 {
		return nil, nil
	}
	packetSize := min(max(endpoint.Desc.MaxPacketSize, 1), maxUSBPacketBytes)
	data := make([]byte, 0, maxLength)
	for len(data) < maxLength {
		buffer := make([]byte, min(maxLength-len(data), packetSize))
		timeout := quietTimeout
		if len(data) == 0 {
			timeout = firstPacketTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		transferred, err := endpoint.ReadContext(ctx, buffer)
		cancel()
		if err != nil {
			if (len(data) > 0 || allowEmpty) && isTimeout(err) {
				break
			}
			return nil, classifyUSBError("CH341 bulk read", err)
		}
		if transferred <= 0 {
			if len(data) > 0 || allowEmpty {
				break
			}
			return nil, &Error{Kind: ErrorDeviceMissing, Operation: "CH341 bulk read", Err: fmt.Errorf("returned no data")}
		}
		data = append(data, buffer[:transferred]...)
	}
	return data, nil
}

func instructionLengths(segment []byte) (writtenBytes int, dataBytes int, err error) {
	for index := 1; index < len(segment); {
		command := int(segment[index])
		index++
		switch {
		case command == streamEnd:
			return writtenBytes, dataBytes, nil
		case command == streamI2CStart || command == streamI2CStop:
		case command >= 0x80 && command <= 0xbf:
			written := encodedLength(command)
			if command&lengthMask == 0 {
				writtenBytes += written
			}
			index += written
		case command >= 0xc0 && command <= 0xff:
			dataBytes += encodedLength(command)
		case command >= 0x60 && command <= 0x63:
		default:
			return 0, 0, &Error{
				Kind:      ErrorProtocol,
				Operation: "decode CH341 stream",
				Err:       fmt.Errorf("unexpected command 0x%02x", command),
			}
		}
		if index > len(segment) {
			return 0, 0, &Error{Kind: ErrorProtocol, Operation: "decode CH341 stream", Err: fmt.Errorf("truncated command")}
		}
	}
	return writtenBytes, dataBytes, nil
}

func encodedLength(command int) int {
	encoded := command & lengthMask
	if encoded == 0 {
		return 1
	}
	return encoded
}

func checkStatusBytes(status []byte) error {
	for offset, value := range status {
		if value&ackBit != 0 {
			return &Error{
				Kind:      ErrorNACK,
				Operation: "CH341 I2C",
				Err:       fmt.Errorf("NACK on byte %d", offset),
			}
		}
	}
	return nil
}

func minimumTransferTimeout(writeLength, readLength int, speed I2CSpeed) time.Duration {
	const (
		transactionOverheadBytes = 8
		bitsPerI2CByte           = 9
	)
	bitsPerSecond := int64(speed) * 1000
	bytesOnWire := int64(writeLength + readLength + transactionOverheadBytes)
	wire := time.Duration((bytesOnWire*bitsPerI2CByte*int64(time.Second) + bitsPerSecond - 1) / bitsPerSecond)
	return max(defaultTransferTimeout, wire+defaultTransferTimeout)
}

func classifyUSBError(operation string, err error) error {
	kind := ErrorIO
	var usbError gousb.Error
	var transferStatus gousb.TransferStatus
	switch {
	case errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &transferStatus) &&
			(transferStatus == gousb.TransferTimedOut || transferStatus == gousb.TransferCancelled):
		kind = ErrorTimeout
	case errors.As(err, &transferStatus) && transferStatus == gousb.TransferNoDevice:
		kind = ErrorDeviceMissing
	case errors.As(err, &transferStatus) &&
		(transferStatus == gousb.TransferStall || transferStatus == gousb.TransferOverflow):
		kind = ErrorProtocol
	case errors.As(err, &usbError):
		switch usbError {
		case gousb.ErrorAccess:
			kind = ErrorPermission
		case gousb.ErrorBusy:
			kind = ErrorBusy
		case gousb.ErrorNoDevice, gousb.ErrorNotFound:
			kind = ErrorDeviceMissing
		case gousb.ErrorTimeout:
			kind = ErrorTimeout
		case gousb.ErrorInvalidParam:
			kind = ErrorInvalidRequest
		case gousb.ErrorPipe, gousb.ErrorOverflow:
			kind = ErrorProtocol
		default:
			kind = ErrorIO
		}
	}
	return &Error{Kind: kind, Operation: operation, Err: err}
}

func isTimeout(err error) bool {
	return IsKind(classifyUSBError("timeout check", err), ErrorTimeout)
}

func shouldInvalidateSession(err error) bool {
	return IsKind(err, ErrorDeviceMissing) || IsKind(err, ErrorIO) || IsKind(err, ErrorProtocol)
}

func (t *Ch341Transport) invalidateSession(session *ch341Session) {
	t.sessionMu.Lock()
	defer t.sessionMu.Unlock()
	if t.session != session {
		return
	}
	_ = t.session.close()
	t.session = nil
}

func (s *ch341Session) close() error {
	s.iface.Close()
	configError := s.config.Close()
	deviceError := s.device.Close()
	if configError != nil {
		return configError
	}
	return deviceError
}

func (t *Ch341Transport) enumerateUSB(includeDeviceStrings bool) ([]USBDeviceInfo, bool) {
	descriptors := make(map[string]*USBDeviceInfo)
	devices, _ := t.context.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		key := usbDeviceKey(desc.Bus, desc.Address)
		descriptors[key] = &USBDeviceInfo{
			Bus:       desc.Bus,
			Device:    desc.Address,
			Vendor:    fmt.Sprintf("%04x", uint16(desc.Vendor)),
			ProductID: fmt.Sprintf("%04x", uint16(desc.Product)),
			Speed:     desc.Speed.String(),
			Class:     int(desc.Class),
			Candidate: t.matches(uint16(desc.Vendor), uint16(desc.Product)),
		}
		return includeDeviceStrings
	})
	for _, device := range devices {
		key := usbDeviceKey(device.Desc.Bus, device.Desc.Address)
		if info := descriptors[key]; info != nil {
			info.Manufacturer, _ = device.Manufacturer()
			info.Product, _ = device.Product()
		}
		_ = device.Close()
	}

	result := make([]USBDeviceInfo, 0, len(descriptors))
	candidateFound := false
	for _, info := range descriptors {
		result = append(result, *info)
		candidateFound = candidateFound || info.Candidate
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Bus != result[j].Bus {
			return result[i].Bus < result[j].Bus
		}
		return result[i].Device < result[j].Device
	})
	return result, candidateFound
}

func (t *Ch341Transport) matches(vendor, product uint16) bool {
	for _, id := range t.usbIDs {
		if id.Vendor == vendor && id.Product == product {
			return true
		}
	}
	return false
}

func usbDeviceKey(bus, address int) string {
	return fmt.Sprintf("%d:%d", bus, address)
}
