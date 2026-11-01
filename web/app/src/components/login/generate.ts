import { SIGNAL_SPEED, VBH, validateSpec, type DeviceIcon, type Flow, type Note, type Signal, type SpecEvent, type SpecLink, type SpecNode, type TopoSpec, type Zone } from './spec';

/*
 * generate(seed) → TopoSpec
 *
 * A topology is composed from feature modules around the local node:
 *   - one inbound module on the left (single device / multi-protocol / admin),
 *   - 1-3 outbound modules, each in its own horizontal band on the right
 *     (exit chain, gated branch, punch, bond, failover, native, offline, remote),
 *   - one routing policy over the exit-capable modules
 *     (plain, per-user exits, domain / IP rules with optional local breakout).
 * Every module and policy is picked by weight; the tags shown are exactly the
 * tags of what was picked, so the same shape recurs with different feature
 * mixes. All randomness comes from the seed, so a seed reproduces its topology.
 */

/** Relative weight per feature (0 disables it). */
export const DEFAULT_WEIGHTS = {
  multihop: 1.2,      // exit chain with 2-3 hops (else a single hop)
  hopAuth: 0.6,       // a relay with nested off, its children un-dialable
  punch: 0.8,         // NAT hole punching via a public helper
  multiAddr: 0.9,     // two addresses / sub-interfaces to one peer
  failover: 0.8,      // two relay paths, primary fails
  native: 0.6,        // native Hysteria 2 server as an exit
  offline: 0.35,      // an offline peer (decoration)
  multiProto: 0.9,    // several devices over different protocols
  remoteAdmin: 0.35,  // admin browser to a remote panel through the relay
  perUserExit: 0.9,   // users leave through different exits
  rules: 0.9,         // domain / IP rules pick the exit
  quota: 0.5,         // a user near its traffic quota
};
export type Weights = typeof DEFAULT_WEIGHTS;

const MAX_TAGS = 6;

// ------------------------------------------------------------------ rng
function hashSeed(s: string): number {
  let h = 1779033703 ^ s.length;
  for (let i = 0; i < s.length; i++) { h = Math.imul(h ^ s.charCodeAt(i), 3432918353); h = (h << 13) | (h >>> 19); }
  h = Math.imul(h ^ (h >>> 16), 2246822507); h = Math.imul(h ^ (h >>> 13), 3266489909);
  return (h ^= h >>> 16) >>> 0;
}

function mulberry32(a: number) {
  return () => {
    a |= 0; a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

type Rng = ReturnType<typeof makeRng>;
function makeRng(seed: string) {
  const r = mulberry32(hashSeed(seed));
  return {
    f: r,
    range: (a: number, b: number) => a + (b - a) * r(),
    int: (a: number, b: number) => a + Math.floor(r() * (b - a + 1)),
    chance: (p: number) => r() < p,
    pick: <T>(arr: T[]): T => arr[Math.floor(r() * arr.length)],
    wpick<T extends { w: number }>(items: T[]): T | null {
      const tot = items.reduce((s, x) => s + Math.max(0, x.w), 0);
      if (tot <= 0) return null;
      let t = r() * tot;
      for (const x of items) { t -= Math.max(0, x.w); if (t <= 0) return x; }
      return items[items.length - 1];
    },
  };
}

/** A fresh browser-generated seed. */
export function newSeed(): string {
  const a = new Uint32Array(1);
  crypto.getRandomValues(a);
  return a[0].toString(36).slice(0, 6);
}

// ------------------------------------------------------------------ modules
const TOP = 46, BOTTOM = 392, RIGHT = 596;
const r1 = Math.round;

interface Band { cy: number; h: number }
interface Self { x: number; y: number }
interface Opts { punchN: number }

/** Paths from after self to an exit; `first` = the self-adjacent link (rule chips). */
interface ExitRoute {
  path: string[];
  first: string;
  local?: boolean;
  // quality bond: one link, then the other
  only?: string[]; then?: string[];
  // failover: primary path until, alternate from
  until?: number; alt?: string[]; from?: number;
}

interface Module {
  signals?: Signal[];
  hold?: number;
  nodes: SpecNode[];
  links: SpecLink[];
  zones?: Zone[];
  notes?: Note[];
  events?: SpecEvent[];
  exits: ExitRoute[];
  tags: string[];
  /** Punch: its own site-to-site flows from the first device. */
  ownFlows?: (src: string) => Flow[];
  selfZone?: boolean;
  /** Remote management target path after self. */
  admin?: string[];
}

type Kind = 'chain' | 'gate' | 'punch' | 'bond' | 'failover' | 'native' | 'offline' | 'local' | 'remote';
type ModFn = (R: Rng, W: Weights, b: Band, i: number, S: Self, o: Opts) => Module;

const MODS: Record<Kind, ModFn> = {
  chain(R, W, b, i, S) {
    const p = 'c' + i;
    const deep = R.chance(W.multihop / (W.multihop + 1));
    const hops = deep ? R.int(2, 3) : 1;          // hops after self, the last is the exit
    const withNet = hops < 3 || R.chance(0.5);
    const xEnd = withNet ? 470 + R.range(-20, 20) : 560;
    const nodes: SpecNode[] = [], links: SpecLink[] = [], path: string[] = [];
    let prev = 'self';
    for (let j = 1; j <= hops; j++) {
      const id = p + 'h' + j, last = j === hops;
      const x = S.x + (xEnd - S.x) * (j / hops);
      const y = b.cy + (hops > 1
        ? (j % 2 ? -1 : 1) * Math.min(b.h * 0.2, 34) * R.range(0.5, 1)
        : R.range(-1, 1) * Math.min(b.h * 0.15, 20));
      nodes.push({ id, role: last ? 'exit' : 'relay', x: r1(x), y: r1(y), tag: hops > 1 ? 'abc'[j - 1] : undefined });
      links.push({ id: p + 'l' + j, a: prev, b: id, kind: 'peer', curve: j === 1 ? R.range(-0.08, 0.08) : 0 });
      path.push(id); prev = id;
    }
    if (withNet) {
      nodes.push({ id: p + 'net', role: 'internet', x: RIGHT - 4, y: r1(nodes[nodes.length - 1].y) });
      links.push({ id: p + 'eg', a: prev, b: p + 'net', kind: 'egress' });
      path.push(p + 'net');
    }
    const notes = hops > 1 && b.h >= 150
      ? [{ x: r1((S.x + xEnd) / 2 + 40), y: r1(b.cy - b.h / 2 + 18), text: 'exit_via a/b' + (hops > 2 ? '/c' : '') }]
      : [];
    return { nodes, links, notes, exits: [{ path, first: p + 'l1' }], tags: hops > 1 ? ['multihop', 'nested'] : ['exitNode'] };
  },

  gate(R, W, b, i, S) {
    const p = 'g' + i, rx = S.x + 130 + R.range(-15, 15);
    const dy = Math.min(b.h * 0.28, 42);
    return {
      nodes: [
        { id: p + 'r', role: 'relay', x: r1(rx), y: r1(b.cy), badge: 'lock' },
        { id: p + 'c1', role: 'peer', x: r1(rx + 130), y: r1(b.cy - dy), state: 'dim' },
        { id: p + 'c2', role: 'peer', x: r1(rx + 115), y: r1(b.cy + dy), state: 'dim' },
      ],
      links: [
        { id: p + 'l', a: 'self', b: p + 'r', kind: 'peer' },
        { id: p + 'd1', a: p + 'r', b: p + 'c1', kind: 'peer', state: 'dim' },
        { id: p + 'd2', a: p + 'r', b: p + 'c2', kind: 'peer', state: 'dim' },
      ],
      exits: [], tags: ['nested', 'hopAuth'],
    };
  },

  // NAT hole punching. 1-3 peers, each behind its own NAT; one public helper
  // (x) that every NAT'd node reaches signals the punches. The chain
  // self → p1 → … → pn is punched segment by segment; until a segment is up,
  // traffic beyond the punched prefix is relayed by x.
  punch(R, W, b, i, S, o) {
    const p = 'p' + i, n = o.punchN;
    const side = b.cy < VBH / 2 ? 1 : -1;            // peers towards the panel centre, x away from it
    const rowY = b.cy + side * Math.min(b.h * 0.18, 34);
    const xEnd = n === 1 ? 470 : 560;
    const peers: SpecNode[] = [];
    for (let k = 1; k <= n; k++) {
      const x = n === 1 ? xEnd : S.x + 125 + (xEnd - S.x - 125) * ((k - 1) / (n - 1));
      peers.push({ id: p + 'n' + k, role: 'peer', x: r1(x + R.range(-8, 8)), y: r1(rowY + (n > 1 ? R.range(-10, 10) : 0)) });
    }
    const midX = n === 1 ? (S.x + xEnd) / 2 : (peers[0].x + peers[n - 1].x) / 2;
    const helper: SpecNode = { id: p + 'x', role: 'relay', x: r1(midX + R.range(-20, 20)), y: r1(b.cy - side * Math.min(b.h * 0.3, 62)), tag: 'x' };
    const nodes: SpecNode[] = [helper, ...peers];
    const links: SpecLink[] = [{ id: p + 's0', a: 'self', b: helper.id, kind: 'peer' }];
    peers.forEach((q, k) => links.push({ id: p + 's' + (k + 1), a: q.id, b: helper.id, kind: 'peer' }));
    const zones: Zone[] = peers.map((q) => ({ x: q.x - 32, y: q.y - 30, w: 64, h: 74, label: 'NAT' }));
    let dev: SpecNode | null = null;
    if (n === 1) {   // a single site gets its LAN device
      dev = { id: p + 'd', role: 'device', x: RIGHT - 16, y: peers[0].y, icon: R.pick<DeviceIcon>(['phone', 'monitor', 'laptop']), label: false };
      nodes.push(dev);
      links.push({ id: p + 'a', a: peers[0].id, b: dev.id, kind: 'access' });
      zones[0] = { x: peers[0].x - 40, y: peers[0].y - 38, w: RIGHT + 24 - peers[0].x + 40, h: 92, label: 'NAT' };
    }
    // punches, one after another: a signal goes from one end through x to
    // the other and back (each learns the other's address), then the direct
    // link appears pending while both ends probe, then it comes up
    const events: SpecEvent[] = [], ups: number[] = [], signals: Signal[] = [];
    let t = R.range(1.5, 2.0);
    const chain = ['self', ...peers.map((q) => q.id)];
    const at = (id: string) => (id === 'self' ? S : peers.find((q) => q.id === id)!);
    const dist = (a: { x: number; y: number }, b: { x: number; y: number }) => Math.hypot(a.x - b.x, a.y - b.y);
    for (let k = 1; k <= n; k++) {
      const a = chain[k - 1], b = chain[k];
      const trip = (2 * (dist(at(a), helper) + dist(helper, at(b)))) / SIGNAL_SPEED;
      signals.push({ at: +t.toFixed(2), path: [a, helper.id, b, helper.id, a] });
      const appear = t + trip, up = appear + R.range(1.0, 1.3);
      const id = p + 'pp' + k;
      links.push({ id, a, b, kind: 'punch', state: 'pending', appear: +appear.toFixed(2), curve: R.range(-0.12, 0.12) });
      events.push({ at: +up.toFixed(2), link: id, state: 'up' });
      ups.push(+up.toFixed(2));
      t = up + R.range(0.3, 0.6);
    }
    const tail = dev ? [dev.id] : [];
    const last = peers[n - 1].id;
    return {
      nodes, links, zones, events, signals, hold: ups[n - 1] + 2.5,
      // phase k: the first k segments are punched, x relays the rest
      ownFlows: (src) => {
        const out: Flow[] = [];
        for (let k = 0; k <= n; k++) {
          const pre = chain.slice(1, k + 1);
          const path = k === n ? [src, 'self', ...pre, ...tail] : [src, 'self', ...pre, helper.id, last, ...tail];
          const f: Flow = { paths: [path], dir: 'both' };
          if (k) f.from = +(ups[k - 1] + 0.35).toFixed(2);
          if (k < n) f.until = ups[k];
          out.push(f);
        }
        return out;
      },
      exits: [], tags: n > 1 ? ['punch', 'viaRelay', 'direct', 'multihop'] : ['punch', 'viaRelay', 'direct'], selfZone: true,
    };
  },

  bond(R, W, b, i, S) {
    const p = 'b' + i;
    const ex: SpecNode = { id: p + 'x', role: 'exit', x: r1(455 + R.range(-15, 15)), y: r1(b.cy) };
    // a narrow lens: the pair stays inside its band even on a diagonal
    const len = Math.hypot(ex.x - S.x, ex.y - S.y), curve = Math.min(0.16, (b.h * 0.22) / Math.max(len, 1));
    const quality = R.chance(0.45);
    // documentation addresses (RFC 5737)
    const a1 = R.pick(['203.0.113.10', '203.0.113.24']), a2 = R.pick(['198.51.100.7', '198.51.100.31']);
    const t = R.range(3.0, 4.0);
    return {
      nodes: [ex, { id: p + 'net', role: 'internet', x: RIGHT - 4, y: ex.y }],
      links: [
        { id: p + 'm1', a: 'self', b: ex.id, kind: 'peer', curve: -curve, chips: [{ t: 0.3, text: 'wan1' }, { t: 0.78, text: a1 }] },
        { id: p + 'm2', a: 'self', b: ex.id, kind: 'peer', curve, chips: [{ t: 0.3, text: 'wan2' }, { t: 0.78, text: a2 }] },
        { id: p + 'eg', a: ex.id, b: p + 'net', kind: 'egress' },
      ],
      notes: b.h >= 140 ? [{ x: r1((S.x + ex.x) / 2 + 10), y: ex.y, key: quality ? 'quality' : 'aggregate' }] : [],
      // quality: when wan1 drops, what is on it turns back and goes over wan2
      events: quality ? [{ at: +t.toFixed(2), link: p + 'm1', state: 'degraded' }, { at: +(t + 1.1).toFixed(2), link: p + 'm1', state: 'offline', detour: { a: 'self', b: ex.id, via: [] } }] : [],
      // quality: one link at a time; aggregate: the engine alternates parallel links
      exits: [quality
        ? { path: [ex.id, p + 'net'], first: p + 'm1', only: [p + 'm1', p + 'eg'], until: +(t + 1.1).toFixed(2), then: [p + 'm2', p + 'eg'] }
        : { path: [ex.id, p + 'net'], first: p + 'm1' }],
      tags: ['multiAddr', 'multiWan', 'perAddrIface', quality ? 'quality' : 'aggregate'],
    };
  },

  failover(R, W, b, i, S) {
    const p = 'f' + i, dy = Math.min(b.h * 0.3, 70);
    const h1: SpecNode = { id: p + 'h1', role: 'relay', x: r1(S.x + 140), y: r1(b.cy - dy) };
    const h2: SpecNode = { id: p + 'h2', role: 'relay', x: r1(S.x + 140), y: r1(b.cy + dy) };
    const ex: SpecNode = { id: p + 'x', role: 'exit', x: r1(475 + R.range(-10, 10)), y: r1(b.cy) };
    const t = R.range(2.8, 3.6), off = t + 1.3, back = off;
    return {
      nodes: [h1, h2, ex, { id: p + 'net', role: 'internet', x: RIGHT - 4, y: ex.y }],
      links: [
        { id: p + 'p1', a: 'self', b: h1.id, kind: 'peer' }, { id: p + 'q1', a: h1.id, b: ex.id, kind: 'peer' },
        { id: p + 'p2', a: 'self', b: h2.id, kind: 'peer' }, { id: p + 'q2', a: h2.id, b: ex.id, kind: 'peer' },
        { id: p + 'eg', a: ex.id, b: p + 'net', kind: 'egress' },
      ],
      events: [
        { at: +t.toFixed(2), link: p + 'p1', state: 'degraded' },
        // what is on the way over h1 turns back and goes over h2
        { at: +off.toFixed(2), link: p + 'p1', state: 'offline', detour: { a: 'self', b: ex.id, via: [h2.id] } },
        { at: +off.toFixed(2), link: p + 'q1', state: 'offline', detour: { a: 'self', b: ex.id, via: [h2.id] } },
        { at: +off.toFixed(2), node: h1.id, state: 'off' },
      ],
      exits: [{ path: [h1.id, ex.id, p + 'net'], first: p + 'p1', until: +off.toFixed(2), alt: [h2.id, ex.id, p + 'net'], from: +back.toFixed(2) }],
      tags: ['quality', 'failover', 'noPathWait'],
    };
  },

  native(R, W, b, i) {
    const p = 'n' + i;
    const nv: SpecNode = { id: p + 'v', role: 'native', x: r1(420 + R.range(-25, 25)), y: r1(b.cy + R.range(-1, 1) * Math.min(b.h * 0.15, 18)) };
    return {
      nodes: [nv, { id: p + 'net', role: 'internet', x: RIGHT - 4, y: nv.y }],
      links: [{ id: p + 'l', a: 'self', b: nv.id, kind: 'native' }, { id: p + 'eg', a: nv.id, b: p + 'net', kind: 'egress' }],
      exits: [{ path: [nv.id, p + 'net'], first: p + 'l' }],
      tags: ['native', 'compat'],
    };
  },

  offline(R, W, b, i, S) {
    const p = 'o' + i;
    const a: SpecNode = { id: p + 'a', role: 'peer', x: r1(S.x + 170 + R.range(-20, 20)), y: r1(b.cy) };
    const c: SpecNode = { id: p + 'c', role: 'peer', x: r1(a.x + 150), y: r1(b.cy + R.range(-1, 1) * Math.min(b.h * 0.2, 26)), state: 'off' };
    return {
      nodes: [a, c],
      links: [{ id: p + 'l', a: 'self', b: a.id, kind: 'peer' }, { id: p + 'x', a: a.id, b: c.id, kind: 'peer', state: 'offline' }],
      exits: [], tags: [],
    };
  },

  local(R, W, b, i) {
    const p = 'k' + i;
    return {
      nodes: [{ id: p + 'net', role: 'internet', x: r1(500 + R.range(-30, 20)), y: r1(b.cy) }],
      links: [{ id: p + 'eg', a: 'self', b: p + 'net', kind: 'egress', curve: R.range(-0.05, 0.05) }],
      exits: [{ path: [p + 'net'], first: p + 'eg', local: true }],
      tags: ['localOut'],
    };
  },

  remote(R, W, b, i, S) {
    const p = 'r' + i;
    const h: SpecNode = { id: p + 'h', role: 'relay', x: r1(S.x + 150), y: r1(b.cy - Math.min(b.h * 0.15, 24)) };
    const rm: SpecNode = { id: p + 'm', role: 'remote', x: r1(480 + R.range(-15, 15)), y: r1(b.cy + Math.min(b.h * 0.1, 14)), badge: 'key' };
    return {
      nodes: [h, rm],
      links: [{ id: p + 'l1', a: 'self', b: h.id, kind: 'peer' }, { id: p + 'l2', a: h.id, b: rm.id, kind: 'peer' }],
      notes: [{ x: r1(rm.x - 10), y: r1(rm.y - 44), text: '/scale/remote/…' }],
      exits: [], admin: [h.id, rm.id],
      tags: ['remoteAdmin', 'noPw'],
    };
  },
};

// ------------------------------------------------------------------ generate
export function generate(seed: string, weights: Partial<Weights> = {}): TopoSpec {
  const W = { ...DEFAULT_WEIGHTS, ...weights };
  for (let attempt = 0; attempt < 12; attempt++) {
    const spec = build(attempt ? `${seed}/${attempt}` : seed, W);
    if (!validateSpec(spec).length) return { ...spec, seed };
  }
  return { ...build(seed + '/fallback', { ...W, punch: 0, failover: 0, remoteAdmin: 0 }), seed };
}

const PROTOS = ['WireGuard', 'IKEv2', 'L2TP', 'OpenVPN', 'SOCKS5', 'HTTP', 'Shadowsocks', 'Hysteria 2'];
const EXIT_KINDS: Kind[] = ['chain', 'bond', 'failover', 'native'];

function build(seed: string, W: Weights): TopoSpec {
  const R = makeRng(seed);
  const inTags: string[] = [], policyTags: string[] = [];

  // inbound
  const inbound = R.wpick([
    { k: 'single', w: 1 },
    { k: 'multi', w: W.multiProto },
    { k: 'admin', w: W.remoteAdmin },
  ])!.k;
  const S: Self = { x: inbound === 'multi' ? 230 : r1(185 + R.range(-15, 15)), y: r1(215 + R.range(-14, 14)) };

  // outbound modules
  const k = R.wpick([{ n: 1, w: 0.22 }, { n: 2, w: 0.5 }, { n: 3, w: 0.28 }])!.n;
  const kinds: Kind[] = [];
  if (inbound === 'admin') kinds.push('remote');
  const pool: { k: Kind; w: number }[] = [
    { k: 'chain', w: 1.0 + W.multihop * 0.5 }, { k: 'gate', w: W.hopAuth }, { k: 'punch', w: W.punch },
    { k: 'bond', w: W.multiAddr }, { k: 'failover', w: W.failover }, { k: 'native', w: W.native }, { k: 'offline', w: W.offline },
  ];
  while (kinds.length < k) {
    const avail = pool.filter((x) => !kinds.includes(x.k) || (x.k === 'chain' && kinds.filter((y) => y === 'chain').length < 2));
    const x = R.wpick(avail);
    if (!x) break;
    kinds.push(x.k);
  }
  // every topology except a pure admin one needs at least one exit
  if (inbound !== 'admin' && !kinds.some((x) => EXIT_KINDS.includes(x))) {
    if (kinds.length >= 3) kinds.pop();
    kinds.push(R.wpick<{ k: Kind; w: number }>([{ k: 'chain', w: 2 }, { k: 'native', w: W.native }, { k: 'bond', w: W.multiAddr }])!.k);
  }

  // routing policy
  let exitCount = kinds.filter((x) => EXIT_KINDS.includes(x)).length;
  const devCount = inbound === 'multi' ? R.int(2, 4) : 1;
  const policies = [{ k: 'plain', w: 1 }];
  if (exitCount >= 1) policies.push({ k: 'rules', w: W.rules });
  if (exitCount >= 2 && devCount >= 2) policies.push({ k: 'perUser', w: W.perUserExit });
  let policy = inbound === 'admin' ? 'plain' : R.wpick(policies)!.k;
  // rules: domain + IP rule over two exits, or one exit plus local breakout
  if (policy === 'rules' && (exitCount < 2 || R.chance(0.45)) && kinds.length < 3) { kinds.push('local'); exitCount++; }
  if (policy === 'rules' && exitCount < 2) policy = 'plain';

  // bands: local breakout last; longer punch chains are rarer and taller
  kinds.sort((a, b) => Number(a === 'local') - Number(b === 'local'));
  const opts: Opts = { punchN: R.wpick([{ n: 1, w: 1 }, { n: 2, w: 0.8 }, { n: 3, w: 0.55 }])!.n };
  const order = R.chance(0.5) ? kinds : kinds.slice().reverse();
  const size = (kind: Kind) => (kind === 'punch' && opts.punchN > 1 ? 1.4 + 0.25 * opts.punchN : 1);
  const unit = (BOTTOM - TOP) / order.reduce((t, kind) => t + size(kind), 0);
  let y0 = TOP;
  const mods = order.map((kind, i) => {
    const h = unit * size(kind), band = { cy: y0 + h / 2, h };
    y0 += h;
    return MODS[kind](R, W, band, i, S, opts);
  });

  // inbound nodes
  const nodes: SpecNode[] = [{ id: 'self', role: 'self', x: S.x, y: S.y }];
  const links: SpecLink[] = [], zones: Zone[] = [], notes: Note[] = [], events: SpecEvent[] = [], flows: Flow[] = [], signals: Signal[] = [];
  const devs: string[] = [];
  if (inbound === 'admin') {
    nodes.push({ id: 'adm', role: 'admin', x: 58, y: S.y });
    links.push({ id: 'aa', a: 'adm', b: 'self', kind: 'access' });
    devs.push('adm');
  } else if (inbound === 'single') {
    nodes.push({ id: 'dev', role: 'device', x: 58, y: S.y, icon: R.pick<DeviceIcon>(['laptop', 'laptop', 'phone', 'monitor', 'router']) });
    links.push({ id: 'a0', a: 'dev', b: 'self', kind: 'access' });
    devs.push('dev');
  } else {
    const protos = PROTOS.slice().sort(() => R.f() - 0.5);
    const gap = Math.min(104, (BOTTOM - TOP) / devCount);
    const icons: DeviceIcon[] = ['laptop', 'phone', 'monitor', 'router'];
    for (let d = 0; d < devCount; d++) {
      const id = d ? 'd' + (d + 1) : 'dev';
      const y = S.y + (d - (devCount - 1) / 2) * gap;
      const chips = devCount <= 3 && R.chance(0.6)
        ? [{ t: 0.42, text: protos[d * 2], dy: -9 }, { t: 0.42, text: protos[d * 2 + 1], dy: 9 }]
        : [{ t: 0.45, text: protos[d] }];
      nodes.push({ id, role: 'device', x: 50, y: r1(y), icon: icons[d], label: false });
      links.push({ id: 'a' + d, a: id, b: 'self', kind: 'access', chips });
      devs.push(id);
    }
    inTags.push('multiProto');
    if (R.chance(W.quota / (W.quota + 1))) {
      const qid = R.pick(devs);
      nodes.find((x) => x.id === qid)!.badge = 'quota';
      inTags.push('quota');
    }
  }

  // merge modules
  let selfZone = false, hold = 0;
  for (const m of mods) {
    nodes.push(...m.nodes); links.push(...m.links);
    zones.push(...(m.zones ?? [])); notes.push(...(m.notes ?? [])); events.push(...(m.events ?? [])); signals.push(...(m.signals ?? [])); hold = Math.max(hold, m.hold ?? 0);
    if (m.selfZone) selfZone = true;
  }
  // the zone label sits under the self label
  if (selfZone) zones.push({ x: S.x - 42, y: S.y - 38, w: 84, h: 102, label: 'NAT' });

  // flows
  const linkIdsOf = (src: string) => links.filter((l) => l.a === src || l.b === src).map((l) => l.id);
  const route = (src: string, e: ExitRoute): Flow[] => {
    const path = [src, 'self', ...e.path];
    if (e.only) return [   // quality bond: one link, then the other after it fails
      { paths: [path], dir: 'both', only: [...e.only, ...linkIdsOf(src)], until: e.until },
      { paths: [path], dir: 'both', only: [...e.then!, ...linkIdsOf(src)], from: e.until! },
    ];
    if (e.alt) return [
      { paths: [path], dir: 'both', until: e.until },
      { paths: [[src, 'self', ...e.alt]], dir: 'both', from: e.from },
    ];
    return [{ paths: [path], dir: 'both', every: e.path.length > 1 && mods.length === 1 ? 0.32 : 0.5 }];
  };
  const exits = mods.flatMap((m) => m.exits);

  if (inbound === 'admin') {
    const rm = mods.find((m) => m.admin)!;
    flows.push({ paths: [['adm', 'self', ...rm.admin!]], dir: 'both', every: 0.5 });
  } else if (policy === 'perUser') {
    devs.forEach((d, j) => flows.push(...route(d, exits[j % exits.length])));
    policyTags.push('perUserExit');
  } else if (policy === 'rules') {
    const ruleChips = [
      { t: 0.56, text: R.pick(['*.example.com', '*.example.org', 'example.net']) },
      { t: 0.56, text: R.pick(['198.51.100.0/24', '203.0.113.0/24', '192.0.2.0/24']) },
    ];
    let ci = 0;
    exits.forEach((e) => {
      const l = links.find((x) => x.id === e.first)!;
      if (e.local) l.chips = [{ t: 0.56, key: 'other' }];
      else if (ci < 2) { l.chips = (l.chips ?? []).filter((c) => c.t > 0.7).concat(ruleChips[ci]); policyTags.push(ci ? 'ipRule' : 'domainRule'); ci++; }
      else return;
      devs.forEach((d) => route(d, e).forEach((f) => flows.push({ ...f, every: 0.9 })));
    });
  } else {
    // plain: every device uses the first exit, other exits carry a light background flow
    exits.forEach((e, j) => devs.forEach((d, di) => {
      if (j && di) return;
      route(d, e).forEach((f) => flows.push(j ? { ...f, every: 1.1 } : f));
    }));
  }
  // punch modules carry their own site-to-site flow from the first device
  if (inbound !== 'admin') mods.forEach((m) => m.ownFlows?.(devs[0]).forEach((f) => flows.push({ ...f, every: 0.6 })));

  // tags: every picked part shows its main tag first, then secondary tags
  // round-robin up to MAX_TAGS; the generic QUIC / exit tags only fill up.
  const groups = [inTags, ...mods.map((m) => m.tags), policyTags].filter((g) => g.length);
  const tags: string[] = [];
  for (let r = 0; tags.length < MAX_TAGS && groups.some((g) => g.length > r); r++) {
    for (const g of groups) if (g[r] && !tags.includes(g[r]) && tags.length < MAX_TAGS) tags.push(g[r]);
  }
  if (tags.length < 3) ['quic', 'exitNode'].forEach((t) => { if (tags.length < 3 && !tags.includes(t) && (t === 'quic' || exitCount)) tags.push(t); });

  return { seed, tags, nodes, links, zones, notes, events, flows, signals, hold: hold || undefined };
}
