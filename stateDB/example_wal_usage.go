// statedb/example_wal_usage.go
// 这是一个示例文件，展示如何使用 WAL 功能
// 在生产环境中可以删除此文件

package statedb

import (
	"fmt"
	"log"
)

// ExampleWithWAL 演示启用 WAL 的使用方式
func ExampleWithWAL() {
	// 1. 创建配置，启用 WAL
	cfg := Config{
		DataDir:         "stateDB/data",
		EpochSize:       40000,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          true, // 启用 WAL
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	// 2. 创建数据库实例
	// 如果存在 WAL 文件，会自动恢复
	db, err := New(cfg)
	if err != nil {
		log.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	// 3. 写入账户更新
	// WAL 会在写入内存前先持久化
	err = db.ApplyAccountUpdate(100, KVUpdate{
		Key:     "v1_account_0x1234",
		Value:   []byte("balance:1000"),
		Deleted: false,
	})
	if err != nil {
		log.Fatalf("Failed to apply update: %v", err)
	}

	// 4. 继续写入更多更新
	for i := 0; i < 100; i++ {
		err = db.ApplyAccountUpdate(uint64(100+i), KVUpdate{
			Key:     fmt.Sprintf("v1_account_0x%04x", i),
			Value:   []byte(fmt.Sprintf("balance:%d", i*100)),
			Deleted: false,
		})
		if err != nil {
			log.Fatalf("Failed to apply update: %v", err)
		}
	}

	// 5. 当 Epoch 结束时，调用 FlushAndRotate
	// 这会将内存 diff 持久化到 BadgerDB，并删除 WAL
	err = db.FlushAndRotate(39999)
	if err != nil {
		log.Fatalf("Failed to flush: %v", err)
	}

	fmt.Println("WAL example completed successfully")
}

// ExampleWithoutWAL 演示不启用 WAL 的使用方式
func ExampleWithoutWAL() {
	// 1. 创建配置，不启用 WAL
	cfg := Config{
		DataDir:         "stateDB/data",
		EpochSize:       40000,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          false, // 不启用 WAL
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	// 2. 创建数据库实例
	db, err := New(cfg)
	if err != nil {
		log.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	// 3. 写入账户更新
	// 直接写入内存，不经过 WAL
	err = db.ApplyAccountUpdate(100, KVUpdate{
		Key:     "v1_account_0x1234",
		Value:   []byte("balance:1000"),
		Deleted: false,
	})
	if err != nil {
		log.Fatalf("Failed to apply update: %v", err)
	}

	fmt.Println("Non-WAL example completed successfully")
}

// ExampleCrashRecovery 演示崩溃恢复场景
func ExampleCrashRecovery() {
	cfg := Config{
		DataDir:         "stateDB/data",
		EpochSize:       40000,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          true,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	// 第一次运行：写入数据但不 flush（模拟崩溃前的状态）
	{
		db, err := New(cfg)
		if err != nil {
			log.Fatalf("Failed to create DB: %v", err)
		}

		// 写入一些数据
		for i := 0; i < 10; i++ {
			err = db.ApplyAccountUpdate(uint64(100+i), KVUpdate{
				Key:     fmt.Sprintf("v1_account_0x%04x", i),
				Value:   []byte(fmt.Sprintf("balance:%d", i*100)),
				Deleted: false,
			})
			if err != nil {
				log.Fatalf("Failed to apply update: %v", err)
			}
		}

		// 模拟崩溃：直接关闭，不调用 FlushAndRotate
		// WAL 文件仍然存在
		db.Close()
		fmt.Println("Simulated crash - WAL file still exists")
	}

	// 第二次运行：重启并恢复
	{
		db, err := New(cfg)
		if err != nil {
			log.Fatalf("Failed to create DB: %v", err)
		}
		defer db.Close()

		// 此时 WAL 已经被回放，内存窗口已恢复
		fmt.Println("Recovery completed - data restored from WAL")

		// 可以继续正常操作
		err = db.ApplyAccountUpdate(110, KVUpdate{
			Key:     "v1_account_0x9999",
			Value:   []byte("balance:9999"),
			Deleted: false,
		})
		if err != nil {
			log.Fatalf("Failed to apply update: %v", err)
		}

		fmt.Println("Crash recovery example completed successfully")
	}
}
