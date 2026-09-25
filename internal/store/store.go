package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("记录不存在")

// Meta carries the identity and bookkeeping fields shared by stored records.
type Meta struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Entity is implemented by records persisted in a Collection.
type Entity interface {
	EntityID() string
	EntityCreatedAt() time.Time
}

// Mutable is implemented by records whose identity fields can be assigned.
// Collection uses it to stamp identifiers and timestamps on write.
type Mutable interface {
	SetEntityMeta(id string, created, updated time.Time)
}

// Collection is a concurrency safe, JSON backed collection of records.
type Collection[T Entity] struct {
	mu    sync.RWMutex
	items map[string]T
	path  string
}

// NewCollection loads a collection from path, creating it when absent.
func NewCollection[T Entity](path string) (*Collection[T], error) {
	c := &Collection[T]{items: make(map[string]T), path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, c.flush()
		}
		return nil, err
	}
	if len(data) == 0 {
		return c, nil
	}
	var list []T
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	for _, item := range list {
		c.items[item.EntityID()] = item
	}
	return c, nil
}

// List returns every record sorted by identifier.
func (c *Collection[T]) List() []T {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]T, 0, len(c.items))
	for _, v := range c.items {
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].EntityID() < out[j].EntityID()
	})
	return out
}

// Get fetches a record by identifier.
func (c *Collection[T]) Get(id string) (T, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.items[id]
	if !ok {
		var zero T
		return zero, ErrNotFound
	}
	return v, nil
}

// Put inserts or replaces a record and persists the collection. It returns the
// stored record so callers observe the stamped identifier and timestamps.
func (c *Collection[T]) Put(item T) (T, error) {
	id := item.EntityID()
	if id == "" {
		var zero T
		return zero, errors.New("记录标识不能为空")
	}
	c.mu.Lock()
	created := time.Now()
	if old, ok := c.items[id]; ok {
		created = old.EntityCreatedAt()
	}
	if m, ok := any(&item).(Mutable); ok {
		m.SetEntityMeta(id, created, time.Now())
	}
	c.items[id] = item
	c.mu.Unlock()
	if err := c.flush(); err != nil {
		var zero T
		return zero, err
	}
	return item, nil
}

// Delete removes a record and persists the collection.
func (c *Collection[T]) Delete(id string) error {
	c.mu.Lock()
	if _, ok := c.items[id]; !ok {
		c.mu.Unlock()
		return ErrNotFound
	}
	delete(c.items, id)
	c.mu.Unlock()
	return c.flush()
}

// Replace swaps the contents of the collection with the supplied records.
func (c *Collection[T]) Replace(items []T) error {
	next := make(map[string]T, len(items))
	for _, item := range items {
		next[item.EntityID()] = item
	}
	c.mu.Lock()
	c.items = next
	c.mu.Unlock()
	return c.flush()
}

func (c *Collection[T]) flush() error {
	c.mu.RLock()
	list := make([]T, 0, len(c.items))
	for _, v := range c.items {
		list = append(list, v)
	}
	c.mu.RUnlock()
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].EntityID() < list[j].EntityID()
	})
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if c.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Sample is a single point in a time series.
type Sample struct {
	Time   time.Time `json:"time"`
	RxRate float64   `json:"rxRate"`
	TxRate float64   `json:"txRate"`
	Rx     uint64    `json:"rx"`
	Tx     uint64    `json:"tx"`
}

// SeriesStore keeps rolling numeric samples for charts.
type SeriesStore struct {
	mu     sync.RWMutex
	path   string
	points []Sample
	max    int
}

// NewSeriesStore opens a persisted time series.
func NewSeriesStore(path string, max int) *SeriesStore {
	if max <= 0 {
		max = 720
	}
	s := &SeriesStore{path: path, max: max}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &s.points)
	}
	return s
}

// Append records a new sample and trims the oldest entries.
func (s *SeriesStore) Append(sample Sample) {
	s.mu.Lock()
	s.points = append(s.points, sample)
	if len(s.points) > s.max {
		s.points = append(s.points[:0], s.points[len(s.points)-s.max:]...)
	}
	points := append([]Sample(nil), s.points...)
	path := s.path
	s.mu.Unlock()
	if path == "" {
		return
	}
	if data, err := json.Marshal(points); err == nil {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, data, 0o644)
	}
}

// Points returns a copy of the retained samples.
func (s *SeriesStore) Points() []Sample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Sample(nil), s.points...)
}

// Last returns the most recent sample.
func (s *SeriesStore) Last() (Sample, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.points) == 0 {
		return Sample{}, false
	}
	return s.points[len(s.points)-1], true
}

// Reset clears the stored samples.
func (s *SeriesStore) Reset() {
	s.mu.Lock()
	s.points = nil
	path := s.path
	s.mu.Unlock()
	if path != "" {
		_ = os.WriteFile(path, []byte("[]"), 0o644)
	}
}
