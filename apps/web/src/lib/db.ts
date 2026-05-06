/**
 * Web-app facade over @neko/db. Re-exports the shared org resolver so
 * routes and pages can `import { getOrgId } from "@/lib/db"`.
 *
 * Single-tenant for now — getOrgId() returns the id of the (currently
 * one) organization row, creating it on first call if none exists.
 */

export { getOrgId } from "@neko/db";

export type OnboardingStatus =
  | { state: "needs_wizard" }
  | { state: "processing"; jobId: string }
  | { state: "ready"; profileVersion: number; seats: string[] }
  | { state: "failed"; jobId: string; message: string }
  | { state: "db_error"; message: string };
