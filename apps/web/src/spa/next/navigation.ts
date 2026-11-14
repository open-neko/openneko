import { useMemo } from "react";
import { useLocation, useNavigate, useParams as useRouterParams, useRevalidator } from "react-router";

export class RedirectSignal extends Error {
  constructor(readonly url: string) {
    super(`redirect ${url}`);
  }
}

export class NotFoundSignal extends Error {
  constructor() {
    super("not found");
  }
}

export function redirect(url: string): never {
  throw new RedirectSignal(url);
}

export const permanentRedirect = redirect;

export function notFound(): never {
  throw new NotFoundSignal();
}

export type AppRouter = {
  push(href: string, options?: { scroll?: boolean }): void;
  replace(href: string, options?: { scroll?: boolean }): void;
  back(): void;
  forward(): void;
  refresh(): void;
  prefetch(href: string): void;
};

export function useRouter(): AppRouter {
  const navigate = useNavigate();
  const { revalidate } = useRevalidator();
  return useMemo<AppRouter>(
    () => ({
      push: (href, options) => void navigate(href, { preventScrollReset: options?.scroll === false }),
      replace: (href, options) => void navigate(href, { replace: true, preventScrollReset: options?.scroll === false }),
      back: () => void navigate(-1),
      forward: () => void navigate(1),
      refresh: () => {
        void revalidate();
        window.dispatchEvent(new Event("openneko:refresh"));
      },
      prefetch: () => {},
    }),
    [navigate, revalidate],
  );
}

export function usePathname(): string {
  return useLocation().pathname;
}

export function useSearchParams(): URLSearchParams {
  const { search } = useLocation();
  return useMemo(() => new URLSearchParams(search), [search]);
}

export function useParams<T extends Record<string, string | string[]> = Record<string, string>>(): T {
  return useRouterParams() as unknown as T;
}

export type ReadonlyURLSearchParams = URLSearchParams;
