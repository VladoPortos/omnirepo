/**
 * Go module proxy (GOPROXY) path helpers.
 *
 * The GOPROXY wire protocol cannot carry uppercase letters in module
 * paths (case-insensitive filesystems would collide e.g. github.com/Azure
 * and github.com/azure), so every uppercase letter is escaped as
 * "!" + lowercase letter:
 *
 *   github.com/Azure/Thing → github.com/!azure/!thing
 *
 * Versions are lowercase in practice, but the same rule applies to them
 * per the protocol spec, so callers escape both. Used by the row-delete
 * URL builder (useDeleteGoModuleVersion) and the per-row download links
 * on GoRepoPage.
 */

export function escapeGoModulePath(path: string): string {
  return path.replace(/[A-Z]/g, (c) => `!${c.toLowerCase()}`);
}
