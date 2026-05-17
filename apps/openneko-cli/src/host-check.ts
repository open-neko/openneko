import { existsSync } from "node:fs";

export interface HostCheckResult {
  supported: boolean;
  triple: string;
  reason?: string;
}

/**
 * Returns whether microsandbox is expected to run on this host. Used
 * by `openneko doctor` and as a guard before `openneko install`. The
 * actual triple list lives in apps/worker/src/plugins/microsandbox-sdk.ts
 * — keep them in sync.
 */
export function checkHost(): HostCheckResult {
  if (process.platform === "darwin") {
    if (process.arch !== "arm64") {
      return {
        supported: false,
        triple: `${process.platform}-${process.arch}`,
        reason:
          "macOS x86_64 is not supported — microsandbox bundles only arm64. Run OpenNeko on Apple Silicon, or on a Linux host with KVM.",
      };
    }
    return { supported: true, triple: "darwin-arm64" };
  }
  if (process.platform === "linux") {
    if (process.arch !== "x64" && process.arch !== "arm64") {
      return {
        supported: false,
        triple: `${process.platform}-${process.arch}`,
        reason: `Linux ${process.arch} not supported by the bundled microsandbox runtime.`,
      };
    }
    if (!existsSync("/dev/kvm")) {
      return {
        supported: false,
        triple: process.arch === "x64" ? "linux-x64-gnu" : "linux-arm64-gnu",
        reason:
          "/dev/kvm not found — microsandbox needs KVM (nested virtualization in cloud VMs may need the host enabled).",
      };
    }
    return {
      supported: true,
      triple: process.arch === "x64" ? "linux-x64-gnu" : "linux-arm64-gnu",
    };
  }
  return {
    supported: false,
    triple: `${process.platform}-${process.arch}`,
    reason:
      "Windows is not currently supported. WSL2 viability is being evaluated.",
  };
}
