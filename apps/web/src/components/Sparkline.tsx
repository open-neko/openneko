// Tiny bar sparkline — shared by the workflows list and the hours-saved hero.
// Pure presentational: bars scaled to the series max, faded for zero days.

export function Sparkline({
  values,
  width = 88,
  height = 16,
}: {
  values: number[];
  width?: number;
  height?: number;
}) {
  if (values.length === 0) return null;
  const max = Math.max(1, ...values);
  const barW = width / values.length;
  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      aria-hidden="true"
      style={{ display: "block" }}
    >
      {values.map((v, i) => {
        const h = (v / max) * height;
        return (
          <rect
            key={i}
            x={i * barW + 0.5}
            y={height - h}
            width={Math.max(1, barW - 1)}
            height={h}
            fill="var(--text3)"
            opacity={v === 0 ? 0.35 : 1}
          />
        );
      })}
    </svg>
  );
}
