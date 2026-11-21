import { Field } from "@/components/ui/field";

export type UnconfinedTurnsState = {
  setting: { on: boolean; host?: string; kernel?: string; reason?: string; hostUser?: string; at?: string };
  system: { available: boolean; blocked?: string; host?: string; kernel?: string; reason?: string };
  applies: boolean;
  notApplied?: string;
  commands: { allow: string; deny: string };
};

const label = "Agent turns without OS confinement";

function Command({ text }: { text: string }) {
  return <code className="block rounded-[4px] bg-neutral px-2 py-1 font-mono text-ui-body-sm text-text2 break-all">{text}</code>;
}

export default function UnconfinedTurnsField({ initial }: { initial: UnconfinedTurnsState }) {
  const { setting, system, applies, notApplied, commands } = initial;

  if (applies) {
    return (
      <Field label={label}>
        <div role="alert" className="text-danger text-ui-body-sm" data-testid="unconfined-on">
          Allowed. Agent turns run without OS confinement. The agent can read the files and reach the network of the
          OpenNeko account. Host user {setting.hostUser} allowed it on {setting.host} ({setting.kernel}) at {setting.at}.
          {setting.reason ? ` Reason: ${setting.reason}.` : ""}
        </div>
        <span className="text-text3 text-ui-body-sm">To refuse these turns again, run this on the host:</span>
        <Command text={commands.deny} />
      </Field>
    );
  }

  if (!system.available) {
    return (
      <Field label={label} hint={`Denied. ${system.blocked ?? ""}`}>
        <span className="text-text3 text-ui-body-sm" data-testid="unconfined-unavailable">
          The host operator can allow these turns only on a host that cannot confine them.
        </span>
      </Field>
    );
  }

  return (
    <Field label={label} hint={`Denied, so agent turns do not run. ${system.reason ?? ""}`}>
      {setting.on && notApplied ? (
        <span className="text-text3 text-ui-body-sm" data-testid="unconfined-not-applied">
          An override exists and does not apply: {notApplied}.
        </span>
      ) : null}
      <div className="text-danger text-ui-body-sm" data-testid="unconfined-warning">
        The operator of host {system.host} can allow agent turns without OS confinement. Each turn can then read the files
        and reach the network of the OpenNeko account, also the secret key and the database login. The audit log records
        the host user. Run this on the host as the account that owns the install, or as root for a server install:
      </div>
      <Command text={commands.allow} />
    </Field>
  );
}
