import { build } from 'esbuild';
import { copyFile } from 'node:fs/promises';
await build({entryPoints: ['app.js'], bundle: true, minify: true, outfile: 'public/app.js', legalComments: 'linked', target: ['es2022']});
await copyFile('node_modules/@xterm/xterm/css/xterm.css', 'public/xterm.css');
await copyFile('node_modules/@xterm/xterm/LICENSE', 'public/xterm.LICENSE');
await copyFile('node_modules/@xterm/addon-fit/LICENSE', 'public/addon-fit.LICENSE');
