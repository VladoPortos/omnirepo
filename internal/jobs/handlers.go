package jobs

// This file exists as the named home for the Handlers type so downstream
// plans (02-09 scan handler, 02-10 pull-external, 02-12 GC) can import
// and construct handler maps without pulling the full pool.go surface
// into their mental model. The Handlers type itself is declared in
// pool.go; this file is intentionally a lightweight anchor.
//
// Downstream plans populate Handlers like:
//
//   syncHandlers := jobs.Handlers{
//       "pull_external": pullExternal.Handle,
//       "promote":       promote.Handle,
//       "gc":            gc.Handle,
//   }
//
// Handlers is just map[string]Handler; no constructor needed.
