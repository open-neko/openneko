"use client";

import PackSetup, { type PackApplied } from "@/components/admin/PackSetup";
import { registerComponent } from "./renderer";
import type { RenderContext } from "./renderer";
import type { PackSetupProps } from "./catalog";
import type { A2UIComponent } from "./types";

const PACK_ID = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

export function packAppliedFollowUp(result: PackApplied): Record<string, unknown> {
  return {
    prompt: result.status === "installed"
      ? `I applied the ${result.name} pack in the card. Check that its data source answers, run one small sample query, and tell me what I can ask next.`
      : `I installed the ${result.name} pack in the card, but it reports "${result.status}". Tell me what to finish.`,
    packId: result.packId,
    operation: result.operation,
  };
}

registerComponent("PackSetup", (comp: A2UIComponent, ctx: RenderContext) => {
  const props = comp as unknown as PackSetupProps & { id: string };
  const packId = typeof props.packId === "string" ? props.packId : "";
  if (!PACK_ID.test(packId)) return null;
  return <PackSetup
    packId={packId}
    variant="chat"
    onApplied={result => ctx.onAction?.(props.id, "pack_setup_applied", packAppliedFollowUp(result))}
  />;
});
