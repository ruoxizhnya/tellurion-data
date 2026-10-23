package ingest

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// 写面管线：`Fetch → Normalize → ValidatePoints → GatePoints → Deduplicate →
// BulkInsert`，五步语义对齐 core `tellurion:pkg/data/source/etl.go`（只读参考，
// 本仓不 import core）。
//
// 相对 core 只多一道 **GatePoints**（`docs/db-schema.md` §6.1 D3）：归一化
// 之后、去重之前，按该 data_type 声明的可见锚点逐点校验并填 `VisibleAt`。
// 它必须发生在去重之前——否则「重复点里第一条合规、第二条缺锚点」会被
// 去重吞掉，门形同虚设。

// ErrPipelineMisconfigured 是装配错误（nil registry / store / clock /
// normalizer）。这些是启动期 bug，不是数据问题，必须与数据拒收分开。
var ErrPipelineMisconfigured = errors.New("ingest: ETL pipeline misconfigured")

// Normalizer 把源侧原始条目转成统一点位。
//
// 契约：
//   - 载荷缺失时返回 `Data == nil`，由管线计为 skipped（不是写一行空数据）；
//   - 不读墙钟；`IngestTime` 由管线用注入的时钟统一赋值。
type Normalizer func(item DataItem, source, dataType string) UnifiedDataPoint

// BulkInserter 是写面 port。`internal/store.Writer` 提供 PG 实现；两者之间
// 的点位投影在装配层完成（store 不 import ingest，保持「存储不知道采集语义」）。
type BulkInserter interface {
	BulkInsert(ctx context.Context, dataType string, points []UnifiedDataPoint) (persisted, skipped int, err error)
}

// ETLPipeline 是一次同步作业的驱动面。字段导出是为装配期注入。
type ETLPipeline struct {
	Registry *Registry
	Store    BulkInserter
	// Now 是唯一时钟源（可复现性：同输入同字节）。nil 视为装配错误。
	Now func() time.Time
}

// NewETLPipeline 构造管线，时钟默认墙钟（生产）；测试用 WithClock 覆盖。
func NewETLPipeline(reg *Registry, store BulkInserter) *ETLPipeline {
	return &ETLPipeline{Registry: reg, Store: store, Now: time.Now}
}

// WithClock 返回注入了固定时钟的浅拷贝（测试 / 回放用）。
func (p *ETLPipeline) WithClock(now func() time.Time) *ETLPipeline {
	cp := *p
	cp.Now = now
	return &cp
}

// ProcessResult 是单次作业的计数。字段与 core 同名同义。
type ProcessResult struct {
	DataType  string
	Source    string
	Fetched   int
	Persisted int
	Skipped   int
	Duration  time.Duration
}

// Process 执行一次取数-写入。
//
// 失败分诊：
//   - 取数失败 ⇒ 原样返回（`ErrUnsupported` / 可重试耗尽）；
//   - 声明缺失 ⇒ ErrDeclarationMissing（不许给没登记可见语义的类型放行）；
//   - 任一点缺锚点 / 锚点坏 ⇒ 整批拒收（ErrVisibleAtMissing /
//     ErrAnchorInvalid），**store 不会被调用**；
//   - 写入冲突 ⇒ store 的 ErrContentConflict，整批回滚。
func (p *ETLPipeline) Process(ctx context.Context, req FetchRequest, normalizer Normalizer) (ProcessResult, error) {
	if p == nil || p.Registry == nil || p.Store == nil || p.Now == nil || normalizer == nil {
		return ProcessResult{}, ErrPipelineMisconfigured
	}

	started := time.Now()

	// 先查声明再取数：没登记的类型不该浪费一次上游调用。
	decl, err := p.Registry.DeclarationFor(req.DataType)
	if err != nil {
		return ProcessResult{}, err
	}

	resp, err := p.Registry.Fetch(ctx, req)
	if err != nil {
		return ProcessResult{}, err
	}

	result := ProcessResult{DataType: req.DataType, Source: resp.Source, Fetched: len(resp.Items)}
	ingestTime := p.Now().UTC()

	points := make([]UnifiedDataPoint, 0, len(resp.Items))
	for _, item := range resp.Items {
		point := normalizer(item, resp.Source, req.DataType)
		if point.Data == nil {
			result.Skipped++
			continue
		}
		if point.DataType == "" {
			point.DataType = req.DataType
		} else if point.DataType != req.DataType {
			return ProcessResult{}, fmt.Errorf("%w: normalizer returned data type %q for request %q",
				ErrPipelineMisconfigured, point.DataType, req.DataType)
		}
		point.IngestTime = ingestTime
		points = append(points, point)
	}

	valid, invalid := ValidatePoints(points)
	result.Skipped += invalid

	gated, err := GatePoints(decl, valid)
	if err != nil {
		// 整批拒收：GatePoints 已保证返回值是 nil，这里再显式不调 store。
		return ProcessResult{}, err
	}

	deduped, dup := DeduplicateWithCount(gated)
	result.Skipped += dup

	if len(deduped) == 0 {
		result.Duration = time.Since(started)
		return result, nil
	}

	persisted, storeSkipped, err := p.Store.BulkInsert(ctx, req.DataType, deduped)
	if err != nil {
		return ProcessResult{}, err
	}
	result.Persisted = persisted
	result.Skipped += storeSkipped
	result.Duration = time.Since(started)
	return result, nil
}
