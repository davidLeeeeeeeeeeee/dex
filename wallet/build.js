/**
 * build.js — esbuild 打包 background + content script
 * Popup 由 Vite 单独构建（npm run build:popup）
 */

import * as esbuild from 'esbuild';
import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const isWatch = process.argv.includes('--watch');
const OUT = 'dist';

function copyFile(src, dest) {
    fs.mkdirSync(path.dirname(dest), { recursive: true });
    fs.copyFileSync(src, dest);
}

function copyStatic() {
    copyFile('manifest.json', `${OUT}/manifest.json`);
    for (const f of fs.readdirSync('icons')) {
        copyFile(`icons/${f}`, `${OUT}/icons/${f}`);
    }
    console.log('📁 Static assets copied');
}

const buildOptions = [
    {
        entryPoints: ['background/service_worker.js'],
        bundle: true,
        outdir: `${OUT}/background`,
        format: 'esm',
        platform: 'browser',
        target: 'chrome120',
        sourcemap: isWatch ? 'inline' : false,
        loader: { '.json': 'json' },
    },
    {
        entryPoints: ['content/content_script.js'],
        bundle: true,
        outdir: `${OUT}/content`,
        format: 'iife',
        platform: 'browser',
        target: 'chrome120',
        sourcemap: isWatch ? 'inline' : false,
    },
];

async function main() {
    copyStatic();
    const ctxs = await Promise.all(buildOptions.map(o => esbuild.context(o)));
    await Promise.all(ctxs.map(c => c.rebuild()));
    console.log(`✅ Build → ${OUT}/`);

    if (isWatch) {
        await Promise.all(ctxs.map(c => c.watch()));
        console.log('👀 Watching background/content...');
        fs.watchFile('manifest.json', { interval: 500 }, () => { copyStatic(); });
    } else {
        ctxs.forEach(c => c.dispose());
    }
}

main().catch(e => { console.error(e); process.exit(1); });
