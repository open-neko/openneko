const SHELL_VERSION = "__OPENNEKO_SHELL_VERSION__";
const SHELL_ASSETS = [
  "/index.html",
  "/manifest.webmanifest",
  "/version.json",
  "/favicon.ico",
  "/icon.png",
  "/icon-192.png",
  "/icon-512.png",
  "/cat.png",
];

const CACHE_PREFIX = "openneko-shell-";
const CACHE_NAME = `${CACHE_PREFIX}${SHELL_VERSION}`;

self.addEventListener("install", (event) => {
  event.waitUntil(
    (async () => {
      const cache = await caches.open(CACHE_NAME);
      await Promise.all(
        SHELL_ASSETS.map(async (url) => {
          const init = url.startsWith("/assets/") ? { cache: "force-cache" } : { cache: "no-cache" };
          const response = await fetch(new Request(url, init));
          if (!response.ok) {
            throw new Error(`Failed to cache ${url}: ${response.status}`);
          }
          await cache.put(url, response);
        }),
      );
      await self.skipWaiting();
    })(),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    (async () => {
      const keys = await caches.keys();
      await Promise.all(
        keys
          .filter((key) => key.startsWith(CACHE_PREFIX) && key !== CACHE_NAME)
          .map((key) => caches.delete(key)),
      );
      await self.clients.claim();
    })(),
  );
});

self.addEventListener("message", (event) => {
  if (event.data && event.data.type === "SKIP_WAITING") {
    void self.skipWaiting();
  }
});

self.addEventListener("fetch", (event) => {
  const { request } = event;
  if (request.method !== "GET" && request.method !== "HEAD") return;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin) return;
  if (url.pathname === "/api" || url.pathname.startsWith("/api/")) return;
  if (url.pathname === "/sw.js") return;

  if (request.mode === "navigate") {
    event.respondWith(
      (async () => {
        try {
          const response = await fetch(request);
          if (
            response.status === 200 &&
            !response.redirected &&
            response.type === "basic" &&
            (response.headers.get("content-type") ?? "").includes("text/html")
          ) {
            const cache = await caches.open(CACHE_NAME);
            await cache.put("/index.html", response.clone());
          }
          return response;
        } catch {
          const cache = await caches.open(CACHE_NAME);
          const cached = (await cache.match("/index.html")) ?? (await caches.match("/index.html"));
          if (cached) return cached;
          return new Response("Offline", {
            status: 503,
            headers: { "Content-Type": "text/plain; charset=utf-8" },
          });
        }
      })(),
    );
    return;
  }

  if (url.pathname.startsWith("/assets/")) {
    event.respondWith(
      (async () => {
        const cache = await caches.open(CACHE_NAME);
        const cached = await cache.match(url.pathname);
        if (cached) return cached;
        const response = await fetch(request);
        if (response.ok && !response.redirected && response.type === "basic") {
          await cache.put(url.pathname, response.clone());
        }
        return response;
      })(),
    );
    return;
  }

  event.respondWith(
    (async () => {
      const cache = await caches.open(CACHE_NAME);
      try {
        const response = await fetch(request);
        if (response.ok && !response.redirected && response.type === "basic") {
          await cache.put(url.pathname, response.clone());
        }
        return response;
      } catch {
        const cached = (await cache.match(url.pathname)) ?? (await caches.match(url.pathname));
        if (cached) return cached;
        return new Response("Offline", {
          status: 503,
          headers: { "Content-Type": "text/plain; charset=utf-8" },
        });
      }
    })(),
  );
});
