package handlers

import (
	"dex/pb"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// HandlePendingBlocks 获取候选区块详情及共识状态
func (hm *HandlerManager) HandlePendingBlocks(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandlePendingBlocks")

	blocks := hm.consensusManager.GetPendingBlocks()
	heightStates := hm.consensusManager.GetPendingHeightsState()

	// 构建高度到共识状态的映射
	stateMap := make(map[uint64]struct {
		preference string
		confidence int
		votes      map[string]int
	})
	for _, hs := range heightStates {
		stateMap[hs.Height] = struct {
			preference string
			confidence int
			votes      map[string]int
		}{
			preference: hs.Preference,
			confidence: hs.Confidence,
			votes:      hs.LastVotes,
		}
	}

	resp := &pb.PendingBlocksResponse{
		Blocks:       make([]*pb.PendingBlockInfo, 0, len(blocks)),
		HeightStates: make([]*pb.HeightConsensusState, 0, len(heightStates)),
	}

	for _, block := range blocks {
		state, hasState := stateMap[block.Header.Height]
		var votes int32
		var isPreferred bool
		if hasState {
			if v, ok := state.votes[block.ID]; ok {
				votes = int32(v)
			}
			isPreferred = state.preference == block.ID
		}

		resp.Blocks = append(resp.Blocks, &pb.PendingBlockInfo{
			BlockId:     block.ID,
			Height:      block.Header.Height,
			ParentId:    block.Header.ParentID,
			Proposer:    block.Header.Proposer,
			Window:      int32(block.Header.Window),
			Votes:       votes,
			IsPreferred: isPreferred,
		})
	}

	for _, hs := range heightStates {
		resp.HeightStates = append(resp.HeightStates, &pb.HeightConsensusState{
			Height:     hs.Height,
			Preference: hs.Preference,
			Confidence: int32(hs.Confidence),
			Finalized:  hs.Finalized,
		})
	}

	data, _ := proto.Marshal(resp)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
}
