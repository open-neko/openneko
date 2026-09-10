import Link from "next/link";
import { CircleAlert } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";

export function RecordsDegradedBanner({ message }: { message: string }) {
  return (
    <Alert variant="destructive" className="records-degraded">
      <CircleAlert aria-hidden="true" />
      <AlertTitle>Records are unavailable.</AlertTitle>
      <AlertDescription>
        {message} No fallback database read was attempted.
      </AlertDescription>
      <Link href="/admin">Open health settings</Link>
    </Alert>
  );
}

export function RecordsUnavailable({ message }: { message: string }) {
  return (
    <main className="records-root">
      <RecordsDegradedBanner message={message} />
      <section className="records-unavailable-panel">
        <span className="records-eyebrow">Protected data plane</span>
        <h1>This app is paused</h1>
        <p>
          OpenNeko will not bypass GraphJin or query application tables directly
          while records are degraded.
        </p>
      </section>
    </main>
  );
}
