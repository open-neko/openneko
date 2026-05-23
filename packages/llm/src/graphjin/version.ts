// Pinned GraphJin binary release. Keep in sync with:
//   - Dockerfile ARG GRAPHJIN_VERSION
//   - scripts/install-graphjin-binary.ts download URL
// Bump together; a mismatch between embedded server (worker spawn) and
// CLI surface (agent path) is hard to debug after the fact.
export const GRAPHJIN_VERSION = "3.18.25";
