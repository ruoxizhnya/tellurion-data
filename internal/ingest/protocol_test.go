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

// QT-40 X1a（常量腿）：15 个 DataType* 必须是契约常量的别名。
//
// 编译期自证的是「别名指向契约符号」；这条测试守住回归——把某个常量改回
// 本地字面量（且契约随后改值）时当场红，避免 wire 取值出现第二份副本。
func TestDataTypeConstantsMatchContract(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"DataTypeOHLCDaily", DataTypeOHLCDaily, dataprotocol.DataTypeOHLCDaily},
		{"DataTypeOHLCMinute", DataTypeOHLCMinute, dataprotocol.DataTypeOHLCMinute},
		{"DataTypeRealtime", DataTypeRealtime, dataprotocol.DataTypeRealtime},
		{"DataTypeCapitalFlow", DataTypeCapitalFlow, dataprotocol.DataTypeCapitalFlow},
		{"DataTypeFundamental", DataTypeFundamental, dataprotocol.DataTypeFundamental},
		{"DataTypeSectors", DataTypeSectors, dataprotocol.DataTypeSectors},
		{"DataTypeStockSector", DataTypeStockSector, dataprotocol.DataTypeStockSector},
		{"DataTypeTopList", DataTypeTopList, dataprotocol.DataTypeTopList},
		{"DataTypeLimitUpPool", DataTypeLimitUpPool, dataprotocol.DataTypeLimitUpPool},
		{"DataTypeAnnounce", DataTypeAnnounce, dataprotocol.DataTypeAnnounce},
		{"DataTypeNews", DataTypeNews, dataprotocol.DataTypeNews},
		{"DataTypeHotSearch", DataTypeHotSearch, dataprotocol.DataTypeHotSearch},
		{"DataTypeGlobalOHLCV", DataTypeGlobalOHLCV, dataprotocol.DataTypeGlobalOHLCV},
		{"DataTypeMacro", DataTypeMacro, dataprotocol.DataTypeMacro},
		{"DataTypeDailyBasic", DataTypeDailyBasic, dataprotocol.DataTypeDailyBasic},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want contract value %q", tc.name, tc.got, tc.want)
		}
	}
}
