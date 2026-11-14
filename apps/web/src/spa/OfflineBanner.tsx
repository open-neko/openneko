import { WifiOff } from "lucide-react";
import { useOnlineStatus } from "./pwa";

export default function OfflineBanner() {
  const online = useOnlineStatus();
  if (online) return null;

  return (
    <div
      role="status"
      aria-live="polite"
      data-testid="offline-banner"
      className="border-b border-border bg-warn-soft px-4 py-2.5 text-ui-body-sm font-medium text-warn-ink"
    >
      <div className="mx-auto flex max-w-[1200px] items-center gap-2">
        <WifiOff className="size-4 shrink-0" aria-hidden="true" strokeWidth={2} />
        <span>You are offline. OpenNeko will reconnect when your connection returns.</span>
      </div>
    </div>
  );
}
