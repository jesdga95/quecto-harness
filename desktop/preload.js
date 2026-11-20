// Marks the page as running in the macOS window, whose title bar is drawn by
// the page itself, and tells it when the window enters and leaves fullscreen.
const { ipcRenderer } = require('electron');

if (process.platform === 'darwin') {
  window.addEventListener('DOMContentLoaded', () => {
    document.documentElement.classList.add('desktop-mac');
  });
  // Fullscreen hides the traffic lights, so the room kept for them goes too.
  ipcRenderer.on('fullscreen', (_event, on) => {
    document.documentElement.classList.toggle('is-fullscreen', on);
  });
}
