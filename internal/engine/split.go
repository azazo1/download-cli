package engine

// SafeZoneSize 是 safe zone 每次向前推进的最大字节数 (与 ABDM 相同, 1MiB).
const SafeZoneSize int64 = 128 * 8192

// SplitToRange 把 [0, size-1] 均分成至多 maxParts 段, 每段不小于 minPartSize
// (最后的余数分给前几段). 移植自 ABDM 的 splitToRange.
func SplitToRange(size, minPartSize int64, maxParts int) []PartState {
	if size <= 0 || minPartSize <= 0 || maxParts <= 0 {
		panic("SplitToRange: 参数必须为正数")
	}
	minParts := (size + minPartSize - 1) / minPartSize
	count := min(int64(maxParts), minParts)
	ideal := size / count
	rem := size % count
	out := make([]PartState, 0, count)
	start := int64(0)
	for i := int64(0); i < count; i++ {
		end := start + ideal - 1
		if i < rem {
			end++
		}
		out = append(out, PartState{From: start, To: end, Current: start})
		start = end + 1
	}
	return out
}

// canReadLocked 返回 worker 本次最多可写入的字节数, 必要时推进 safe zone.
// 返回 0 表示分片已经写满. 调用方需持有 Job.mu.
func (p *Part) canReadLocked(want int64) int64 {
	cur := p.current.Load()
	if p.to < 0 {
		// 长度未知时不存在分裂, 不限制.
		return want
	}
	avail := p.safeZone + 1 - cur
	if avail < want && p.safeZone < p.to {
		remaining := p.to - cur + 1
		p.safeZone = min(p.safeZone+min(remaining, SafeZoneSize), p.to)
		avail = p.safeZone + 1 - cur
	}
	return min(max(avail, 0), want)
}

// splitLocked 把 safe zone 之后的剩余区间对半切开, 返回后半段作为新分片.
// 新分片小于 minPartSize 时不切, 返回 nil. 调用方需持有 Job.mu.
//
// 因为 safeZone >= Current-1 恒成立, 已写入和正在写入的字节都不会落进新分片.
func (p *Part) splitLocked(minPartSize int64) *Part {
	if p.to < 0 {
		return nil
	}
	delta := p.to - p.safeZone
	if delta/2 < minPartSize {
		return nil
	}
	keepEnd := p.safeZone + delta/2 + delta%2
	if keepEnd+1 > p.to {
		return nil
	}
	np := newPart(keepEnd+1, p.to, keepEnd+1)
	p.to = keepEnd
	return np
}

// remainingLocked 返回 safe zone 之后尚未被占用的字节数, 用于挑选分裂对象.
func (p *Part) remainingLocked() int64 {
	if p.to < 0 {
		return 0
	}
	return p.to - p.safeZone
}
