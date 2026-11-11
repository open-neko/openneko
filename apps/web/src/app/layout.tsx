import type { Metadata, Viewport } from "next";
import { DensityProvider } from "@/components/DensityProvider";
import AppRail, { type RailIdentity } from "@/components/AppRail";
import { railIdentity } from "@/lib/rail-identity";
import CommandDock from "@/components/CommandDock";
import CommandBar from "@/components/CommandBar";
import AppToaster from "@/components/AppToaster";
import { THEME_COLOR } from "@/lib/theme-color";
import "@fontsource-variable/archivo/wght.css";
import "@fontsource-variable/manrope/wght.css";
import "./globals.css";
import "./styles/_operations.css";
import "./styles/_library.css";
import { mapStyleSetting } from "@/lib/map-style";

// Set data-density before paint from the persisted choice (default compact),
// so the dense layout never flashes the comfortable one on load.
const DENSITY_INIT = `(function(){try{var d=localStorage.getItem('neko-density');document.documentElement.setAttribute('data-density',d==='comfortable'?'comfortable':'compact');}catch(e){document.documentElement.setAttribute('data-density','compact');}})();`;

const DESCRIPTION = "AI that helps run your business.";

export const metadata: Metadata = {
  title: "OpenNeko",
  description: DESCRIPTION,
  openGraph: { title: "OpenNeko", description: DESCRIPTION, siteName: "OpenNeko", type: "website" },
  twitter: { card: "summary", title: "OpenNeko", description: DESCRIPTION },
};

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  viewportFit: "cover",
  themeColor: THEME_COLOR,
};

export default async function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  const initial: RailIdentity | undefined = await railIdentity();
  return (
    <html lang="en" data-density="compact" data-map-style={mapStyleSetting()} suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: DENSITY_INIT }} />
      </head>
      <body>
        <a href="#main-content" className="skip-to-main">
          Skip to main content
        </a>
        <DensityProvider>
          <AppRail initial={initial} />
          <div id="main-content">{children}</div>
          <CommandDock />
          <CommandBar />
        </DensityProvider>
        <AppToaster />
      </body>
    </html>
  );
}
