/** Shows a frame a tenth of the way into a video, at most three seconds in,
    once its length is known. A video that reports no length shows its
    opening frame. */
export function thumbFrame(node: HTMLVideoElement) {
  const seek = () => {
    const d = node.duration;
    node.currentTime = Number.isFinite(d) && d > 0 ? Math.min(d * 0.1, 3) : 0.1;
  };
  node.addEventListener("loadedmetadata", seek, { once: true });
  return { destroy: () => node.removeEventListener("loadedmetadata", seek) };
}
