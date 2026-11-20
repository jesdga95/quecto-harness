// Quecto Harness in a desktop window. The bundled server runs as a child
// process and the window loads its URL. A server already running from the
// terminal or the service is reused and left running on quit.
const { app, BrowserWindow, Menu, dialog, shell } = require('electron');
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const home = process.env.QUECTO_HARNESS_HOME || path.join(os.homedir(), '.quecto-harness');

let server = null;
let serverURL = null;
let win = null;
let quitting = false;

function binaryPath() {
  if (app.isPackaged) return path.join(process.resourcesPath, 'quecto-harness');
  return path.join(__dirname, '..', 'quecto-harness');
}

// Finder starts apps with a bare PATH, and the agent's shell tools need the
// one the user's login shell sets up.
function loginEnv() {
  const shellPath = process.env.SHELL || '/bin/zsh';
  const mark = '__QUECTO_ENV__';
  try {
    const out = execFileSync(shellPath, ['-ilc', `printf ${mark}; env -0; printf ${mark}`], {
      encoding: 'utf8',
      timeout: 5000,
      stdio: ['ignore', 'pipe', 'ignore'],
    });
    const body = out.split(mark)[1] || '';
    const env = {};
    for (const pair of body.split('\0')) {
      const i = pair.indexOf('=');
      if (i > 0) env[pair.slice(0, i)] = pair.slice(i + 1);
    }
    if (env.PATH) return { ...process.env, ...env };
  } catch {}
  return process.env;
}

async function reachable(url) {
  try {
    const res = await fetch(url, { signal: AbortSignal.timeout(1000) });
    return res.ok;
  } catch {
    return false;
  }
}

async function runningServer() {
  try {
    const url = fs.readFileSync(path.join(home, 'url'), 'utf8').trim();
    if (url && (await reachable(url))) return url;
  } catch {}
  return null;
}

function startServer() {
  return new Promise((resolve, reject) => {
    const child = spawn(binaryPath(), ['--no-open'], { env: loginEnv(), stdio: ['ignore', 'pipe', 'pipe'] });
    let out = '';
    let err = '';
    child.stdout.on('data', (chunk) => {
      out += chunk;
      const line = out.split('\n').find((l) => l.startsWith('http'));
      if (line && !serverURL) resolve(line.trim());
    });
    child.stderr.on('data', (chunk) => {
      err += chunk;
    });
    child.on('error', (e) => reject(new Error(`Could not start ${binaryPath()}: ${e.message}`)));
    child.on('exit', (code) => {
      server = null;
      if (!serverURL) reject(new Error(err.trim() || `The server exited with code ${code}.`));
      else if (!quitting) {
        dialog.showErrorBox('The server stopped', err.trim() || `It exited with code ${code}.`);
        app.quit();
      }
    });
    server = child;
  });
}

function sameOrigin(url) {
  try {
    return new URL(url).origin === new URL(serverURL).origin;
  } catch {
    return false;
  }
}

function createWindow() {
  win = new BrowserWindow({
    width: 1280,
    height: 860,
    minWidth: 520,
    minHeight: 480,
    title: 'Quecto Harness',
    show: false,
    // The page draws the title bar, with room left for the traffic lights.
    ...(process.platform === 'darwin' && {
      titleBarStyle: 'hidden',
      // Centred on the page's 56px title row.
      trafficLightPosition: { x: 16, y: 21 },
    }),
    webPreferences: {
      contextIsolation: true,
      sandbox: true,
      preload: path.join(__dirname, 'preload.js'),
    },
  });
  win.once('ready-to-show', () => win.show());
  win.on('closed', () => {
    win = null;
  });

  // Files served by the harness open in a child window, which shares the
  // session cookie. Anything else goes to the default browser.
  win.webContents.setWindowOpenHandler(({ url }) => {
    if (sameOrigin(url)) return { action: 'allow' };
    if (/^https?:|^mailto:/.test(url)) shell.openExternal(url);
    return { action: 'deny' };
  });
  win.webContents.on('will-navigate', (event, url) => {
    if (sameOrigin(url)) return;
    event.preventDefault();
    if (/^https?:|^mailto:/.test(url)) shell.openExternal(url);
  });

  const sendFullscreen = () => win.webContents.send('fullscreen', win.isFullScreen());
  win.on('enter-full-screen', sendFullscreen);
  win.on('leave-full-screen', sendFullscreen);
  win.webContents.on('did-finish-load', sendFullscreen);

  win.loadURL(serverURL);
}

// The standard menus, without reload or developer tools outside development.
// Edit stays, since copy and paste go through its roles.
function buildMenu() {
  const view = app.isPackaged
    ? [{ role: 'resetZoom' }, { role: 'zoomIn' }, { role: 'zoomOut' }, { type: 'separator' }, { role: 'togglefullscreen' }]
    : [{ role: 'reload' }, { role: 'toggleDevTools' }, { type: 'separator' }, { role: 'resetZoom' }, { role: 'zoomIn' }, { role: 'zoomOut' }, { type: 'separator' }, { role: 'togglefullscreen' }];
  Menu.setApplicationMenu(
    Menu.buildFromTemplate([
      ...(process.platform === 'darwin' ? [{ role: 'appMenu' }] : []),
      { role: 'editMenu' },
      { label: 'View', submenu: view },
      { role: 'windowMenu' },
    ]),
  );
}

if (!app.requestSingleInstanceLock()) {
  app.quit();
} else {
  app.on('second-instance', () => {
    if (!win) return createWindow();
    if (win.isMinimized()) win.restore();
    win.focus();
  });

  app.whenReady().then(async () => {
    buildMenu();
    try {
      serverURL = (await runningServer()) || (await startServer());
    } catch (e) {
      dialog.showErrorBox('Quecto Harness could not start', e.message);
      app.exit(1);
      return;
    }
    createWindow();
  });

  app.on('activate', () => {
    if (!win && serverURL) createWindow();
  });

  app.on('window-all-closed', () => {
    if (process.platform !== 'darwin') app.quit();
  });

  // The server gets SIGTERM and time to finish running turns before the app
  // exits. It is killed if it is still up after ten seconds.
  app.on('before-quit', (event) => {
    quitting = true;
    if (!server) return;
    event.preventDefault();
    const child = server;
    const force = setTimeout(() => child.kill('SIGKILL'), 10000);
    child.once('exit', () => {
      clearTimeout(force);
      app.exit(0);
    });
    child.kill('SIGTERM');
  });
}
