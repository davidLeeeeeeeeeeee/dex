---
name: Wallet
description: FrostBit Wallet Chrome Extension architecture, content script injection, secure popup approval, and static protobuf translation.
triggers:
  - wallet
  - extension
  - popup
  - approval
  - content script
  - manifest
  - frostbit provider
---

# Wallet Skill

This skill documents the architecture and development guidelines for the FrostBit Wallet Chrome Extension (Manifest V3).

## Core Architecture

The wallet extension uses a standard multiple-context architecture mandated by Manifest V3:

1. **Background Service Worker (`background/service_worker.js`)**: 
   - Non-persistent background script responsible for memory state (unlocked status, plaintext mnemonic).
   - Serves as the central message router handling all secure logic (key derivation, signing, sending).
   - Manages the popup window lifecycle for transaction approvals.
2. **Inpage Script (`content/inpage.js`)**:
   - Injected into the **MAIN** world of user webpages (like the Explorer).
   - Exposes the global `window.frostbit` provider API.
   - Communicates exclusively via `window.postMessage` to bypass browser isolation policies.
3. **Content Script Bridge (`content/content_script.js`)**:
   - Runs in the isolated extension context of webpages.
   - Dynamically injects `inpage.js` as a `<script>` tag.
   - Listens to `postMessage` from `inpage.js` and securely forwards messages using `chrome.runtime.sendMessage` to the background service worker.
4. **Vite Frontend (`src/App.vue` & `popup/`)**:
   - The user-facing UI for Wallet unlocking, mnemonic generation, balance viewing, sending, and importantly, **approving/rejecting DApp signature requests**.

## Secure Transaction Approval Flow

**Crucial Security Design:** DApps are NEVER allowed to sign transactions "blindly" or "silently", even if the wallet is currently unlocked. 
1. The DApp calls `window.frostbit.sendTransaction(txDesc)`.
2. The Background script intercepts the `SEND_TX` / `SIGN_TX` messages via `sender.tab`.
3. It pauses the promise and stores the intent in a memory queue (`_session.pendingRequests`).
4. It triggers `chrome.windows.create` to pop open the Wallet UI.
5. `App.vue` detects the pending requests and switches to the **Approval Page** (`page = 'approve'`).
6. The user manually clicks "Approve", the final message is sent to the background to sign, and the original suspended Promise resolves back to the DApp.

## Protobuf Compilation Rule

Manifest V3 strictly enforces CSP `unsafe-eval`, permanently blocking runtime code generation libraries.
Because of this, **`protobufjs` cannot be used with JSON reflection dynamically.**

Instead, we use **Static Code Generation**:
1. Run `npx pbjs -t static-module -w es6 -o lib/data_proto.js pb/data.proto` when `pb/data.proto` changes.
2. `lib/proto.js` directly imports the generated classes from `data_proto.js`, avoiding dynamic `eval()` or `new Function()` evaluations entirely during runtime sequence/deserialization.

## Build and Developer Workflow

The wallet consists of two separate build systems:
1. **Service Worker and Scripts**: Uses `esbuild` for rapid, single-file bundling.
   ```bash
   node build.js
   ```
2. **Frontend UI (Popup)**: Uses `Vite` + `Vue3`.
   ```bash
   npm run build:popup
   ```

To install the extension, load the unpacked `wallet/` directory inside `chrome://extensions`. Whenever `background` or `content/` scripts are updated, you must click the "Reload" icon on the extension page and refresh the DApp webpage for changes to take effect.
