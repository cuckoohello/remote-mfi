package biz

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cuckoohello/remote-mfi/internal/transport"
)

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

func TestCertificateIsNotCached(t *testing.T) {
	driver := &fakeDriver{certificate: []byte{1, 2, 3}}
	service := newTestService(t, driver)
	for range 2 {
		result, err := service.Certificate(context.Background())
		if err != nil {
			t.Fatalf("Certificate: %v", err)
		}
		if result.ProtocolMajor != 3 || !bytes.Equal(result.Data, driver.certificate) {
			t.Fatalf("unexpected certificate result: %+v", result)
		}
	}
	if got := driver.certificateCalls.Load(); got != 2 {
		t.Fatalf("certificate calls = %d, want 2", got)
	}
}

func TestChipGateHonorsDeadline(t *testing.T) {
	gate := newChipGate()
	release, _, err := gate.acquire(context.Background(), "holder", time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	started := time.Now()
	if _, _, err := gate.acquire(context.Background(), "waiter", 20*time.Millisecond); err != ErrChipBusy {
		t.Fatalf("second acquire error = %v, want ErrChipBusy", err)
	}
	if elapsed := time.Since(started); elapsed < 15*time.Millisecond {
		t.Fatalf("deadline elapsed too early: %s", elapsed)
	}
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
	signDelay   time.Duration
	certificate []byte

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
