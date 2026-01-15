package syncer

import (
	"context"
	"dex/pb"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// setStatus 设置状态
func (s *Syncer) setStatus(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

// getLatestHeight 获取节点最新区块高度
func (s *Syncer) getLatestHeight(ctx context.Context, node string) (uint64, error) {
	// 使用 /heightquery 接口获取高度
	url := fmt.Sprintf("%s/heightquery", node)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("heightquery request failed: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	// 解析 protobuf 响应
	var heightResp pb.HeightResponse
	if err := proto.Unmarshal(body, &heightResp); err != nil {
		return 0, fmt.Errorf("failed to unmarshal height response: %v", err)
	}

	// 返回 LastAcceptedHeight（已确认的高度）
	return heightResp.LastAcceptedHeight, nil
}

// syncBlock 同步单个区块
func (s *Syncer) syncBlock(ctx context.Context, node string, height uint64) error {
	block, err := s.fetchBlock(ctx, node, height)
	if err != nil {
		return err
	}
	return s.indexDB.IndexBlock(block)
}

// fetchBlock 从节点获取区块
func (s *Syncer) fetchBlock(ctx context.Context, node string, height uint64) (*pb.Block, error) {
	url := fmt.Sprintf("%s/getblock", node)

	// 构建 protobuf 请求
	reqProto := &pb.GetBlockRequest{Height: height}
	reqBody, err := proto.Marshal(reqProto)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Body = io.NopCloser(bytesReader(reqBody))

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getblock failed: %d - %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var blockResp pb.GetBlockResponse
	if err := proto.Unmarshal(body, &blockResp); err != nil {
		return nil, err
	}

	if blockResp.Error != "" {
		return nil, fmt.Errorf("getblock error: %s", blockResp.Error)
	}

	return blockResp.Block, nil
}

type bytesReaderWrapper struct {
	*bytesBuffer
}

type bytesBuffer struct {
	data []byte
	pos  int
}

func bytesReader(data []byte) io.Reader {
	return &bytesBuffer{data: data}
}

func (b *bytesBuffer) Read(p []byte) (n int, err error) {
	if b.pos >= len(b.data) {
		return 0, io.EOF
	}
	n = copy(p, b.data[b.pos:])
	b.pos += n
	return n, nil
}
