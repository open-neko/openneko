/**
 * Demo mode: on with NEXT_PUBLIC_DEMO=true (preferred) or the legacy
 * DEMO=true server-side flag. When on, /api/onboarding/status returns ready
 * with the default seats, so the wizard does not open.
 */

export function isDemoMode(): boolean {
  return (
    process.env.NEXT_PUBLIC_DEMO === "true" || process.env.DEMO === "true"
  );
}

export const DEMO_SEATS = ["CEO", "CFO", "CRO"] as const;
