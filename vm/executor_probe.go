package vm

import (
	"dex/logs"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var vmFailedSummarySeen sync.Map

const (
	vmProbeReportInterval  = 10 * time.Second
	vmProbeSlowPreExecute  = 1500 * time.Millisecond
	vmProbeSlowCommitBlock = 1500 * time.Millisecond
	vmProbeSlowApplyResult = 1200 * time.Millisecond
)

type vmExecutorProbeStats struct {
	preBlocks         atomic.Uint64
	preTxSeen         atomic.Uint64
	preTxExecuted     atomic.Uint64
	prePairs          atomic.Uint64
	preDiffOps        atomic.Uint64
	preWSOps          atomic.Uint64
	preDryRunErrors   atomic.Uint64
	preTotalNs        atomic.Uint64
	preRebuildNs      atomic.Uint64
	preAppliedCheckNs atomic.Uint64
	preDryRunNs       atomic.Uint64
	preApplyWSNs      atomic.Uint64
	preWitnessNs      atomic.Uint64
	preRewardsNs      atomic.Uint64
	preDiffNs         atomic.Uint64

	commitCalls    atomic.Uint64
	commitTotalNs  atomic.Uint64
	commitReexecNs atomic.Uint64
	commitApplyNs  atomic.Uint64

	applyCalls      atomic.Uint64
	applyWriteOps   atomic.Uint64
	applyAccountOps atomic.Uint64
	applyStateOps   atomic.Uint64
	applyTotalNs    atomic.Uint64
	applyDiffNs     atomic.Uint64
	applyStakeNs    atomic.Uint64
	applySyncNs     atomic.Uint64
	applyFlushNs    atomic.Uint64

	lastReportAtNs atomic.Int64
}

var vmProbe vmExecutorProbeStats

func addProbeDuration(dst *atomic.Uint64, d time.Duration) {
	if d <= 0 {
		return
	}
	dst.Add(uint64(d))
}

func avgProbeDuration(totalNs, count uint64) time.Duration {
	if count == 0 {
		return 0
	}
	return time.Duration(totalNs / count)
}

func maybeLogVMProbeSummary(now time.Time) {
	nowNs := now.UnixNano()
	last := vmProbe.lastReportAtNs.Load()
	if last != 0 && nowNs-last < int64(vmProbeReportInterval) {
		return
	}
	if !vmProbe.lastReportAtNs.CompareAndSwap(last, nowNs) {
		return
	}

	preBlocks := vmProbe.preBlocks.Load()
	commitCalls := vmProbe.commitCalls.Load()
	applyCalls := vmProbe.applyCalls.Load()

	logs.Info(
		"[VM][Probe] pre{blocks=%d avg=%s rebuild=%s applied=%s dryrun=%s applyWS=%s witness=%s rewards=%s diff=%s txSeen=%d txExec=%d pairs=%d wsOps=%d diffOps=%d dryErr=%d} commit{calls=%d avg=%s reexec=%s apply=%s} apply{calls=%d avg=%s diffLoop=%s stake=%s sync=%s flush=%s writeOps=%d accountOps=%d stateOps=%d}",
		preBlocks,
		avgProbeDuration(vmProbe.preTotalNs.Load(), preBlocks),
		avgProbeDuration(vmProbe.preRebuildNs.Load(), preBlocks),
		avgProbeDuration(vmProbe.preAppliedCheckNs.Load(), preBlocks),
		avgProbeDuration(vmProbe.preDryRunNs.Load(), preBlocks),
		avgProbeDuration(vmProbe.preApplyWSNs.Load(), preBlocks),
		avgProbeDuration(vmProbe.preWitnessNs.Load(), preBlocks),
		avgProbeDuration(vmProbe.preRewardsNs.Load(), preBlocks),
		avgProbeDuration(vmProbe.preDiffNs.Load(), preBlocks),
		vmProbe.preTxSeen.Load(),
		vmProbe.preTxExecuted.Load(),
		vmProbe.prePairs.Load(),
		vmProbe.preWSOps.Load(),
		vmProbe.preDiffOps.Load(),
		vmProbe.preDryRunErrors.Load(),
		commitCalls,
		avgProbeDuration(vmProbe.commitTotalNs.Load(), commitCalls),
		avgProbeDuration(vmProbe.commitReexecNs.Load(), commitCalls),
		avgProbeDuration(vmProbe.commitApplyNs.Load(), commitCalls),
		applyCalls,
		avgProbeDuration(vmProbe.applyTotalNs.Load(), applyCalls),
		avgProbeDuration(vmProbe.applyDiffNs.Load(), applyCalls),
		avgProbeDuration(vmProbe.applyStakeNs.Load(), applyCalls),
		avgProbeDuration(vmProbe.applySyncNs.Load(), applyCalls),
		avgProbeDuration(vmProbe.applyFlushNs.Load(), applyCalls),
		vmProbe.applyWriteOps.Load(),
		vmProbe.applyAccountOps.Load(),
		vmProbe.applyStateOps.Load(),
	)
}

func classifyTxFailureReason(err error) string {
	if err == nil {
		return "unknown"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "buyer frozen balance underflow"):
		return "buyer frozen balance underflow"
	case strings.Contains(msg, "seller frozen balance underflow"):
		return "seller frozen balance underflow"
	case strings.Contains(msg, "failed to update account balances"):
		return "failed to update account balances"
	default:
		return msg
	}
}

func formatTopFailureReasons(reasonCounts map[string]int, topN int) string {
	if len(reasonCounts) == 0 {
		return ""
	}
	if topN <= 0 {
		topN = 1
	}
	type reasonCount struct {
		reason string
		count  int
	}
	items := make([]reasonCount, 0, len(reasonCounts))
	for reason, count := range reasonCounts {
		items = append(items, reasonCount{reason: reason, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].reason < items[j].reason
		}
		return items[i].count > items[j].count
	})
	if topN > len(items) {
		topN = len(items)
	}
	parts := make([]string, 0, topN)
	for i := 0; i < topN; i++ {
		parts = append(parts, fmt.Sprintf("%dx %s", items[i].count, items[i].reason))
	}
	return strings.Join(parts, " | ")
}

func shouldLogVMFailedSummaryAtInfo(blockHash string) bool {
	if blockHash == "" {
		return true
	}
	_, loaded := vmFailedSummarySeen.LoadOrStore(blockHash, struct{}{})
	return !loaded
}
