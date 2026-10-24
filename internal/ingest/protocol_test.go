package ingest

import (
	"reflect"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/dataprotocol"
)

// QT-40 X1a：本包不再持有点位镜像——类型必须就是契约那个类型。
// 若有人重新引入本地 struct（哪怕字段只差一个），这条当场红。
func TestUnifiedDataPointIsContractType(t *testing.T) {
	got := reflect.TypeOf(UnifiedDataPoint{})
	want := reflect.TypeOf(dataprotocol.UnifiedDataPoint{})
	if got != want {
		t.Fatalf("UnifiedDataPoint = %v, want the contract type %v", got, want)
	}
}

// 去重坐标只有一份实现：包内函数必须与契约方法同值（含 UTC 归一与纳秒）。
func TestDeduplicateKeyDelegatesToContract(t *testing.T) {
	p := UnifiedDataPoint{
		Symbol:    "600519.SH",
		DataType:  dataprotocol.DataTypeOHLCDaily,
		TradeTime: time.Date(2026, 5, 14, 9, 30, 0, 123456789, time.FixedZone("CST", 8*3600)),
	}
	const want = "600519.SH|ohlcv_daily|2026-05-14T01:30:00.123456789Z"
	if got := DeduplicateKey(p); got != want {
		t.Fatalf("DeduplicateKey() = %q, want %q", got, want)
	}
	if got := p.DeduplicateKey(); got != want {
		t.Fatalf("contract DeduplicateKey() = %q, want %q", got, want)
	}
}
