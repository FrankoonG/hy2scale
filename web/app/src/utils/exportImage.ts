/**
 * Save part of the page as a PNG: the element is cloned with every computed
 * style written inline (so classes, the theme and Dark Reader's colours all
 * come along), drawn through an SVG image onto a canvas and downloaded.
 * HTML is wrapped in a foreignObject; an <svg> is serialised as is. Only
 * inline content (data: images, system fonts) survives, which is all the
 * topology views use.
 */

const XHTML = 'http://www.w3.org/1999/xhtml';
const SVGNS = 'http://www.w3.org/2000/svg';
// Motion is meaningless in a still image and transitions would be sampled mid-way.
const SKIP = /^(animation|transition|cursor|pointer-events|user-select|-webkit-user-select|touch-action|will-change)/;

function inline(src: Element, dst: Element) {
  const cs = getComputedStyle(src);
  let s = '';
  for (let i = 0; i < cs.length; i++) {
    const k = cs[i];
    if (SKIP.test(k)) continue;
    s += `${k}:${cs.getPropertyValue(k)};`;
  }
  dst.setAttribute('style', s);
  const a = src.children;
  const b = dst.children;
  for (let i = 0; i < a.length && i < b.length; i++) inline(a[i], b[i]);
}

export interface ExportOptions {
  /** Output size in CSS px (the image is drawn at `scale`×). */
  width: number;
  height: number;
  background: string;
  fileName: string;
  scale?: number;
  /** Drawn first, stretched to the image (e.g. a map under the graph). */
  underlay?: CanvasImageSource;
  /** Adjusts the styled clone before it is serialised (root first). */
  adjust?: (clone: Element) => void;
}

export async function exportPng(el: Element, o: ExportOptions) {
  const clone = el.cloneNode(true) as Element;
  inline(el, clone);
  o.adjust?.(clone);
  const w = Math.ceil(o.width);
  const h = Math.ceil(o.height);
  let svg: string;
  if (clone instanceof SVGSVGElement) {
    clone.setAttribute('xmlns', SVGNS);
    clone.setAttribute('width', String(w));
    clone.setAttribute('height', String(h));
    svg = new XMLSerializer().serializeToString(clone);
  } else {
    clone.setAttribute('xmlns', XHTML);
    svg = `<svg xmlns="${SVGNS}" width="${w}" height="${h}"><foreignObject x="0" y="0" width="${w}" height="${h}">`
      + `${new XMLSerializer().serializeToString(clone)}</foreignObject></svg>`;
  }
  const img = new Image();
  img.src = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg);
  await img.decode();
  const k = o.scale ?? 2;
  const canvas = document.createElement('canvas');
  canvas.width = w * k;
  canvas.height = h * k;
  const ctx = canvas.getContext('2d');
  if (!ctx) throw new Error('canvas unavailable');
  ctx.fillStyle = o.background;
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  ctx.scale(k, k);
  if (o.underlay) ctx.drawImage(o.underlay, 0, 0, w, h);
  ctx.drawImage(img, 0, 0, w, h);
  const blob = await new Promise<Blob | null>((res) => canvas.toBlob(res, 'image/png'));
  if (!blob) throw new Error('image encoding failed');
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = o.fileName;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}

/** "<base>-YYYYMMDD-HHMM.png", with characters unsafe in file names replaced. */
export function imageFileName(base: string) {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, '0');
  const stamp = `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`;
  return `${base.replace(/[\\/:*?"<>|\s]+/g, '_')}-${stamp}.png`;
}
