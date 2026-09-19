/**
 * Executable identity shipped by the OpenNeko agent image.
 *
 * OpenShell binds model egress to /proc/<pid>/exe, so this is part of the
 * vendored runtime contract, not an installation setting. The release smoke
 * compares it with Hermes' resolved interpreter inside the published image.
 */
export const VENDORED_HERMES_MODEL_BINARY = "/usr/bin/python3.11";

/**
 * Process-local switch honored by OpenNeko's pinned Hermes compatibility
 * patch. It removes `delegate_task` from the model-visible tool schema and
 * rejects direct dispatches for that Hermes process.
 */
export const HERMES_NATIVE_DELEGATION_ENV =
  "OPENNEKO_HERMES_NATIVE_DELEGATION";
export const HERMES_NATIVE_DELEGATION_DISABLED = "disabled";

/** Opt-in standalone Go harness executable; Hermes identity stays unchanged. */
export const VENDORED_HARNESS_MODEL_BINARY = "/usr/local/bin/harness-openneko";
