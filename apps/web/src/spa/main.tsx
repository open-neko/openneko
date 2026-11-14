import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RouterProvider, createBrowserRouter } from "react-router";
import { registerServiceWorker } from "./pwa";
import { routes } from "./routes";
import "@fontsource-variable/archivo/wght.css";
import "@fontsource-variable/manrope/wght.css";
import "@/app/globals.css";
import "@/app/styles/_operations.css";
import "@/app/styles/_library.css";

void registerServiceWorker();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <RouterProvider router={createBrowserRouter(routes)} />
  </StrictMode>,
);
