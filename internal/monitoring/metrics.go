package monitoring

import "github.com/prometheus/client_golang/prometheus"

var (
	trackerBlockGap = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bloop_tracker_block_gap",
			Help: "Difference between safe head and last processed block per network.",
		},
		[]string{"network"},
	)
	trackerCurrentBlock = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bloop_tracker_current_block_height",
			Help: "Current blockchain height observed by tracker per network.",
		},
		[]string{"network"},
	)
	trackerSafeHead = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bloop_tracker_safe_head",
			Help: "Safe head considering confirmation depth per network.",
		},
		[]string{"network"},
	)
	trackerLastProcessed = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bloop_tracker_last_processed_block",
			Help: "Last processed block persisted by tracker per network.",
		},
		[]string{"network"},
	)
	trackerQueueLen = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bloop_tracker_block_queue_len",
			Help: "Current block queue length per network.",
		},
		[]string{"network"},
	)
	trackerQueueCap = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bloop_tracker_block_queue_cap",
			Help: "Configured block queue capacity per network.",
		},
		[]string{"network"},
	)
	trackerProcessedBlocks = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bloop_tracker_processed_blocks_total",
			Help: "Total processed blocks since tracker start per network.",
		},
		[]string{"network"},
	)
	trackerProcessedTxs = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bloop_tracker_processed_txs_total",
			Help: "Total processed transactions since tracker start per network.",
		},
		[]string{"network"},
	)
	trackerErrorCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bloop_tracker_error_count_total",
			Help: "Total tracker errors since tracker start per network.",
		},
		[]string{"network"},
	)
	trackerSkippedChannelFull = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bloop_tracker_skipped_channel_full_total",
			Help: "Total skipped enqueues due to full channel since tracker start per network.",
		},
		[]string{"network"},
	)
	trackerSkippedProcessed = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bloop_tracker_skipped_processed_total",
			Help: "Total already-processed block skips since tracker start per network.",
		},
		[]string{"network"},
	)
	trackerBlocksPerSecond = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bloop_tracker_blocks_per_second",
			Help: "Average processed blocks per second since tracker start per network.",
		},
		[]string{"network"},
	)
	trackerTxsPerSecond = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bloop_tracker_txs_per_second",
			Help: "Average processed transactions per second since tracker start per network.",
		},
		[]string{"network"},
	)
	trackerUptimeSeconds = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bloop_tracker_uptime_seconds",
			Help: "Tracker uptime in seconds per network.",
		},
		[]string{"network"},
	)
)

func init() {
	prometheus.MustRegister(
		trackerBlockGap,
		trackerCurrentBlock,
		trackerSafeHead,
		trackerLastProcessed,
		trackerQueueLen,
		trackerQueueCap,
		trackerProcessedBlocks,
		trackerProcessedTxs,
		trackerErrorCount,
		trackerSkippedChannelFull,
		trackerSkippedProcessed,
		trackerBlocksPerSecond,
		trackerTxsPerSecond,
		trackerUptimeSeconds,
	)
}

// TrackerSnapshot is a point-in-time tracker health state exported to Prometheus.
type TrackerSnapshot struct {
	Network            string
	BlockGap           uint64
	CurrentBlockHeight uint64
	SafeHead           uint64
	LastProcessedBlock uint64
	BlockQueueLen      int
	BlockQueueCap      int
	ProcessedBlocks    uint64
	ProcessedTxs       uint64
	ErrorCount         uint64
	SkippedChannelFull uint64
	SkippedProcessed   uint64
	BlocksPerSecond    float64
	TxsPerSecond       float64
	UptimeSeconds      float64
}

// SetTrackerSnapshot updates all tracker gauges for a network in one call.
func SetTrackerSnapshot(s TrackerSnapshot) {
	labels := prometheus.Labels{"network": s.Network}
	trackerBlockGap.With(labels).Set(float64(s.BlockGap))
	trackerCurrentBlock.With(labels).Set(float64(s.CurrentBlockHeight))
	trackerSafeHead.With(labels).Set(float64(s.SafeHead))
	trackerLastProcessed.With(labels).Set(float64(s.LastProcessedBlock))
	trackerQueueLen.With(labels).Set(float64(s.BlockQueueLen))
	trackerQueueCap.With(labels).Set(float64(s.BlockQueueCap))
	trackerProcessedBlocks.With(labels).Set(float64(s.ProcessedBlocks))
	trackerProcessedTxs.With(labels).Set(float64(s.ProcessedTxs))
	trackerErrorCount.With(labels).Set(float64(s.ErrorCount))
	trackerSkippedChannelFull.With(labels).Set(float64(s.SkippedChannelFull))
	trackerSkippedProcessed.With(labels).Set(float64(s.SkippedProcessed))
	trackerBlocksPerSecond.With(labels).Set(s.BlocksPerSecond)
	trackerTxsPerSecond.With(labels).Set(s.TxsPerSecond)
	trackerUptimeSeconds.With(labels).Set(s.UptimeSeconds)
}
