package app

// RouteClass drives per-route timeouts and rate-limit buckets
// (api-gateway doc: reads 1s/10rps, transfers 5s/2rps).
type RouteClass string

const (
	ClassPublic   RouteClass = "public" // auth endpoints — limited by IP
	ClassRead     RouteClass = "read"
	ClassTransfer RouteClass = "transfer"
	ClassWrite    RouteClass = "write" // non-transfer mutations
)

// Route is one row of the RBAC table — the single source of truth for
// method, path, allowed roles and class. The router registers exactly this
// table; the authz test walks it.
type Route struct {
	Method string
	Path   string
	// Roles allowed; empty = no authentication required.
	Roles []string
	Class RouteClass
	// Idempotency = the Idempotency-Key header is mandatory (ADR-0012).
	Idempotency bool
}

var (
	customerOnly = []string{"customer"}
	anyAuthed    = []string{"customer", "support", "admin"}
	staffOnly    = []string{"support", "admin"}
)

// Table is the gateway's REST surface (api-gateway doc).
func Table() []Route {
	return []Route{
		{Method: "POST", Path: "/v1/auth/register", Class: ClassPublic},
		{Method: "POST", Path: "/v1/auth/login", Class: ClassPublic},
		{Method: "POST", Path: "/v1/auth/refresh", Class: ClassPublic},
		{Method: "POST", Path: "/v1/auth/logout", Class: ClassPublic},

		{Method: "GET", Path: "/v1/customers/me", Roles: anyAuthed, Class: ClassRead},

		{Method: "POST", Path: "/v1/accounts", Roles: customerOnly, Class: ClassWrite},
		{Method: "GET", Path: "/v1/accounts", Roles: anyAuthed, Class: ClassRead},
		{Method: "GET", Path: "/v1/accounts/:id/transactions", Roles: anyAuthed, Class: ClassRead},

		{Method: "POST", Path: "/v1/topups", Roles: customerOnly, Class: ClassTransfer, Idempotency: true},
		{Method: "POST", Path: "/v1/transfers", Roles: customerOnly, Class: ClassTransfer, Idempotency: true},
		{Method: "GET", Path: "/v1/transfers/:id", Roles: anyAuthed, Class: ClassRead},
		{Method: "GET", Path: "/v1/transfers", Roles: anyAuthed, Class: ClassRead},
		{Method: "GET", Path: "/v1/rates", Roles: anyAuthed, Class: ClassRead},

		{Method: "GET", Path: "/v1/admin/customers/:id/accounts", Roles: staffOnly, Class: ClassRead},
		{Method: "POST", Path: "/v1/admin/accounts/:id/freeze", Roles: staffOnly, Class: ClassWrite},
		{Method: "POST", Path: "/v1/admin/accounts/:id/unfreeze", Roles: staffOnly, Class: ClassWrite},
	}
}

// Allowed implements the role check for a route.
func Allowed(route Route, roles []string) bool {
	if len(route.Roles) == 0 {
		return true
	}
	for _, want := range route.Roles {
		for _, have := range roles {
			if want == have {
				return true
			}
		}
	}
	return false
}
