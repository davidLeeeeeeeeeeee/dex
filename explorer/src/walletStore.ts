import { reactive } from 'vue'

export const wallet = reactive({ address: '', connected: false })

declare global {
    interface Window { frostbit?: any }
}

export async function connectWallet() {
    if (!window.frostbit) throw new Error('请先安装 FrostBit Wallet 扩展')
    const accounts: string[] = await window.frostbit.requestAccounts()
    if (accounts.length > 0) {
        wallet.address = accounts[0]
        wallet.connected = true
    }
}

export function disconnectWallet() {
    wallet.address = ''
    wallet.connected = false
}

export function shortAddr(addr: string) {
    return addr ? `${addr.slice(0, 8)}…${addr.slice(-6)}` : ''
}
