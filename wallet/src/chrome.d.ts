// Chrome 扩展 API 类型声明
declare const chrome: {
    runtime: {
        sendMessage: (message: any, callback?: (response: any) => void) => void
        lastError: { message: string } | null
    }
    storage: {
        local: {
            get: (keys: string | string[] | null, callback?: (items: any) => void) => Promise<any>
            set: (items: object, callback?: () => void) => Promise<void>
        }
    }
}
