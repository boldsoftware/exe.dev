// Package stage organizes different staging environments: prod, staging, local, test, etc.
package stage

// An Env represents a deployment stage/environment.
type Env struct {
	WebHost  string // the base hostname of the website; prod is "exe.dev"
	ReplHost string // the base hostname of the repl; prod is "exe.dev"
	BoxHost  string // the base hostname of boxes; prod is "exe.dev" (but soon will be "exe.xyz")

	DevMode string // dev mode: "local", "test", or ""; TODO: delete in favor of more precise flags
}
