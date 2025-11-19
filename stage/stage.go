// Package stage organizes different staging environments: prod, staging, local, test, etc.
package stage

// An Env represents a deployment stage/environment.
type Env struct {
	Name string // the name of the stage (for logging/debugging): "prod", "staging", "local", "test", etc.

	WebHost  string // the base hostname of the website; prod is "exe.dev"
	ReplHost string // the base hostname of the repl; prod is "exe.dev"
	BoxHost  string // the base hostname of boxes; prod is "exe.dev" (but soon will be "exe.xyz")

	UseRoute53 bool // whether to use Route53 for DNS management and LetsEncrypt DNS challenges
	UseCobble  bool // whether to start cobble/pebble for local ACME testing

	ReplDev bool // whether to expose dev-only repl features (printing internal errors, showing hidden commands)

	DevMode string // dev mode: "local", "test", or ""; TODO: delete in favor of more precise flags
}

func (e Env) String() string {
	if e.DevMode != "" {
		return e.Name + "(" + e.DevMode + ")"
	}
	return e.Name
}
