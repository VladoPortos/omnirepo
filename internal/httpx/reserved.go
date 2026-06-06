package httpx

// ReservedPrefixes are the top-level URL segments reserved by OmniRepo protocol
// handlers and system endpoints. Project names MUST NOT collide with these
// values — auth.ProjectNameValid rejects them at creation time and the main
// router mounts reserved paths itself via chi.Mount.
var ReservedPrefixes = [...]string{"v2", "s3", "git", "api", "ui", "assets", "static", "login", "logout", "healthz", "readyz"}

// IsReserved reports whether name is one of the reserved top-level prefixes.
// Match is case-sensitive and exact (no substring or prefix matching).
func IsReserved(name string) bool {
	for _, p := range ReservedPrefixes {
		if p == name {
			return true
		}
	}
	return false
}
