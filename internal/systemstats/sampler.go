// Package systemstats samples host resource usage for the authenticated local
// system status endpoint.
package systemstats

import "sync"

// Snapshot is safe to expose to the desktop UI and contains no process list,
// path, hostname, or other host-identifying data.
type Snapshot struct {
	Available        bool    `json:"available"`
	CPUPercent       float64 `json:"cpuPercent"`
	MemoryPercent    float64 `json:"memoryPercent"`
	MemoryUsedBytes  uint64  `json:"memoryUsedBytes"`
	MemoryTotalBytes uint64  `json:"memoryTotalBytes"`
}

type hostSample struct {
	idle        uint64
	total       uint64
	memoryUsed  uint64
	memoryTotal uint64
}

// Sampler keeps only the preceding aggregate CPU counters. CPU percentage is
// calculated over the interval between /api/system polls instead of starting
// another background goroutine.
type Sampler struct {
	mu       sync.Mutex
	previous hostSample
	primed   bool
}

func NewSampler() *Sampler {
	sampler := &Sampler{}
	_ = sampler.Snapshot()
	return sampler
}

func (s *Sampler) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := readHostSample()
	if err != nil {
		return Snapshot{}
	}
	result := snapshotFromSamples(s.previous, current, s.primed)
	s.previous = current
	s.primed = true
	return result
}

func snapshotFromSamples(previous, current hostSample, primed bool) Snapshot {
	result := Snapshot{
		Available:        current.memoryTotal > 0,
		MemoryUsedBytes:  current.memoryUsed,
		MemoryTotalBytes: current.memoryTotal,
	}
	if current.memoryTotal > 0 {
		result.MemoryPercent = clampPercent(float64(current.memoryUsed) * 100 / float64(current.memoryTotal))
	}
	if primed && current.total > previous.total && current.idle >= previous.idle {
		totalDelta := current.total - previous.total
		idleDelta := current.idle - previous.idle
		if idleDelta <= totalDelta {
			result.CPUPercent = clampPercent(float64(totalDelta-idleDelta) * 100 / float64(totalDelta))
		}
	}
	return result
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
