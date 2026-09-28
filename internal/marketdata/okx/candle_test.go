package okx

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// 下面几行是从真实 OKX 接口抓到的原始数据（REST /market/candles 与 WS candle 频道
// 推送用的是同一套数组编码），不是手写的示例——用真实数据测解析，能测出字段顺序、
// confirm 语义这类光看文档容易搞错的细节。
var (
	realConfirmedRow   = []string{"1787180400000", "69264.7", "69462.5", "69160.6", "69337.1", "333.0511483", "23087731.217311397", "23087731.217311397", "1"}
	realUnconfirmedRow = []string{"1787184000000", "69337.2", "69599.4", "69319.9", "69548.1", "115.18879798", "8006796.381173947", "8006796.381173947", "0"}
)

func TestParseCandleRowConfirmed(t *testing.T) {
	c, confirmed, err := parseCandleRow(realConfirmedRow, types.TF1h)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if !confirmed {
		t.Error("confirm=1 应解析为已收盘")
	}

	wantOpenTime := time.UnixMilli(1787180400000).UTC()
	if !c.OpenTime.Equal(wantOpenTime) {
		t.Errorf("OpenTime = %s，期望 %s", c.OpenTime, wantOpenTime)
	}
	if !c.CloseTime.Equal(wantOpenTime.Add(time.Hour)) {
		t.Errorf("CloseTime = %s，期望开盘时间 + 1 小时", c.CloseTime)
	}
	if !c.Open.Equal(decimal.RequireFromString("69264.7")) {
		t.Errorf("Open = %s，期望 69264.7", c.Open)
	}
	if !c.High.Equal(decimal.RequireFromString("69462.5")) {
		t.Errorf("High = %s，期望 69462.5", c.High)
	}
	if !c.Low.Equal(decimal.RequireFromString("69160.6")) {
		t.Errorf("Low = %s，期望 69160.6", c.Low)
	}
	if !c.Close.Equal(decimal.RequireFromString("69337.1")) {
		t.Errorf("Close = %s，期望 69337.1", c.Close)
	}
	if !c.Volume.Equal(decimal.RequireFromString("333.0511483")) {
		t.Errorf("Volume = %s，期望 333.0511483", c.Volume)
	}
	// OKX 不提供主动买卖量拆分，必须保持零值，不能瞎填。
	if !c.TakerBuyVolume.IsZero() {
		t.Errorf("TakerBuyVolume = %s，期望零值（OKX 不提供这项数据）", c.TakerBuyVolume)
	}
}

func TestParseCandleRowUnconfirmed(t *testing.T) {
	_, confirmed, err := parseCandleRow(realUnconfirmedRow, types.TF1h)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if confirmed {
		t.Error("confirm=0 不该被解析为已收盘")
	}
}

func TestParseCandleRowRejectsTooFewFields(t *testing.T) {
	if _, _, err := parseCandleRow([]string{"1", "2", "3"}, types.TF1h); err == nil {
		t.Fatal("字段不够应报错")
	}
}

func TestParseCandleRowRejectsMalformedPrice(t *testing.T) {
	row := []string{"1787180400000", "not-a-number", "69462.5", "69160.6", "69337.1", "333.05", "0", "0", "1"}
	if _, _, err := parseCandleRow(row, types.TF1h); err == nil {
		t.Fatal("非法价格字段应报错")
	}
}

func TestParseCandleRowRejectsMalformedTimestamp(t *testing.T) {
	row := []string{"not-a-timestamp", "1", "2", "3", "4", "5", "0", "0", "1"}
	if _, _, err := parseCandleRow(row, types.TF1h); err == nil {
		t.Fatal("非法时间戳应报错")
	}
}
