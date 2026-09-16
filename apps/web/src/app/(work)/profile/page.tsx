import { connection } from "next/server";
import PageHeading from "@/components/PageHeading";
import { getCurrentActor } from "@/lib/actor";
import { getAuthProvider, getCurrentUser } from "@/lib/auth";
import { demoSafeEmail } from "@/lib/demo-mode";
import { ProfileClient } from "./ProfileClient";

export default async function ProfilePage() {
  await connection();
  const [user, actor, provider] = await Promise.all([
    getCurrentUser(),
    getCurrentActor(),
    getAuthProvider(),
  ]);
  return (
    <div className="library-page">
      <PageHeading
        title="Your account"
        description={actor.role === "admin" ? "Your persona and sign-in. Administration lives under Admin." : "Your persona and sign-in."}
      />
      <main className="library-main">
        <ProfileClient email={demoSafeEmail(user?.email ?? "", user?.id ?? "")} signInEnabled={Boolean(provider)} />
      </main>
    </div>
  );
}
