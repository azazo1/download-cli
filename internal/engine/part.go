// Package engine 实现分片多连接下载与动态分片.
//
// 动态分片算法参考 AB Download Manager (Apache-2.0) 的 PartSplitSupport:
// 每个连接只能写入自己的 safe zone, 空闲连接出现时, 把剩余最多的分片
// 从 safe zone 之后的中点切开, 后半段交给空闲连接.
package engine

import (
	"fmt"
	"sync/atomic"
)

// PartState 是分片的可持久化快照. To 为 -1 表示长度未知.
type PartState struct {
	From    int64 `json:"from"`
	To      int64 `json:"to"`
	Current int64 `json:"current"`
}

// Part 是运行期分片, 表示闭区间 [From, To].
//
// 并发约定: To 与 safeZone 只在 Job.mu 内读写; Current 只由所属 worker 推进,
// 其它 goroutine 通过原子操作读取.
type Part struct {
	From int64
	// to 为 -1 表示长度未知 (单连接盲下).
	to       int64
	current  atomic.Int64
	safeZone int64
}

func newPart(from, to, current int64) *Part {
	p := &Part{From: from, to: to}
	p.current.Store(current)
	p.safeZone = current - 1
	return p
}

// Current 返回下一个待写入字节的偏移.
func (p *Part) Current() int64 { return p.current.Load() }

// downloaded 返回本分片已写入的字节数.
func (p *Part) downloaded() int64 { return p.current.Load() - p.From }

// completedLocked 判断分片是否完成, 调用方需持有 Job.mu.
func (p *Part) completedLocked() bool {
	return p.to >= 0 && p.current.Load() == p.to+1
}

func (p *Part) stateLocked() PartState {
	return PartState{From: p.From, To: p.to, Current: p.current.Load()}
}

// rangeStr 返回分片区间的可读形式. 会读取 to, 调用方必须持有 Job.mu.
// 锁外需要标签时使用 Job.label.
func (p *Part) rangeStr() string {
	return fmt.Sprintf("[%d-%d]", p.From, p.to)
}
