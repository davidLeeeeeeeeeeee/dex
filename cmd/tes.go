package main

import (
	"dex/config"
	"dex/consensus"
	"dex/db"
	"dex/logs"
	"fmt"
)

func main() {
	consensus.RunLoop()
	// Create a logger for the database manager
	logger := logs.NewNodeLogger("tes", 1000)
	dbMgr, err := db.NewManager("data/data_node_0", logger)
	if err != nil {
		logs.Error("CheckAuth GetInstance err : %v", err)
		return
	}
	defer dbMgr.Close()

	cfg := config.DefaultConfig()
	maxBytes := cfg.Database.WriteBatchSoftLimit
	maxCount := cfg.Database.MaxCountPerTxn
	fmt.Println("maxBytes:", maxBytes)
	fmt.Println("maxCount:", maxCount)
}
