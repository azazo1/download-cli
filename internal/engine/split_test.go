package engine

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// assertCover 断言分片按 From 排序后连续且恰好覆盖 [0, size-1].
func assertCover(t *testing.T, size int64, parts []PartState) {
	t.Helper()
	slices.SortFunc(parts, func(a, b PartState) int { return int(min(max(a.From-b.From, -1), 1)) })
	next := int64(0)
	for _, p := range parts {
		if p.From != next || p.To < p.From {
			t.Fatalf("分片不连续: 期望起点 %d, 实际 %+v (全部 %+v)", next, p, parts)
		}
		next = p.To + 1
	}
	if next != size {
		t.Fatalf("分片没有覆盖到末尾: 结束于 %d, 大小 %d", next, size)
	}
}

func TestSplitToRange(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		size := r.Int64N(50_000_000) + 1
		minPart := r.Int64N(2_000_000) + 1
		maxParts := r.IntN(64) + 1
		parts := SplitToRange(size, minPart, maxParts)
		want := min(int64(maxParts), (size+minPart-1)/minPart)
		if int64(len(parts)) != want {
			t.Fatalf("size=%d min=%d max=%d: 分片数 %d, 期望 %d", size, minPart, maxParts, len(parts), want)
		}
		assertCover(t, size, parts)
	}
}

// TestDynamicSplit 随机推进写入与 safe zone 并反复分裂, 断言区间始终无重叠无遗漏,
// 新分片不会落入已写入或 safe zone 的范围, 且长度不小于 minPartSize.
func TestDynamicSplit(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for range 300 {
		size := r.Int64N(100_000_000) + 1
		minPart := r.Int64N(4_000_000) + 1024
		parts := []*Part{}
		for _, s := range SplitToRange(size, minPart, r.IntN(8)+1) {
			parts = append(parts, newPart(s.From, s.To, s.Current))
		}
		for range 200 {
			p := parts[r.IntN(len(parts))]
			if r.IntN(2) == 0 {
				// 模拟 worker 写入: 先取可写量, 再推进 current.
				if n := p.canReadLocked(r.Int64N(4*SafeZoneSize) + 1); n > 0 {
					p.current.Add(r.Int64N(n) + 1)
				}
				continue
			}
			oldTo, safe := p.to, p.safeZone
			np := p.splitLocked(minPart)
			if np == nil {
				continue
			}
			if np.From <= safe || np.From <= p.Current()-1 {
				t.Fatalf("新分片 %v 落入 safe zone %d / current %d", np, safe, p.Current())
			}
			if np.to != oldTo || p.to != np.From-1 {
				t.Fatalf("分裂边界错误: 原 to=%d, 保留 %v, 新 %v", oldTo, p, np)
			}
			if np.to-np.From+1 < minPart {
				t.Fatalf("新分片 %v 小于最小分片 %d", np, minPart)
			}
			parts = append(parts, np)
		}
		states := make([]PartState, len(parts))
		for i, p := range parts {
			if p.Current() > p.to+1 || p.safeZone > p.to {
				t.Fatalf("分片写越界: %v current=%d safe=%d", p, p.Current(), p.safeZone)
			}
			states[i] = p.stateLocked()
		}
		assertCover(t, size, states)
	}
}
