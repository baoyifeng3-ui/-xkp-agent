package collect

import (
	"context"
	"testing"
	"time"

	psnet "github.com/shirou/gopsutil/v4/net"
)

func TestNetworkCollectorCalculatesRatesAndExcludesLoopback(t *testing.T) {
	now := time.Unix(100, 0)
	samples := [][]psnet.IOCountersStat{
		{{Name: "lo", BytesRecv: 9999, BytesSent: 9999}, {Name: "eth0", BytesRecv: 1000, BytesSent: 2000}},
		{{Name: "lo", BytesRecv: 99999, BytesSent: 99999}, {Name: "eth0", BytesRecv: 5000, BytesSent: 8000}},
	}
	index := 0
	collector := NewNetworkCollector()
	collector.now = func() time.Time { return now }
	collector.counters = func(context.Context, bool) ([]psnet.IOCountersStat, error) {
		value := samples[index]
		index++
		return value, nil
	}

	first, err := collector.Collect(context.Background())
	if err != nil || first.NetworkReceiveBytesPerSecond != nil || first.NetworkSendBytesPerSecond != nil {
		t.Fatalf("first sample = %#v, %v", first, err)
	}
	now = now.Add(2 * time.Second)
	second, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.NetworkReceiveBytesPerSecond == nil || *second.NetworkReceiveBytesPerSecond != 2000 {
		t.Fatalf("receive rate = %#v", second.NetworkReceiveBytesPerSecond)
	}
	if second.NetworkSendBytesPerSecond == nil || *second.NetworkSendBytesPerSecond != 3000 {
		t.Fatalf("send rate = %#v", second.NetworkSendBytesPerSecond)
	}
}

func TestNetworkCollectorDoesNotEmitNegativeRatesAfterCounterReset(t *testing.T) {
	now := time.Unix(100, 0)
	samples := [][]psnet.IOCountersStat{{{Name: "eth0", BytesRecv: 5000, BytesSent: 8000}}, {{Name: "eth0", BytesRecv: 10, BytesSent: 20}}}
	index := 0
	collector := NewNetworkCollector()
	collector.now = func() time.Time { return now }
	collector.counters = func(context.Context, bool) ([]psnet.IOCountersStat, error) { value := samples[index]; index++; return value, nil }
	_, _ = collector.Collect(context.Background())
	now = now.Add(time.Second)
	result, err := collector.Collect(context.Background())
	if err != nil || result.NetworkReceiveBytesPerSecond != nil || result.NetworkSendBytesPerSecond != nil {
		t.Fatalf("reset result = %#v, %v", result, err)
	}
}
