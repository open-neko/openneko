import type { Metadata } from "next";
import { Archivo, Manrope } from "next/font/google";
import { Toaster } from "sonner";
import "./globals.css";

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
  title: "Neko",
  description: "Executive intelligence for CXOs",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" className={`${display.variable} ${body.variable}`}>
      <body>
        {children}
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
              width: 360,
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
