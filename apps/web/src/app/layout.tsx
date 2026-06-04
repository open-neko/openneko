import type { Metadata, Viewport } from "next";
import { Archivo, Manrope } from "next/font/google";
import { Toaster } from "sonner";
import { DensityProvider } from "@/components/DensityProvider";
import "./globals.css";

// Set data-density before paint from the persisted choice (default compact),
// so the dense layout never flashes the comfortable one on load.
const DENSITY_INIT = `(function(){try{var d=localStorage.getItem('neko-density');document.documentElement.setAttribute('data-density',d==='comfortable'?'comfortable':'compact');}catch(e){document.documentElement.setAttribute('data-density','compact');}})();`;

const display = Archivo({
  variable: "--font-display",
  subsets: ["latin"],
  weight: ["600", "700", "800", "900"],
});

const body = Manrope({
  variable: "--font-body",
  subsets: ["latin"],
});

export const metadata: Metadata = {
  title: "OpenNeko",
  description: "Executive intelligence for CXOs",
};

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  viewportFit: "cover",
  themeColor: "#FAFAF7",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" data-density="compact" className={`${display.variable} ${body.variable}`}>
      <head>
        <script dangerouslySetInnerHTML={{ __html: DENSITY_INIT }} />
      </head>
      <body>
        <DensityProvider>{children}</DensityProvider>
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
            style: {
              width: "min(360px, calc(100vw - 32px))",
              display: "flex",
              alignItems: "flex-start",
              gap: 12,
              padding: "14px 16px 14px 18px",
              background: "#FFFFFF",
              color: "#2D2A24",
              border: "1px solid #EEEBE4",
              borderRadius: 16,
              boxShadow:
                "0 1px 2px rgba(20,18,12,0.04), 0 12px 40px -8px rgba(20,18,12,0.12), 0 24px 60px -16px rgba(20,18,12,0.08)",
              fontFamily: "var(--font-body), 'Manrope', sans-serif",
              fontSize: 13,
              fontWeight: 400,
              lineHeight: 1.5,
              boxSizing: "border-box",
              position: "relative",
              overflow: "hidden",
            },
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
