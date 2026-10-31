// World map of the node topology (geo mode): projections, the bundled
// Natural Earth data (public domain; web/app/public/geo, loaded on demand)
// and the basemap drawn on a canvas under the graph. Positions are "base"
// coordinates — the projection at a fixed scale around (0, 0) — so the
// canvas view (screen = base × k + offset) pans and zooms them like any
// other topology layout.
import { geoArea, geoEqualEarth, geoMercator, geoPath, type GeoProjection } from 'd3-geo';
import { feature, mesh } from 'topojson-client';
import type { Feature, FeatureCollection, Geometry, MultiLineString } from 'geojson';
import type { Topology } from 'topojson-specification';

export type MapProj = 'mercator' | 'equalearth';
export const MAP_PROJS: MapProj[] = ['mercator', 'equalearth'];

/** Width of the whole world in base units. */
export const BASE = 1000;

type Lang = 'en' | 'zh' | 'ko';
interface Named { en: string; zh?: string; ko?: string }
interface Props extends Named { iso: string; lx: number; ly: number; ml: number }

export interface Country { iso: string; names: Named; label: [number, number]; ml: number; f: Feature<Geometry, Props> }
interface Region { names: Named; label: [number, number]; ml: number; f: Feature<Geometry, Props> }
/** [lon, lat, scalerank, capital, en, zh, ko] */
type City = [number, number, number, number, string, string, string];

export interface GeoData {
  countries: Country[];
  byIso: Map<string, Country>;
  borders: MultiLineString;
  cities: City[];
}
export interface Admin1 { regions: Region[]; lines: MultiLineString }

const name = (n: Named, lang: Lang) => (lang !== 'en' && n[lang]) || n.en;

async function getJSON<T>(url: string): Promise<T> {
  const r = await fetch(url, { credentials: 'same-origin' });
  if (!r.ok) throw new Error(`${url}: ${r.status}`);
  return r.json() as Promise<T>;
}

function layer(topo: Topology): FeatureCollection<Geometry, Props> {
  const obj = topo.objects[Object.keys(topo.objects)[0]];
  return feature(topo, obj) as unknown as FeatureCollection<Geometry, Props>;
}

/** One country per ISO code: several features can share one (Australia and
 * Ashmore and Cartier Islands are both AU); the largest is the country. */
function largestByIso(countries: Country[]): Map<string, Country> {
  const m = new Map<string, { c: Country; a: number }>();
  for (const c of countries) {
    if (!/^[A-Z]{2}$/.test(c.iso)) continue;
    const a = geoArea(c.f);
    const cur = m.get(c.iso);
    if (!cur || a > cur.a) m.set(c.iso, { c, a });
  }
  return new Map([...m].map(([k, v]) => [k, v.c]));
}

export async function loadGeo(base: string): Promise<GeoData> {
  const [ct, cities] = await Promise.all([
    getJSON<Topology>(`${base}/geo/countries.json`),
    getJSON<City[]>(`${base}/geo/cities.json`),
  ]);
  const fc = layer(ct);
  const countries: Country[] = fc.features.map((f) => ({
    iso: String(f.properties.iso || ''), names: f.properties, label: [f.properties.lx, f.properties.ly], ml: f.properties.ml ?? 5, f,
  }));
  const obj = ct.objects[Object.keys(ct.objects)[0]];
  return {
    countries,
    byIso: largestByIso(countries),
    borders: mesh(ct, obj as any, (a, b) => a !== b) as MultiLineString,
    cities,
  };
}

export async function loadAdmin1(base: string): Promise<Admin1> {
  const t = await getJSON<Topology>(`${base}/geo/admin1.json`);
  const fc = layer(t);
  const obj = t.objects[Object.keys(t.objects)[0]];
  return {
    regions: fc.features.map((f) => ({ names: f.properties, label: [f.properties.lx, f.properties.ly], ml: f.properties.ml ?? 8, f })),
    lines: mesh(t, obj as any, (a, b) => a !== b) as MultiLineString,
  };
}

// ---------------------------------------------------------------- projections

const projCache = new Map<MapProj, { p: GeoProjection; s: number }>();

/** The base projection: the world BASE units wide, centred on (0, 0). */
function baseOf(kind: MapProj): { p: GeoProjection; s: number } {
  let b = projCache.get(kind);
  if (!b) {
    const p = (kind === 'mercator' ? geoMercator() : geoEqualEarth()).translate([0, 0]).scale(1);
    const [[x0], [x1]] = geoPath(p).bounds({ type: 'Sphere' });
    const s = BASE / (x1 - x0);
    p.scale(s);
    b = { p, s };
    projCache.set(kind, b);
  }
  return b;
}

/** Base coordinates of [lon, lat]; null when it does not project. */
export function project(kind: MapProj, lonlat: [number, number]): { x: number; y: number } | null {
  const r = baseOf(kind).p(lonlat);
  return r && Number.isFinite(r[0]) && Number.isFinite(r[1]) ? { x: r[0], y: r[1] } : null;
}

/** [lon, lat] of base coordinates. */
export function invert(kind: MapProj, x: number, y: number): [number, number] | null {
  const r = baseOf(kind).p.invert?.([x, y]);
  return r ? [r[0], r[1]] : null;
}

/** The projection drawing at view (x, y, k): screen = base × k + (x, y). */
function screenProjection(kind: MapProj, v: { x: number; y: number; k: number }): GeoProjection {
  const { s } = baseOf(kind);
  const p = kind === 'mercator' ? geoMercator() : geoEqualEarth();
  return p.scale(s * v.k).translate([v.x, v.y]);
}

type Box = [[number, number], [number, number]];
const boundsCache = new WeakMap<object, Map<MapProj, Box>>();
/** Base-coordinate bounds of a feature. */
export function boundsOf(kind: MapProj, f: object): Box {
  let m = boundsCache.get(f);
  if (!m) boundsCache.set(f, (m = new Map()));
  let b = m.get(kind);
  if (!b) {
    b = geoPath(baseOf(kind).p).bounds(f as any) as Box;
    m.set(kind, b);
  }
  return b;
}

/**
 * Base bounds of a country's main landmass (its largest polygon): what a
 * view fits, so overseas territories or islands across the antimeridian do
 * not shrink the country to a dot.
 */
export function mainBounds(kind: MapProj, f: Feature<Geometry, unknown>): Box {
  if (f.geometry.type !== 'MultiPolygon') return boundsOf(kind, f);
  let best: Feature | null = null;
  let area = -1;
  for (const coords of f.geometry.coordinates) {
    const part: Feature = { type: 'Feature', properties: {}, geometry: { type: 'Polygon', coordinates: coords } };
    const a = geoArea(part);
    if (a > area) { area = a; best = part; }
  }
  return best ? (geoPath(baseOf(kind).p).bounds(best) as Box) : boundsOf(kind, f);
}

/** Bounds of the whole world in base coordinates. */
export function worldBounds(kind: MapProj): Box {
  return geoPath(baseOf(kind).p).bounds({ type: 'Sphere' }) as Box;
}

// ---------------------------------------------------------------- basemap

export interface Palette { ocean: string; land: string; border: string; region: string; text: string; halo: string; city: string; outside: string }

/** Web-map zoom level of the view (world = 256 × 2^z px). */
export const zoomLevel = (k: number) => Math.log2((BASE * k) / 256);

/**
 * Draws the basemap of view `v` into ctx (CSS px; the caller scales for
 * the device pixel ratio). Countries always; states and cities appear as
 * the zoom grows, labels are thinned so they never overlap.
 */
export function drawBasemap(
  ctx: CanvasRenderingContext2D, w: number, h: number, kind: MapProj, v: { x: number; y: number; k: number },
  data: GeoData, admin1: Admin1 | null, pal: Palette, lang: Lang,
) {
  const proj = screenProjection(kind, v);
  const path = geoPath(proj, ctx);
  const z = zoomLevel(v.k);
  const visible = (f: object) => {
    const [[x0, y0], [x1, y1]] = boundsOf(kind, f);
    return x1 * v.k + v.x >= 0 && x0 * v.k + v.x <= w && y1 * v.k + v.y >= 0 && y0 * v.k + v.y <= h;
  };

  ctx.clearRect(0, 0, w, h);
  ctx.fillStyle = pal.outside;
  ctx.fillRect(0, 0, w, h);
  ctx.beginPath();
  path({ type: 'Sphere' });
  ctx.fillStyle = pal.ocean;
  ctx.fill();

  ctx.beginPath();
  for (const c of data.countries) if (visible(c.f)) path(c.f);
  ctx.fillStyle = pal.land;
  ctx.fill();

  if (admin1 && z >= 3.2) {
    ctx.beginPath();
    path(admin1.lines);
    ctx.strokeStyle = pal.region;
    ctx.lineWidth = 0.6;
    ctx.setLineDash([3, 2]);
    ctx.stroke();
    ctx.setLineDash([]);
  }

  ctx.beginPath();
  path(data.borders);
  ctx.strokeStyle = pal.border;
  ctx.lineWidth = 0.8;
  ctx.stroke();

  // ---- labels (larger first; a placed label blocks its box)
  const placed: Array<[number, number, number, number]> = [];
  const free = (x0: number, y0: number, x1: number, y1: number) => {
    if (x1 < 0 || x0 > w || y1 < 0 || y0 > h) return false;
    for (const b of placed) if (x0 < b[2] && x1 > b[0] && y0 < b[3] && y1 > b[1]) return false;
    return true;
  };
  const text = (s: string, x: number, y: number, font: string, color: string, align: CanvasTextAlign = 'center') => {
    ctx.font = font;
    const m = ctx.measureText(s);
    const tw = m.width, th = 12;
    const x0 = align === 'center' ? x - tw / 2 : align === 'left' ? x : x - tw;
    if (!free(x0 - 2, y - th, x0 + tw + 2, y + 3)) return false;
    placed.push([x0 - 2, y - th, x0 + tw + 2, y + 3]);
    ctx.textAlign = align;
    ctx.lineWidth = 3;
    ctx.strokeStyle = pal.halo;
    ctx.strokeText(s, x, y);
    ctx.fillStyle = color;
    ctx.fillText(s, x, y);
    return true;
  };
  ctx.lineJoin = 'round';
  ctx.textBaseline = 'alphabetic';

  // cities: capitals and big cities first (data is sorted by rank)
  const maxRank = z < 2 ? -1 : z < 3 ? 1 : z < 4 ? 3 : z < 5 ? 5 : z < 6 ? 7 : z < 7 ? 9 : 10;
  if (maxRank >= 0) {
    for (const c of data.cities) {
      if (c[2] > maxRank) break;
      const p = proj([c[0], c[1]]);
      if (!p || p[0] < -20 || p[0] > w + 20 || p[1] < -20 || p[1] > h + 20) continue;
      const label = (lang === 'zh' && c[5]) || (lang === 'ko' && c[6]) || c[4];
      if (!text(label, p[0] + 5, p[1] + 4, `${c[3] ? 600 : 400} 11px system-ui, sans-serif`, pal.city, 'left')) continue;
      ctx.beginPath();
      ctx.arc(p[0], p[1], c[3] ? 2.6 : 2, 0, Math.PI * 2);
      ctx.fillStyle = pal.city;
      ctx.fill();
    }
  }
  // countries
  for (const c of data.countries) {
    if (z + 1 < c.ml || !visible(c.f)) continue;
    // Specks (small islands, territories) get no name.
    const [[bx0], [bx1]] = boundsOf(kind, c.f);
    if ((bx1 - bx0) * v.k < 30) continue;
    const p = proj(c.label);
    if (p) text(name(c.names, lang), p[0], p[1], '600 12px system-ui, sans-serif', pal.text);
  }
  // states
  if (admin1 && z >= 4.5) {
    for (const r of admin1.regions) {
      if (z + 1.5 < r.ml || !visible(r.f)) continue;
      const p = proj(r.label);
      if (p) text(name(r.names, lang), p[0], p[1], 'italic 11px system-ui, sans-serif', pal.region);
    }
  }
}
