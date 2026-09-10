import type { AppRegistrySnapshot } from "@neko/records";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

function Grant({ allowed }: { allowed: boolean }) {
  return (
    <span
      className={`records-permission-grant ${allowed ? "is-allowed" : "is-denied"}`}
    >
      {allowed ? "Allowed" : "Denied"}
    </span>
  );
}

export function RecordPermissionsPanel({
  objects,
  permissions,
}: {
  objects: AppRegistrySnapshot["objects"];
  permissions: AppRegistrySnapshot["permissions"];
}) {
  return (
    <section className="records-admin-panel">
      <header>
        <div>
          <h2>Object permissions</h2>
          <p>
            These grants govern generated forms and agent record actions through
            the same policy snapshot.
          </p>
        </div>
      </header>
      <div className="records-admin-table-scroll">
        <Table className="records-admin-table">
          <TableHeader>
            <TableRow>
              <TableHead>Role</TableHead>
              <TableHead>Object</TableHead>
              <TableHead>Read</TableHead>
              <TableHead>Create</TableHead>
              <TableHead>Update</TableHead>
              <TableHead>Delete</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {permissions.map((permission) => {
              const object = objects.find(
                (candidate) => candidate.apiName === permission.objectApiName,
              );
              if (!object) return null;
              return (
                <TableRow
                  key={`${permission.role}-${permission.objectApiName}`}
                >
                  <TableCell>
                    <strong>{permission.role}</strong>
                  </TableCell>
                  <TableCell>
                    {object.label}
                    <small>{object.apiName}</small>
                  </TableCell>
                  <TableCell>
                    <Grant allowed={permission.canRead} />
                  </TableCell>
                  <TableCell>
                    <Grant allowed={permission.canCreate} />
                  </TableCell>
                  <TableCell>
                    <Grant allowed={permission.canUpdate} />
                  </TableCell>
                  <TableCell>
                    <Grant allowed={permission.canDelete} />
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>
      <footer>
        Permission changes are schema actions and require an approved{" "}
        <code>app_permission_set</code> request.
      </footer>
    </section>
  );
}
