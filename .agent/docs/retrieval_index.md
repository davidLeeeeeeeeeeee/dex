# Retrieval Index

Use this index for quick semantic lookup.

## Execution and Tx Lifecycle

- tx kind dispatch: `vm/handlers.go`
- default tx handlers: `vm/default_handlers.go`
- pre-execute + commit flow: `vm/executor.go`
- tx receipt query path: `db/manage_tx_storage.go`, `handlers/handleGetData.go`

## Matching and Orderbook

- matching engine: `matching/match.go`
- order execution integration: `vm/order_handler.go`
- orderbook API: `handlers/handleOrderBook.go`
- order index keys: `keys/keys.go`

## Consensus and Sync

- consensus engine: `consensus/consensusEngine.go`
- query lifecycle: `consensus/queryManager.go`
- sync lifecycle: `consensus/syncManager.go`
- message dispatch: `consensus/messageHandler.go`

## FROST and DKG

- runtime manager: `frost/runtime/manager.go`
- withdraw worker: `frost/runtime/workers/withdraw_worker.go`
- transition worker: `frost/runtime/workers/transition_worker.go`
- roast coordinator/session: `frost/runtime/roast/`, `frost/runtime/session/`
- VM-side frost tx handlers: `vm/frost_*.go`
- frost API handlers: `handlers/frost_query_handlers.go`, `handlers/frost_admin_handlers.go`

## Witness

- witness service: `witness/service.go`
- stake manager: `witness/stake_manager.go`
- challenge/arbitration manager: `witness/challenge_manager.go`
- vm witness handlers: `vm/witness_handler.go`

## Storage

- db manager and write queue: `db/db.go`, `db/write_queue.go`
- key category routing: `keys/category.go`
- key schema: `keys/keys.go`

## Networking and Propagation

- sender manager: `sender/manager.go`
- send queue and retry/backoff: `sender/queue.go`
- gossip sender: `sender/gossip_sender.go`
- txpool queue and validation: `txpool/txpool_queue.go`

## Explorer

- explorer backend api: `cmd/explorer/main.go`
- explorer frost endpoints: `cmd/explorer/frost_handlers.go`
- explorer syncer: `cmd/explorer/syncer/syncer.go`
- frontend api client: `explorer/src/api.ts`
- frontend shell: `explorer/src/App.vue`

## Configuration and Bootstrap

- core config structs/defaults: `config/config.go`
- frost config: `config/frost.go`, `config/frost_default.json`
- witness config: `config/witness.go`, `config/witness_default.json`
- genesis config: `config/genesis.json`
- bootstrap logic: `cmd/main/bootstrap.go`

## Simulator

- simulated consensus components: `consensus/simulatedManager.go`, `consensus/simulatedProposer.go`, `consensus/simulatedTransport.go`, `consensus/simulatedBlockStore.go`
- simulator runner: `cmd/main/simulator.go`

## Types, Utils, Stats

- core types (Block, Message, Gossip, Snapshot): `types/`
- crypto utilities (VRF, BLS, hash, auth): `utils/`
- runtime stats/metrics: `stats/stats.go`, `stats/channel_stat.go`
- node metrics: `cmd/main/metrics.go`

## Protocol Buffers

- proto definition: `pb/data.proto`
- generated code: `pb/data.pb.go`
- extension helpers: `pb/anytx_ext.go`
