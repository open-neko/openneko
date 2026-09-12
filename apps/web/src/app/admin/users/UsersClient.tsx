"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { ActionGroup } from "@/components/ui/action-group";
import { Button } from "@/components/ui/button";
import { Field, Input, NativeSelect } from "@/components/ui/field";
import { Badge } from "@/components/ui/badge";
import { SearchInput } from "@/components/ui/search-input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { matchesListSearch } from "@/lib/list-search";

export interface AdminUserRow {
  id: string;
  email: string;
  name: string | null;
  role: string;
  disabled: boolean;
  hasSignedIn: boolean;
  lastLoginAt: string | null;
  createdAt: string | null;
}

/**
 * Interactive user administration: provision users ahead of first
 * sign-in (required for manual-provisioning auth like magic link),
 * change roles, and disable/enable accounts. The API enforces the
 * last-active-admin guard; errors from it surface inline.
 */
export function UsersClient({ users, identitySetup = false }: { users: AdminUserRow[]; identitySetup?: boolean }) {
  const router = useRouter();
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [role, setRole] = useState<"member" | "admin">(identitySetup ? "admin" : "member");
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const visibleUsers = users.filter((user) =>
    matchesListSearch(
      query,
      user.email,
      user.name,
      user.role,
      user.disabled ? "disabled" : "active",
    ),
  );

  async function callApi(path: string, init: RequestInit): Promise<boolean> {
    setError(null);
    try {
      const res = await fetch(path, {
        headers: { "Content-Type": "application/json" },
        ...init,
      });
      if (!res.ok) {
        const body = (await res.json().catch(() => null)) as {
          error?: string;
        } | null;
        setError(body?.error ?? `request failed (${res.status})`);
        return false;
      }
      router.refresh();
      return true;
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      return false;
    }
  }

  async function createUser(event: React.FormEvent) {
    event.preventDefault();
    setBusy("create");
    const ok = await callApi("/api/admin/users", {
      method: "POST",
      body: JSON.stringify({
        email,
        name: name.trim().length > 0 ? name : undefined,
        role,
        ...(identitySetup ? { updateSoloAccount: true } : {}),
      }),
    });
    if (ok) {
      setEmail("");
      setName("");
      setRole("member");
    }
    setBusy(null);
  }

  async function patchUser(
    id: string,
    patch: { role?: string; disabled?: boolean },
  ) {
    setBusy(id);
    await callApi(`/api/admin/users/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
    });
    setBusy(null);
  }

  return (
    <>
      {error ? (
        <div
          className="mb-4 rounded-control bg-danger-soft px-3 py-2 text-sm text-danger"
          role="alert"
        >
          {error}
        </div>
      ) : null}

      {!identitySetup && <div className="mb-4 max-w-[520px]">
        <SearchInput
          label="Search users"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Search users by name, email, role, or status"
        />
      </div>}

      {identitySetup && <p className="mb-4 text-sm text-text2">Use the email you will sign in with if you enable SSO later.</p>}
      <form
        onSubmit={createUser}
        className="mb-6 grid grid-cols-[minmax(220px,1.4fr)_minmax(180px,1fr)_minmax(130px,0.6fr)_auto] items-end gap-3 max-[820px]:grid-cols-2 max-[520px]:grid-cols-1"
      >
        <Field label="Email" htmlFor="new-user-email">
          <Input
            id="new-user-email"
            name="email"
            autoComplete="email"
            spellCheck={false}
            type="email"
            required
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            placeholder="person@company.com"
          />
        </Field>
        <Field label="Name (optional)" htmlFor="new-user-name">
          <Input
            id="new-user-name"
            name="name"
            autoComplete="name"
            type="text"
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="Display name"
          />
        </Field>
        <Field label="Role" htmlFor="new-user-role">
          <NativeSelect
            id="new-user-role"
            disabled={identitySetup}
            value={role}
            onChange={(event) =>
              setRole(event.target.value === "admin" ? "admin" : "member")
            }
          >
            <option value="member">member</option>
            <option value="admin">admin</option>
          </NativeSelect>
        </Field>
        <Button type="submit" variant="primary" disabled={busy === "create"}>
          {busy === "create" ? "Saving…" : identitySetup ? "Save email" : "Add user"}
        </Button>
      </form>

      {visibleUsers.length === 0 ? (
        <p className="text-sm text-text2">
          {query
            ? "No users match this search."
            : identitySetup ? "Your account already exists. Adding an email is optional until you enable SSO." : "No users yet. Add one above to allow them to sign in."}
        </p>
      ) : (
        <div className="overflow-x-auto">
          <Table className="w-full border-collapse text-left text-sm">
            <TableHeader className="text-ui-label uppercase tracking-[0.12em] text-text3">
              <TableRow>
                <TableHead className="border-b border-border px-3 py-2 font-bold">
                  User
                </TableHead>
                <TableHead className="border-b border-border px-3 py-2 font-bold">
                  Role
                </TableHead>
                <TableHead className="border-b border-border px-3 py-2 font-bold">
                  Status
                </TableHead>
                <TableHead className="border-b border-border px-3 py-2 font-bold">
                  Last login
                </TableHead>
                <TableHead className="border-b border-border px-3 py-2 font-bold">
                  Created
                </TableHead>
                <TableHead className="border-b border-border px-3 py-2 font-bold">
                  Actions
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {visibleUsers.map((user) => (
                <TableRow
                  key={user.id}
                  className="border-b border-border last:border-0"
                >
                  <TableCell className="px-3 py-3">
                    <div className="font-semibold text-text">{user.email}</div>
                    <div className="text-xs text-text3">
                      {user.name ?? user.id}
                      {user.hasSignedIn ? null : " · never signed in"}
                    </div>
                  </TableCell>
                  <TableCell className="px-3 py-3">
                    <RoleBadge role={user.role} />
                  </TableCell>
                  <TableCell className="px-3 py-3">
                    <StatusBadge disabled={user.disabled} />
                  </TableCell>
                  <TableCell className="px-3 py-3 text-text2">
                    {formatDate(user.lastLoginAt)}
                  </TableCell>
                  <TableCell className="px-3 py-3 text-text2">
                    {formatDate(user.createdAt)}
                  </TableCell>
                  <TableCell className="px-3 py-3">
                    <ActionGroup align="start" className="flex-nowrap">
                      <Button
                        size="sm"
                        disabled={busy === user.id}
                        onClick={() =>
                          patchUser(user.id, {
                            role: user.role === "admin" ? "member" : "admin",
                          })
                        }
                      >
                        {user.role === "admin" ? "Make member" : "Make admin"}
                      </Button>
                      <Button
                        variant={user.disabled ? "primary" : "danger"}
                        size="sm"
                        disabled={busy === user.id}
                        onClick={() =>
                          patchUser(user.id, { disabled: !user.disabled })
                        }
                      >
                        {user.disabled ? "Enable" : "Disable"}
                      </Button>
                    </ActionGroup>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </>
  );
}

function RoleBadge({ role }: { role: string }) {
  const isAdmin = role === "admin";
  return <Badge variant={isAdmin ? "success" : "muted"}>{role}</Badge>;
}

function StatusBadge({ disabled }: { disabled: boolean }) {
  return (
    <Badge variant={disabled ? "danger" : "success"}>
      {disabled ? "Disabled" : "Active"}
    </Badge>
  );
}

function formatDate(value: string | null): string {
  if (!value) return "Never";
  return new Date(value).toLocaleString("en-US", {
    year: "numeric",
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}
