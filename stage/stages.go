package stage

func Local() Env {
	return Env{
		Name: "local",

		WebHost:  "localhost",
		ReplHost: "localhost",
		BoxHost:  "localhost",

		UseRoute53: false, // uses pebble/cobble without wildcard certs

		DevMode: "TODO", // should be manually overridden by caller
	}
}

func Staging() Env {
	return Env{
		Name: "staging",

		WebHost:  "exe-staging.dev",
		ReplHost: "exe-staging.dev",
		BoxHost:  "exe-staging.dev",

		UseRoute53: true,

		DevMode: "",
	}
}

func Prod() Env {
	return Env{
		Name: "prod",

		WebHost:  "exe.dev",
		ReplHost: "exe.dev",
		BoxHost:  "exe.dev",

		UseRoute53: true,

		DevMode: "",
	}
}
