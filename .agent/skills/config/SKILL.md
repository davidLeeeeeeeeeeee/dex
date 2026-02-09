---
name: Configuration
description: Node/system configuration map including network, txpool, sender, frost, witness, and genesis bootstrap settings.
triggers:
  - config
  - configuration
  - genesis
  - frost config
  - witness config
---

# Configuration Skill

Use this skill when changing defaults, boot parameters, or JSON config behavior.

## Source Map

- Core config structs/defaults: `config/config.go`
- FROST config structs/defaults: `config/frost.go`
- Witness config structs/defaults: `config/witness.go`
- Config files:
  - `config/frost_default.json`
  - `config/witness_default.json`
  - `config/genesis.json`
- Bootstrap application logic: `cmd/main/bootstrap.go`

## Key Facts

- `config.DefaultConfig()` is the global fallback.
- Runtime calls `LoadFromFile("")`, which currently loads:
  - `config/frost_default.json`
  - `config/witness_default.json`
- Genesis balances/tokens are applied in `cmd/main/bootstrap.go` via:
  - `applyGenesisBalances`
  - `initGenesisTokens`

## Typical Tasks

1. Tune network/sender behavior:
   - edit `config/config.go` (`NetworkConfig`, `SenderConfig`, `TxPoolConfig`)
2. Tune FROST thresholds/timeouts/chains:
   - edit `config/frost.go` and `config/frost_default.json`
3. Tune witness voting/challenge windows:
   - edit `config/witness.go` and `config/witness_default.json`
4. Change genesis token/allocation defaults:
   - edit `config/genesis.json` and bootstrap usage in `cmd/main/bootstrap.go`

## Quick Commands

```bash
rg "type .*Config struct|Default.*Config|Load.*Config" config
rg "LoadFromFile|LoadGenesisConfig|applyGenesisBalances|initGenesisTokens" cmd/main
```

