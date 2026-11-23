"use client";

import { Button } from "@/components/ui/button";
import { Field, Textarea } from "@/components/ui/field";

export function egressHostLines(text: string): string[] {
  const hosts: string[] = [];
  for (const line of text.split(/[\n,]/)) {
    const host = line.trim();
    if (host && !hosts.includes(host)) hosts.push(host);
  }
  return hosts;
}

export default function EgressHostsField({
  value,
  onChange,
  suggestions,
  available,
}: {
  value: string;
  onChange: (next: string) => void;
  suggestions: readonly string[];
  available: boolean;
}) {
  const listed = egressHostLines(value);
  const offered = suggestions.filter((host) => !listed.includes(host));
  return (
    <Field
      label="Hosts the terminal may reach"
      htmlFor="agent-egress-hosts"
      hint={
        available
          ? "One host per line. Commands reach only these hosts, through the sandbox's proxy. A host name means port 443. Add :port for another port, and use *.example.com for the subdomains of example.com. An empty list means no internet. Applies from the next command."
          : "Unavailable: this install has no sandbox service."
      }
    >
      <Textarea
        id="agent-egress-hosts"
        value={value}
        disabled={!available}
        placeholder="pypi.org"
        spellCheck={false}
        autoCapitalize="none"
        autoCorrect="off"
        rows={3}
        onChange={(e) => onChange(e.target.value)}
      />
      {available && offered.length > 0 && (
        <div className="flex flex-wrap items-center gap-2" data-testid="egress-suggestions">
          <span className="text-text3 text-ui-body-sm">Hosts that installed packs name:</span>
          {offered.map((host) => (
            <Button
              key={host}
              type="button"
              variant="secondary"
              size="sm"
              onClick={() => onChange([...listed, host].join("\n"))}
            >
              Add {host}
            </Button>
          ))}
        </div>
      )}
    </Field>
  );
}
