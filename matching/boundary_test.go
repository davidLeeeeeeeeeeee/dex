// 文件名: boundary_test.go
package matching

import (
	"testing"

	"github.com/shopspring/decimal"
)

// TestPriceBoundary 测试价格的边界情况
func TestPriceBoundary(t *testing.T) {
	orderBook := NewOrderBookWithSink(nil)

	// 几个边界值
	testPrices := []struct {
		priceStr  string
		wantError bool
	}{
		{"1e-34", true},  // 小于 1e-33，预期报错
		{"1e-33", false}, // 恰好是最小值，应该成功
		{"1", false},     // 正常范围
		{"1e33", false},  // 恰好是最大值，应该成功
		{"10000000000000000000000000000000000000000000000000000000000000000000000000000000000000", true}, // 大于 1e33，预期报错
	}

	for _, tc := range testPrices {
		priceDec, _ := decimal.NewFromString(tc.priceStr)
		o := &Order{
			ID:     "test_" + tc.priceStr,
			Side:   BUY, // 测试用买单
			Price:  priceDec,
			Amount: decimal.NewFromFloat(100), // 随便给一个数量
		}
		err := orderBook.AddOrder(o)
		if tc.wantError && err == nil {
			t.Errorf("price=%s, EXPECT error but got nil", tc.priceStr)
		} else if !tc.wantError && err != nil {
			t.Errorf("price=%s, EXPECT no error but got err=%v", tc.priceStr, err)
		}
	}
}
