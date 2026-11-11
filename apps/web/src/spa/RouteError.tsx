import { Navigate, isRouteErrorResponse, useRouteError } from "react-router";
import PageHeading from "@/components/PageHeading";
import { ButtonLink } from "@/components/ui/button";
import { NotFoundSignal, RedirectSignal } from "./next/navigation";

function Message({ title, description }: { title: string; description: string }) {
  return (
    <div className="library-page">
      <main className="library-main">
        <PageHeading title={title} description={description} actions={<ButtonLink href="/chat" variant="secondary">Go to Chat</ButtonLink>} />
      </main>
    </div>
  );
}

export function NotFound() {
  return <Message title="Page not found" description="This address has no page." />;
}

export default function RouteError() {
  const error = useRouteError();
  if (error instanceof RedirectSignal) return <Navigate to={error.url} replace />;
  if (error instanceof NotFoundSignal || (isRouteErrorResponse(error) && error.status === 404)) return <NotFound />;
  return <Message title="Something went wrong" description="Reload the page to try again." />;
}
