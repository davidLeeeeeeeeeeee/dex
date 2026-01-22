// statedb/wal.go
package statedb

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
)

const walMagic uint32 = 0x51A1F00D // 任意
var enc = binary.BigEndian

type WalRecord struct {
	Epoch uint64
	Op    byte // 0=SET, 1=DEL
	Key   []byte
	Value []byte // DEL 时可为空
}

type WAL struct {
	f *os.File
	w *bufio.Writer
}

func OpenWAL(dir string, epoch uint64) (*WAL, error) {
	walDir := filepath.Join(dir, "wal")
	_ = os.MkdirAll(walDir, 0o755)
	// 一个简单命名：wal_E{epoch}.log
	// 生产可再加段号、时间等
	fn := filepath.Join(walDir, fmt.Sprintf("wal_E%020d.log", epoch))
	f, err := os.OpenFile(fn, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &WAL{f: f, w: bufio.NewWriterSize(f, 1<<20)}, nil
}

func (w *WAL) AppendBatch(records []WalRecord) error {
	for _, r := range records {
		if err := writeOne(w.w, r); err != nil {
			return err
		}
	}
	if err := w.w.Flush(); err != nil {
		return err
	}
	// 强一致：每批 fsync；若要组提交，可把 Sync 放到定时器
	return w.f.Sync()
}

func writeOne(w io.Writer, r WalRecord) error {
	// 头部: magic(4) + epoch(8) + op(1) + klen(4) + vlen(4)
	hdr := make([]byte, 4+8+1+4+4)
	enc.PutUint32(hdr[0:4], walMagic)
	enc.PutUint64(hdr[4:12], r.Epoch)
	hdr[12] = r.Op
	enc.PutUint32(hdr[13:17], uint32(len(r.Key)))
	enc.PutUint32(hdr[17:21], uint32(len(r.Value)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if _, err := w.Write(r.Key); err != nil {
		return err
	}
	if _, err := w.Write(r.Value); err != nil {
		return err
	}

	// CRC 覆盖头+数据（或仅数据，二选一即可）
	crc := crc32.ChecksumIEEE(hdr)
	crc = crc32.Update(crc, crc32.IEEETable, r.Key)
	crc = crc32.Update(crc, crc32.IEEETable, r.Value)
	cb := make([]byte, 4)
	enc.PutUint32(cb, crc)
	_, err := w.Write(cb)
	return err
}

func (w *WAL) Close() error {
	_ = w.w.Flush()
	return w.f.Close()
}

// 启动恢复：把 WAL 全部回放进内存 diff
func ReplayWAL(fn string, apply func(WalRecord) error) error {
	f, err := os.Open(fn)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		// 读头
		hdr := make([]byte, 21)
		if _, err := io.ReadFull(r, hdr); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		if enc.Uint32(hdr[0:4]) != walMagic {
			return io.ErrUnexpectedEOF
		}
		rec := WalRecord{
			Epoch: enc.Uint64(hdr[4:12]),
			Op:    hdr[12],
		}
		klen := enc.Uint32(hdr[13:17])
		vlen := enc.Uint32(hdr[17:21])
		rec.Key = make([]byte, klen)
		rec.Value = make([]byte, vlen)
		if _, err := io.ReadFull(r, rec.Key); err != nil {
			return err
		}
		if _, err := io.ReadFull(r, rec.Value); err != nil {
			return err
		}
		// 校验
		crcb := make([]byte, 4)
		if _, err := io.ReadFull(r, crcb); err != nil {
			return err
		}
		want := enc.Uint32(crcb)
		crc := crc32.ChecksumIEEE(hdr)
		crc = crc32.Update(crc, crc32.IEEETable, rec.Key)
		crc = crc32.Update(crc, crc32.IEEETable, rec.Value)
		if crc != want {
			return io.ErrUnexpectedEOF
		}

		if err := apply(rec); err != nil {
			return err
		}
	}
}

// 获取 WAL 文件路径
func GetWALPath(dir string, epoch uint64) string {
	walDir := filepath.Join(dir, "wal")
	return filepath.Join(walDir, fmt.Sprintf("wal_E%020d.log", epoch))
}

// 删除指定 Epoch 的 WAL 文件
func RemoveWAL(dir string, epoch uint64) error {
	fn := GetWALPath(dir, epoch)
	err := os.Remove(fn)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
