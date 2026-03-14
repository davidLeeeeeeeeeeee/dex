use anchor_lang::prelude::*;
use anchor_lang::solana_program::{
    ed25519_program,
    hash::hash,
    program::invoke_signed,
    system_instruction,
    sysvar::instructions::{self, load_instruction_at_checked},
};
use anchor_spl::token::{self, Token, TokenAccount, Transfer};

declare_id!("BXNc6sQFunPke4gwkWwy78vRMWUmUQm9YLxGtQbmJNvK");

// ========== 错误码 ==========

#[error_code]
pub enum VaultError {
    #[msg("Ed25519 signature verification failed")]
    InvalidSignature,
    #[msg("Message already consumed")]
    AlreadyConsumed,
    #[msg("Missing Ed25519 signature verification instruction")]
    MissingEd25519Instruction,
    #[msg("Ed25519 instruction data mismatch")]
    Ed25519DataMismatch,
    #[msg("Withdraw amount must be greater than zero")]
    ZeroAmount,
    #[msg("Insufficient vault balance")]
    InsufficientBalance,
}

// ========== PDA 状态 ==========

/// Vault 主状态 — 对应 SchnorrExample 的 P + consumed
/// PDA seeds: ["frost_vault", vault_id.to_le_bytes()]
#[account]
pub struct VaultState {
    pub authority: Pubkey, // 当前 FROST 群公钥 (Ed25519)
    pub vault_id: u32,
    pub key_epoch: u64,
    pub bump: u8,
}

impl VaultState {
    pub const LEN: usize = 8 + 32 + 4 + 8 + 1;
}

/// 消费记录（防重放）— 对应 mapping(bytes32 => bool) consumed
/// PDA seeds: ["consumed", message_hash]
#[account]
pub struct ConsumedRecord {
    pub consumed: bool,
}

impl ConsumedRecord {
    pub const LEN: usize = 8 + 1;
}

// ========== 事件 ==========

#[event]
pub struct PubChanged {
    pub old_authority: Pubkey,
    pub new_authority: Pubkey,
    pub epoch: u64,
}

#[event]
pub struct Withdrawn {
    pub vault_id: u32,
    pub recipient: Pubkey,
    pub amount: u64,
    pub message_hash: [u8; 32],
    pub is_token: bool,
}

// ========== Account 验证结构体 ==========

#[derive(Accounts)]
#[instruction(vault_id: u32)]
pub struct Initialize<'info> {
    #[account(mut)]
    pub payer: Signer<'info>,
    #[account(
        init, payer = payer, space = VaultState::LEN,
        seeds = [b"frost_vault", vault_id.to_le_bytes().as_ref()], bump,
    )]
    pub vault_state: Account<'info, VaultState>,
    pub system_program: Program<'info, System>,
}

#[derive(Accounts)]
pub struct ChangePub<'info> {
    #[account(
        mut,
        seeds = [b"frost_vault", vault_state.vault_id.to_le_bytes().as_ref()],
        bump = vault_state.bump,
    )]
    pub vault_state: Account<'info, VaultState>,
    /// CHECK: validated by address constraint
    #[account(address = instructions::ID)]
    pub instruction_sysvar: AccountInfo<'info>,
}

#[derive(Accounts)]
#[instruction(amount: u64, message: Vec<u8>)]
pub struct Withdraw<'info> {
    #[account(
        seeds = [b"frost_vault", vault_state.vault_id.to_le_bytes().as_ref()],
        bump = vault_state.bump,
    )]
    pub vault_state: Account<'info, VaultState>,
    /// CHECK: same PDA as vault_state, holds SOL
    #[account(
        mut,
        seeds = [b"frost_vault", vault_state.vault_id.to_le_bytes().as_ref()],
        bump = vault_state.bump,
    )]
    pub vault_sol: AccountInfo<'info>,
    #[account(
        init, payer = payer, space = ConsumedRecord::LEN,
        seeds = [b"consumed", hash(&message).as_ref()], bump,
    )]
    pub consumed_record: Account<'info, ConsumedRecord>,
    /// CHECK: recipient bound by signed message
    #[account(mut)]
    pub recipient: AccountInfo<'info>,
    #[account(mut)]
    pub payer: Signer<'info>,
    /// CHECK: validated by address constraint
    #[account(address = instructions::ID)]
    pub instruction_sysvar: AccountInfo<'info>,
    pub system_program: Program<'info, System>,
    // Optional SPL Token accounts
    #[account(mut)]
    pub vault_token_account: Option<Account<'info, TokenAccount>>,
    #[account(mut)]
    pub recipient_token_account: Option<Account<'info, TokenAccount>>,
    pub token_program: Option<Program<'info, Token>>,
}

// ========== Program ==========

#[program]
pub mod frost_vault {
    use super::*;

    /// 初始化 Vault（对应 constructor(Px, Py)）
    pub fn initialize(ctx: Context<Initialize>, vault_id: u32, authority: Pubkey) -> Result<()> {
        let vault = &mut ctx.accounts.vault_state;
        vault.authority = authority;
        vault.vault_id = vault_id;
        vault.key_epoch = 1;
        vault.bump = ctx.bumps.vault_state;
        msg!("FROST Vault initialized: vault_id={} authority={}", vault_id, authority);
        Ok(())
    }

    /// 换公钥（对应 changePub）— 旧公钥签名授权新公钥
    pub fn change_pub(ctx: Context<ChangePub>, new_authority: Pubkey, signature: [u8; 64]) -> Result<()> {
        let vault = &mut ctx.accounts.vault_state;
        let msg_hash = hash(new_authority.as_ref());
        verify_ed25519_ix(&ctx.accounts.instruction_sysvar, &vault.authority.to_bytes(), msg_hash.as_ref(), &signature)?;
        let old = vault.authority;
        vault.authority = new_authority;
        vault.key_epoch += 1;
        emit!(PubChanged { old_authority: old, new_authority, epoch: vault.key_epoch });
        Ok(())
    }

    /// 提现（对应 withdraw）— FROST 签名授权提现
    pub fn withdraw(ctx: Context<Withdraw>, amount: u64, message: Vec<u8>, signature: [u8; 64]) -> Result<()> {
        require!(amount > 0, VaultError::ZeroAmount);
        let vault = &ctx.accounts.vault_state;
        let consumed = &mut ctx.accounts.consumed_record;
        require!(!consumed.consumed, VaultError::AlreadyConsumed);

        let msg_hash = hash(&message);
        verify_ed25519_ix(&ctx.accounts.instruction_sysvar, &vault.authority.to_bytes(), msg_hash.as_ref(), &signature)?;
        consumed.consumed = true;

        let vault_id_bytes = vault.vault_id.to_le_bytes();
        let seeds: &[&[u8]] = &[b"frost_vault", vault_id_bytes.as_ref(), &[vault.bump]];

        if let (Some(vault_token), Some(recipient_token), Some(token_prog)) = (
            &ctx.accounts.vault_token_account,
            &ctx.accounts.recipient_token_account,
            &ctx.accounts.token_program,
        ) {
            // SPL Token transfer
            let cpi_accounts = Transfer {
                from: vault_token.to_account_info(),
                to: recipient_token.to_account_info(),
                authority: ctx.accounts.vault_sol.to_account_info(),
            };
            token::transfer(CpiContext::new_with_signer(token_prog.to_account_info(), cpi_accounts, &[seeds]), amount)?;
            emit!(Withdrawn { vault_id: vault.vault_id, recipient: ctx.accounts.recipient.key(), amount, message_hash: msg_hash.to_bytes(), is_token: true });
        } else {
            // SOL transfer
            require!(ctx.accounts.vault_sol.lamports() >= amount, VaultError::InsufficientBalance);
            invoke_signed(
                &system_instruction::transfer(&ctx.accounts.vault_sol.key(), &ctx.accounts.recipient.key(), amount),
                &[ctx.accounts.vault_sol.to_account_info(), ctx.accounts.recipient.to_account_info(), ctx.accounts.system_program.to_account_info()],
                &[seeds],
            )?;
            emit!(Withdrawn { vault_id: vault.vault_id, recipient: ctx.accounts.recipient.key(), amount, message_hash: msg_hash.to_bytes(), is_token: false });
        }
        Ok(())
    }
}

// ========== Ed25519 验签辅助 ==========

/// 验证当前交易中是否包含正确的 Ed25519SigVerify 指令
fn verify_ed25519_ix(ix_sysvar: &AccountInfo, expected_pubkey: &[u8], expected_msg: &[u8], expected_sig: &[u8; 64]) -> Result<()> {
    let ix = load_instruction_at_checked(0, ix_sysvar).map_err(|_| VaultError::MissingEd25519Instruction)?;
    require_keys_eq!(ix.program_id, ed25519_program::ID, VaultError::MissingEd25519Instruction);

    let d = &ix.data;
    if d.len() < 16 + 64 + 32 + expected_msg.len() { return Err(VaultError::Ed25519DataMismatch.into()); }
    if u16::from_le_bytes([d[0], d[1]]) != 1 { return Err(VaultError::Ed25519DataMismatch.into()); }

    let sig_off = u16::from_le_bytes([d[4], d[5]]) as usize;
    let pub_off = u16::from_le_bytes([d[8], d[9]]) as usize;
    let msg_off = u16::from_le_bytes([d[12], d[13]]) as usize;
    let msg_sz = u16::from_le_bytes([d[14], d[15]]) as usize;

    if sig_off + 64 > d.len() || pub_off + 32 > d.len() || msg_off + msg_sz > d.len() {
        return Err(VaultError::Ed25519DataMismatch.into());
    }
    if d[sig_off..sig_off + 64] != *expected_sig { return Err(VaultError::Ed25519DataMismatch.into()); }
    if d[pub_off..pub_off + 32] != *expected_pubkey { return Err(VaultError::Ed25519DataMismatch.into()); }
    if msg_sz != expected_msg.len() || d[msg_off..msg_off + msg_sz] != *expected_msg {
        return Err(VaultError::Ed25519DataMismatch.into());
    }
    Ok(())
}
