//go:build windows

package systemstats

import "testing"

func TestReadHostSampleReturnsPhysicalMemoryAndCPUCounters(t *testing.T) {
	sample, err := readHostSample()
	if err != nil {
		t.Fatal(err)
	}
	if sample.total == 0 {
		t.Fatal("Windows returned empty aggregate CPU counters")
	}
	if sample.memoryTotal == 0 || sample.memoryUsed > sample.memoryTotal {
		t.Fatalf("unexpected physical memory counters: used=%d total=%d", sample.memoryUsed, sample.memoryTotal)
	}
}
