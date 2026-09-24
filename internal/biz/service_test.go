package biz

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cuckoohello/remote-mfi-for-xcertplay/internal/transport"
)

var errCertificateInjected = errors.New("injected certificate failure")

func TestConcurrentSameRequestIDTriggersChipOnce(t *testing.T) {
	driver := &fakeDriver{signDelay: 20 * time.Millisecond}
	service := newTestService(t, driver)
	requestID := uuid.NewString()
	challenge := []byte{1, 2, 3, 4}

	const callers = 20
	results := make(chan SignResult, callers)
	errors := make(chan error, callers)
	var start sync.WaitGroup
	start.Add(1)
	var workers sync.WaitGroup
	for range callers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			start.Wait()
			result, err := service.Sign(context.Background(), requestID, challenge)
			results <- result
			errors <- err
		}()
	}
	start.Done()
	workers.Wait()
	close(results)
	close(errors)

	for err := range errors {
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
	}
	var first []byte
	cacheHits := 0
	for result := range results {
		if first == nil {
			first = result.Signature
		} else if !bytes.Equal(first, result.Signature) {
			t.Fatal("concurrent callers received different signatures")
		}
		if result.Cached {
			cacheHits++
		}
	}
	if got := driver.signCalls.Load(); got != 1 {
		t.Fatalf("chip sign calls = %d, want 1", got)
	}
	if cacheHits != callers-1 {
		t.Fatalf("cache hits = %d, want %d", cacheHits, callers-1)
	}
}

func TestDifferentRequestIDsAreStrictlySerialized(t *testing.T) {
	driver := &fakeDriver{signDelay: 5 * time.Millisecond}
	service := newTestService(t, driver)

	const callers = 12
	var workers sync.WaitGroup
	errors := make(chan error, callers)
	for index := range callers {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			requestID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("request-%d", index))).String()
			_, err := service.Sign(context.Background(), requestID, []byte{byte(index + 1)})
			errors <- err
		}(index)
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
	}
	if got := driver.maximumActive.Load(); got != 1 {
		t.Fatalf("maximum concurrent chip calls = %d, want 1", got)
	}
	if got := driver.signCalls.Load(); got != callers {
		t.Fatalf("chip sign calls = %d, want %d", got, callers)
	}
}

func TestResetDoesNotClearIdempotencyCache(t *testing.T) {
	driver := &fakeDriver{}
	service := newTestService(t, driver)
	requestID := uuid.NewString()
	challenge := []byte{9, 8, 7}

	first, err := service.Sign(context.Background(), requestID, challenge)
	if err != nil {
		t.Fatalf("first Sign: %v", err)
	}
	if err := service.Reset(context.Background()); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	second, err := service.Sign(context.Background(), requestID, challenge)
	if err != nil {
		t.Fatalf("second Sign: %v", err)
	}
	if !second.Cached {
		t.Fatal("second Sign did not hit idempotency cache")
	}
	if !bytes.Equal(first.Signature, second.Signature) {
		t.Fatal("cached signature changed after reset")
	}
	if got := driver.signCalls.Load(); got != 1 {
		t.Fatalf("chip sign calls = %d, want 1", got)
	}
}

func TestRequestIDReuseWithDifferentChallengeIsRejected(t *testing.T) {
	service := newTestService(t, &fakeDriver{})
	requestID := uuid.NewString()
	if _, err := service.Sign(context.Background(), requestID, []byte{1}); err != nil {
		t.Fatalf("first Sign: %v", err)
	}
	if _, err := service.Sign(context.Background(), requestID, []byte{2}); err != ErrRequestIDReuse {
		t.Fatalf("error = %v, want ErrRequestIDReuse", err)
	}
}

func TestCertificateIsCachedForProcessLifetime(t *testing.T) {
	driver := &fakeDriver{certificate: []byte{1, 2, 3}}
	service := newTestService(t, driver)

	first, err := service.Certificate(context.Background())
	if err != nil {
		t.Fatalf("first Certificate: %v", err)
	}
	if first.Cached {
		t.Fatal("first Certificate reported cached=true")
	}
	if first.ProtocolMajor != 3 || !bytes.Equal(first.Data, driver.certificate) {
		t.Fatalf("unexpected first certificate: %+v", first)
	}
	digest := sha256.Sum256(driver.certificate)
	if first.SHA256Hex != hex.EncodeToString(digest[:]) {
		t.Fatalf("unexpected SHA256Hex: %q", first.SHA256Hex)
	}
	if first.Base64 != base64.StdEncoding.EncodeToString(driver.certificate) {
		t.Fatalf("unexpected Base64: %q", first.Base64)
	}

	second, err := service.Certificate(context.Background())
	if err != nil {
		t.Fatalf("second Certificate: %v", err)
	}
	if !second.Cached {
		t.Fatal("second Certificate should hit the process-lifetime cache")
	}
	if second.SHA256Hex != first.SHA256Hex || second.Base64 != first.Base64 {
		t.Fatal("cached certificate encoding differs from the first read")
	}
	if got := driver.certificateCalls.Load(); got != 1 {
		t.Fatalf("certificate calls = %d, want 1", got)
	}
}

func TestCertificateErrorsAreNotCached(t *testing.T) {
	driver := &fakeDriver{certificateErrors: 1, certificate: []byte{4, 5}}
	service := newTestService(t, driver)

	if _, err := service.Certificate(context.Background()); err == nil {
		t.Fatal("expected first Certificate to fail")
	}
	result, err := service.Certificate(context.Background())
	if err != nil {
		t.Fatalf("second Certificate: %v", err)
	}
	if result.Cached {
		t.Fatal("recovered certificate should not report cached=true")
	}
	if got := driver.certificateCalls.Load(); got != 2 {
		t.Fatalf("certificate calls = %d, want 2", got)
	}
}

func TestChipGateReturnsNoopReleaseOnFailure(t *testing.T) {
	gate := newChipGate()
	release, _, err := gate.acquire(context.Background(), "holder", time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	failRelease, _, err := gate.acquire(context.Background(), "waiter", 10*time.Millisecond)
	if err != ErrChipBusy {
		t.Fatalf("second acquire error = %v, want ErrChipBusy", err)
	}
	if failRelease == nil {
		t.Fatal("acquire must always return a non-nil release function")
	}
	failRelease()
	failRelease()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledRelease, _, err := gate.acquire(cancelled, "waiter", time.Second)
	if err != context.Canceled {
		t.Fatalf("cancelled acquire error = %v, want context.Canceled", err)
	}
	if cancelledRelease == nil {
		t.Fatal("cancelled acquire must return a non-nil release function")
	}
	cancelledRelease()
}

func newTestService(t *testing.T, driver *fakeDriver) *Service {
	t.Helper()
	service, err := NewService(driver, fakeInspector{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service
}

type fakeDriver struct {
	signDelay         time.Duration
	certificate       []byte
	certificateErrors int32

	signCalls        atomic.Int32
	certificateCalls atomic.Int32
	active           atomic.Int32
	maximumActive    atomic.Int32
}

func (d *fakeDriver) ProtocolMajor(context.Context) (uint8, error) {
	return 3, nil
}

func (d *fakeDriver) ReadCertificate(context.Context) ([]byte, error) {
	d.certificateCalls.Add(1)
	if d.certificateErrors > 0 {
		d.certificateErrors--
		return nil, errCertificateInjected
	}
	if d.certificate == nil {
		return []byte{1, 2, 3}, nil
	}
	return append([]byte(nil), d.certificate...), nil
}

func (d *fakeDriver) SignChallenge(_ context.Context, challenge []byte) ([]byte, error) {
	d.signCalls.Add(1)
	active := d.active.Add(1)
	for {
		maximum := d.maximumActive.Load()
		if active <= maximum || d.maximumActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	defer d.active.Add(-1)
	time.Sleep(d.signDelay)
	digest := sha256.Sum256(challenge)
	return digest[:], nil
}

type fakeInspector struct{}

func (fakeInspector) InspectUSB(bool) transport.USBStatus {
	return transport.USBStatus{State: "ready", VIDPID: "1a86:5512"}
}
