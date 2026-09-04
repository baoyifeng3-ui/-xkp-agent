package collect

import (
	"context"
	"fmt"
	"sync"
	"time"

	psnet "github.com/shirou/gopsutil/v4/net"
)

type networkSample struct {
	received uint64
	sent     uint64
	at       time.Time
}

type NetworkCollector struct {
	mu       sync.Mutex
	previous *networkSample
	counters func(context.Context, bool) ([]psnet.IOCountersStat, error)
	now      func() time.Time
}

func NewNetworkCollector() *NetworkCollector {
	return &NetworkCollector{counters: psnet.IOCountersWithContext, now: time.Now}
}

func (c *NetworkCollector) Name() string { return "network" }

func (c *NetworkCollector) Collect(ctx context.Context) (Snapshot, error) {
	values, err := c.counters(ctx, true)
	if err != nil {
		return Snapshot{}, NewCollectorError("NETWORK_UNAVAILABLE", fmt.Errorf("network collection failed: %w", err))
	}
	current := networkSample{at: c.now()}
	for _, value := range values {
		if value.Name == "lo" || value.Name == "lo0" {
			continue
		}
		current.received += value.BytesRecv
		current.sent += value.BytesSent
	}
	c.mu.Lock()
	previous := c.previous
	c.previous = &current
	c.mu.Unlock()
	if previous == nil || !current.at.After(previous.at) ||
		current.received < previous.received || current.sent < previous.sent {
		return Snapshot{}, nil
	}
	seconds := current.at.Sub(previous.at).Seconds()
	received := int64(float64(current.received-previous.received) / seconds)
	sent := int64(float64(current.sent-previous.sent) / seconds)
	return Snapshot{NetworkReceiveBytesPerSecond: &received, NetworkSendBytesPerSecond: &sent}, nil
}
