// HY2 SCALE logo, inline so its colours follow the theme (.lg-* classes in
// login.css). Below 40 px the grey link between the corner dots is dropped:
// it turns into noise at that size (same as the small logo / favicon).
const CHAIN = 'M128,128 L173.42,139.47 L167.92,201.86 L230.3,196.36 L224.81,258.75 L287.19,253.25 L281.7,315.64 L344.08,310.14 L338.58,372.53 L384,384';
const NODES: [number, number][] = [[256, 104], [408, 256], [256, 408], [104, 256]];

export default function BrandLogo({ size = 28 }: { size?: number }) {
  return (
    <svg className="lg-logo" width={size} height={size} viewBox="0 0 512 512" role="img" aria-label="HY2 SCALE">
      {size >= 40 && (
        <path className="lg-chain" d={CHAIN} strokeWidth={10} strokeLinejoin="miter" strokeMiterlimit={8} strokeLinecap="round" />
      )}
      <circle className="lg-ring" cx={128} cy={128} r={20} />
      <circle className="lg-ring" cx={384} cy={384} r={20} />
      {NODES.map(([x, y]) => (
        <g key={`${x},${y}`}>
          <circle className="lg-ring" cx={x} cy={y} r={56} />
          <circle className="lg-hub" cx={x} cy={y} r={36} />
        </g>
      ))}
      <path className="lg-ink" d="M213.24,183 L247.24,183 L218.76,329 L184.76,329Z" />
      <path className="lg-ink" d="M293.24,183 L327.24,183 L298.76,329 L264.76,329Z" />
      <path className="lg-arrow" d="M64,448 L254,230 L258,282 L448,64 M384,64 L448,64 L448,128 M128,448 L64,448 L64,384"
        strokeWidth={32} strokeLinejoin="miter" strokeMiterlimit={10} strokeLinecap="butt" />
    </svg>
  );
}
