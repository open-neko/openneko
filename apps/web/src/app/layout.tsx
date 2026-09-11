import type { Metadata, Viewport } from "next";
import { Toaster } from "sonner";
import { DensityProvider } from "@/components/DensityProvider";
import AppRail from "@/components/AppRail";
import CommandDock from "@/components/CommandDock";
import { THEME_COLOR } from "@/lib/theme-color";
import "@fontsource-variable/archivo/wght.css";
import "@fontsource-variable/manrope/wght.css";
import "./globals.css";
import "./styles/_operations.css";
import "./styles/_library.css";

// Set data-density before paint from the persisted choice (default compact),
// so the dense layout never flashes the comfortable one on load.
const DENSITY_INIT = `(function(){try{var d=localStorage.getItem('neko-density');document.documentElement.setAttribute('data-density',d==='comfortable'?'comfortable':'compact');}catch(e){document.documentElement.setAttribute('data-density','compact');}})();`;

export const metadata: Metadata = {
  title: "OpenNeko",
  description: "Executive intelligence for CXOs",
};

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  viewportFit: "cover",
  themeColor: THEME_COLOR,
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" data-density="compact" suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: DENSITY_INIT }} />
      </head>
      <body>
        <a href="#main-content" className="skip-to-main">
          Skip to main content
        </a>
        <DensityProvider>
          <AppRail />
          <div id="main-content">{children}</div>
          <CommandDock />
        </DensityProvider>
        <Toaster
          position="bottom-right"
          expand
          closeButton
          gap={10}
          offset={20}
          visibleToasts={4}
          toastOptions={{
            duration: 4500,
            unstyled: true,
            classNames: {
              toast: "app-toast",
              title: "app-toast-title",
              description: "app-toast-desc",
              closeButton: "app-toast-close",
              icon: "app-toast-icon",
              content: "app-toast-content",
              actionButton: "app-toast-action",
              cancelButton: "app-toast-cancel",
              success: "app-toast-success",
              error: "app-toast-error",
              info: "app-toast-info",
              warning: "app-toast-warning",
            },
          }}
        />
      </body>
    </html>
  );
}
