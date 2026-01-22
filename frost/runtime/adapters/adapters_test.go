package adapters

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFakeStateReaderAdapter 测试 FakeStateReader 适配器
func TestFakeStateReaderAdapter(t *testing.T) {
	reader := NewFakeStateReader()

	// 测试空状态
	data, exists, err := reader.Get("nonexistent")
	assert.NoError(t, err)
	assert.False(t, exists)
	assert.Nil(t, data)

	// 设置值
	reader.Set("key1", []byte("value1"))
	reader.Set("key2", []byte("value2"))
	reader.Set("prefix_a", []byte("a"))
	reader.Set("prefix_b", []byte("b"))

	// 测试 Get
	data, exists, err = reader.Get("key1")
	assert.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, []byte("value1"), data)

	// 测试 Scan
	var scanned []string
	err = reader.Scan("prefix_", func(k string, v []byte) bool {
		scanned = append(scanned, k)
		return true
	})
	assert.NoError(t, err)
	assert.Len(t, scanned, 2)
}

// TestFakeTxSubmitterAdapter 测试 FakeTxSubmitter 适配器
func TestFakeTxSubmitterAdapter(t *testing.T) {
	submitter := NewFakeTxSubmitter()

	// 测试提交
	txID, err := submitter.Submit("tx1")
	assert.NoError(t, err)
	assert.NotEmpty(t, txID)

	txID2, err := submitter.Submit("tx2")
	assert.NoError(t, err)
	assert.NotEmpty(t, txID2)

	// 验证提交计数
	assert.Equal(t, 2, submitter.SubmitCount())

	// 验证已提交列表
	submitted := submitter.GetSubmitted()
	assert.Len(t, submitted, 2)
	assert.Equal(t, "tx1", submitted[0])
	assert.Equal(t, "tx2", submitted[1])

	// 测试失败模式
	submitter.SetShouldFail(true, errors.New("test error"))
	_, err = submitter.Submit("tx3")
	assert.Error(t, err)
	assert.Equal(t, "test error", err.Error())

	// 测试重置
	submitter.Reset()
	assert.Equal(t, 0, submitter.SubmitCount())
	assert.Len(t, submitter.GetSubmitted(), 0)

	// 重置后可以正常提交
	txID3, err := submitter.Submit("tx4")
	assert.NoError(t, err)
	assert.NotEmpty(t, txID3)
}

// TestStateDBReaderAdapter 测试 StateDBReader 适配器
func TestStateDBReaderAdapter(t *testing.T) {
	// 使用 fake DB
	fakeDB := &fakeDBGetter{data: make(map[string][]byte)}
	fakeDB.data["key1"] = []byte("value1")
	fakeDB.data["key2"] = []byte("value2")

	reader := NewStateDBReader(fakeDB)

	// 测试 Get 存在的 key
	data, exists, err := reader.Get("key1")
	assert.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, []byte("value1"), data)

	// 测试 Get 不存在的 key
	data, exists, err = reader.Get("nonexistent")
	assert.NoError(t, err)
	assert.False(t, exists)
	assert.Nil(t, data)

	// 测试 Scan
	fakeDB.data["prefix_a"] = []byte("a")
	fakeDB.data["prefix_b"] = []byte("b")

	var scanned []string
	err = reader.Scan("prefix_", func(k string, v []byte) bool {
		scanned = append(scanned, k)
		return true
	})
	assert.NoError(t, err)
	assert.Len(t, scanned, 2)
}

// fakeDBGetter fake DB 实现
type fakeDBGetter struct {
	data map[string][]byte
}

func (f *fakeDBGetter) Get(key string) ([]byte, error) {
	if v, ok := f.data[key]; ok {
		return v, nil
	}
	return nil, errors.New("key not found")
}

func (f *fakeDBGetter) Scan(prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range f.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			result[k] = v
		}
	}
	return result, nil
}
