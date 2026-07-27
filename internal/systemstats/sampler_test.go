package systemstats

import "testing"

func TestSnapshotFromSamplesCalculatesCPUAndMemory(t *testing.T) {
	previous := hostSample{idle: 400, total: 1000}
	current := hostSample{idle: 600, total: 2000, memoryUsed: 6, memoryTotal: 8}
	result := snapshotFromSamples(previous, current, true)
	if !result.Available || result.CPUPercent != 80 || result.MemoryPercent != 75 || result.MemoryUsedBytes != 6 || result.MemoryTotalBytes != 8 {
		t.Fatalf("unexpected resource snapshot %#v", result)
	}
}

func TestSnapshotFromSamplesClampsInvalidCounters(t *testing.T) {
	result := snapshotFromSamples(hostSample{idle: 500, total: 1000}, hostSample{idle: 400, total: 900, memoryUsed: 12, memoryTotal: 8}, true)
	if result.CPUPercent != 0 || result.MemoryPercent != 100 {
		t.Fatalf("unexpected clamped snapshot %#v", result)
	}
}
