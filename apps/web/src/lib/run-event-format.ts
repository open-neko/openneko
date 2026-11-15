export const RUN_EVENT_FORMAT = "events=2";

export function withRunEventFormat(url: string): string {
  return `${url}${url.includes("?") ? "&" : "?"}${RUN_EVENT_FORMAT}`;
}
