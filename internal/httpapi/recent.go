package httpapi

import (
	"sync"
	"time"
)

type RecentRequest struct {
	Time   string `json:"time"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	Millis int64  `json:"ms"`
	Note   string `json:"note"`
}

type recentStore struct {
	location *time.Location
	capacity int

	mu      sync.Mutex
	entries []RecentRequest
	next    int
	full    bool
}

func newRecentStore(capacity int, location *time.Location) *recentStore {
	return &recentStore{
		location: location,
		capacity: capacity,
		entries:  make([]RecentRequest, capacity),
	}
}

func (s *recentStore) add(method, path string, status int, duration time.Duration, note string) {
	entry := RecentRequest{
		Time:   time.Now().In(s.location).Format("2006-01-02T15:04:05.000Z07:00"),
		Method: method,
		Path:   path,
		Status: status,
		Millis: duration.Milliseconds(),
		Note:   note,
	}
	s.mu.Lock()
	s.entries[s.next] = entry
	s.next = (s.next + 1) % s.capacity
	if s.next == 0 {
		s.full = true
	}
	s.mu.Unlock()
}

func (s *recentStore) listNewestFirst() []RecentRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := s.next
	if s.full {
		count = s.capacity
	}
	result := make([]RecentRequest, 0, count)
	for offset := 0; offset < count; offset++ {
		index := s.next - 1 - offset
		if index < 0 {
			index += s.capacity
		}
		result = append(result, s.entries[index])
	}
	return result
}
