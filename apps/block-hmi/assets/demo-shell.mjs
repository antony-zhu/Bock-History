export const demoViewport = Object.freeze({ width: 1920, height: 1080 });
export const demoFrameParameter = "__demoFrame";

export function demoFrameURL(locationHref) {
  const sourceURL = new URL(locationHref);
  const frameURL = new URL("index.html", sourceURL);
  frameURL.search = sourceURL.search;
  frameURL.searchParams.set(demoFrameParameter, "1");
  frameURL.hash = sourceURL.hash;
  return frameURL;
}

export function demoVisibleURL(shellHref) {
  const sourceURL = new URL(shellHref);
  const visibleURL = new URL(".", sourceURL);
  visibleURL.search = sourceURL.search;
  visibleURL.hash = sourceURL.hash;
  return visibleURL;
}

export function demoDisplayScale(width, height) {
  return Math.min(1, width / demoViewport.width, height / demoViewport.height);
}
