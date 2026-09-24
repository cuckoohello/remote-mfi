package biz

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/cuckoohello/remote-mfi-for-xcertplay/internal/transport"
)

const (
	chipWaitDeadline = 8 * time.Second
	cacheTTL         = 60 * time.Second
	cacheGCInterval  = 15 * time.Second
)

var (
	ErrChipBusy        = errors.New("chip busy, retry")
	ErrChipMissing     = errors.New("chip missing")
	ErrRequestIDReuse  = errors.New("requestId reuse with different challenge")
	ErrChallengeSize   = errors.New("challenge must be 1..128 bytes")
	ErrRequestIDFormat = errors.New("requestId must be a UUID")
)

type ChipDriver interface {
	ProtocolMajor(ctx context.Context) (uint8, error)
	ReadCertificate(ctx context.Context) ([]byte, error)
	SignChallenge(ctx context.Context, challenge []byte) ([]byte, error)
}

type Certificate struct {
	ProtocolMajor uint8
	Data          []byte
	WaitDuration  time.Duration
	ChipDuration  time.Duration
}

type SignResult struct {
	Signature    []byte
	Cached       bool
	WaitDuration time.Duration
	ChipDuration time.Duration
}

type HealthStatus struct {
	Chip    string
	Reason  string
	VIDPID  string
	Devices []transport.USBDeviceInfo
}

type RuntimeStatus struct {
	CacheEntries int
	LockHolder   string
	LockHeld     time.Duration
}

type Service struct {
	driver    ChipDriver
	inspector transport.USBInspector
	gate      *chipGate
	cache     *signatureCache
}

func NewService(driver ChipDriver, inspector transport.USBInspector) (*Service, error) {
	if driver == nil {
		return nil, fmt.Errorf("chip driver is required")
	}
	if inspector == nil {
		return nil, fmt.Errorf("USB inspector is required")
	}
	return &Service{
		driver:    driver,
		inspector: inspector,
		gate:      newChipGate(),
		cache:     newSignatureCache(cacheTTL),
	}, nil
}

func (s *Service) Certificate(ctx context.Context) (Certificate, error) {
	release, waitDuration, err := s.gate.acquire(ctx, "certificate", chipWaitDeadline)
	if err != nil {
		return Certificate{}, err
	}
	defer release()

	chipStarted := time.Now()
	hardwareContext := context.WithoutCancel(ctx)
	protocolMajor, err := s.driver.ProtocolMajor(hardwareContext)
	if err != nil {
		return Certificate{}, normalizeHardwareError(err)
	}
	certificate, err := s.driver.ReadCertificate(hardwareContext)
	if err != nil {
		return Certificate{}, normalizeHardwareError(err)
	}
	return Certificate{
		ProtocolMajor: protocolMajor,
		Data:          append([]byte(nil), certificate...),
		WaitDuration:  waitDuration,
		ChipDuration:  time.Since(chipStarted),
	}, nil
}

func (s *Service) Sign(ctx context.Context, requestID string, challenge []byte) (SignResult, error) {
	if _, err := uuid.Parse(requestID); err != nil {
		return SignResult{}, ErrRequestIDFormat
	}
	if len(challenge) < 1 || len(challenge) > 128 {
		return SignResult{}, ErrChallengeSize
	}
	challengeDigest := sha256.Sum256(challenge)
	if entry, ok := s.cache.get(requestID); ok {
		return cachedResult(entry, challengeDigest)
	}

	release, waitDuration, err := s.gate.acquire(ctx, requestID, chipWaitDeadline)
	if err != nil {
		return SignResult{}, err
	}
	defer release()

	if entry, ok := s.cache.get(requestID); ok {
		return cachedResult(entry, challengeDigest)
	}

	chipStarted := time.Now()
	signature, err := s.driver.SignChallenge(context.WithoutCancel(ctx), append([]byte(nil), challenge...))
	if err != nil {
		return SignResult{}, normalizeHardwareError(err)
	}
	entry := signatureEntry{
		challengeDigest: challengeDigest,
		signature:       append([]byte(nil), signature...),
		expiresAt:       time.Now().Add(s.cache.ttl),
	}
	s.cache.put(requestID, entry)
	return SignResult{
		Signature:    append([]byte(nil), signature...),
		WaitDuration: waitDuration,
		ChipDuration: time.Since(chipStarted),
	}, nil
}

func (s *Service) Reset(context.Context) error {
	return nil
}

func (s *Service) Probe(ctx context.Context) (uint8, error) {
	release, _, err := s.gate.acquire(ctx, "startup-probe", chipWaitDeadline)
	if err != nil {
		return 0, err
	}
	defer release()
	value, err := s.driver.ProtocolMajor(context.WithoutCancel(ctx))
	return value, normalizeHardwareError(err)
}

func (s *Service) Health() HealthStatus {
	return s.inspectUSB(false)
}

func (s *Service) Diagnostics() HealthStatus {
	return s.inspectUSB(true)
}

func (s *Service) inspectUSB(includeDeviceStrings bool) HealthStatus {
	status := s.inspector.InspectUSB(includeDeviceStrings)
	devices := make([]transport.USBDeviceInfo, len(status.Devices))
	copy(devices, status.Devices)
	return HealthStatus{
		Chip:    status.State,
		Reason:  status.Reason,
		VIDPID:  status.VIDPID,
		Devices: devices,
	}
}

func (s *Service) Runtime() RuntimeStatus {
	holder, held := s.gate.status()
	return RuntimeStatus{
		CacheEntries: s.cache.len(),
		LockHolder:   holder,
		LockHeld:     held,
	}
}

func (s *Service) RunCacheGC(ctx context.Context) {
	ticker := time.NewTicker(cacheGCInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.cache.deleteExpired(time.Now())
		case <-ctx.Done():
			return
		}
	}
}

func cachedResult(entry signatureEntry, digest [sha256.Size]byte) (SignResult, error) {
	if entry.challengeDigest != digest {
		return SignResult{}, ErrRequestIDReuse
	}
	return SignResult{
		Signature: append([]byte(nil), entry.signature...),
		Cached:    true,
	}, nil
}

func normalizeHardwareError(err error) error {
	if err == nil {
		return nil
	}
	if transport.IsKind(err, transport.ErrorDeviceMissing) {
		return fmt.Errorf("%w: %v", ErrChipMissing, err)
	}
	return err
}

type chipGate struct {
	token chan struct{}

	mu       sync.RWMutex
	holder   string
	acquired time.Time
}

func newChipGate() *chipGate {
	return &chipGate{token: make(chan struct{}, 1)}
}

func (g *chipGate) acquire(ctx context.Context, holder string, timeout time.Duration) (func(), time.Duration, error) {
	started := time.Now()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case g.token <- struct{}{}:
		g.mu.Lock()
		g.holder = holder
		g.acquired = time.Now()
		g.mu.Unlock()
		return func() {
			g.mu.Lock()
			g.holder = ""
			g.acquired = time.Time{}
			g.mu.Unlock()
			<-g.token
		}, time.Since(started), nil
	case <-timer.C:
		return nil, time.Since(started), ErrChipBusy
	case <-ctx.Done():
		return nil, time.Since(started), ctx.Err()
	}
}

func (g *chipGate) status() (string, time.Duration) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.holder == "" {
		return "", 0
	}
	return g.holder, time.Since(g.acquired)
}

type signatureEntry struct {
	challengeDigest [sha256.Size]byte
	signature       []byte
	expiresAt       time.Time
}

type signatureCache struct {
	ttl time.Duration

	mu      sync.RWMutex
	entries map[string]signatureEntry
}

func newSignatureCache(ttl time.Duration) *signatureCache {
	return &signatureCache{
		ttl:     ttl,
		entries: make(map[string]signatureEntry),
	}
}

func (c *signatureCache) get(requestID string) (signatureEntry, bool) {
	c.mu.RLock()
	entry, ok := c.entries[requestID]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return signatureEntry{}, false
	}
	entry.signature = append([]byte(nil), entry.signature...)
	return entry, true
}

func (c *signatureCache) put(requestID string, entry signatureEntry) {
	c.mu.Lock()
	c.entries[requestID] = entry
	c.mu.Unlock()
}

func (c *signatureCache) deleteExpired(now time.Time) {
	c.mu.Lock()
	for requestID, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, requestID)
		}
	}
	c.mu.Unlock()
}

func (c *signatureCache) len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
