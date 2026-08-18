package collect

import "context"

type MultiCollector struct{ collectors []Collector }

func NewMultiCollector(collectors ...Collector) *MultiCollector {
	return &MultiCollector{collectors: collectors}
}
func (m *MultiCollector) Snapshot(ctx context.Context) Snapshot { return Gather(ctx, m.collectors...) }
