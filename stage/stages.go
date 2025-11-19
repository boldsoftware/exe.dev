package stage

func Local() Env {
	return Env{
		Name: "local",

		WebHost:  "localhost",
		ReplHost: "localhost",
		BoxHost:  "localhost",

		// auto-start cobble/pebble for ACME testing
		UseRoute53: false,
		UseCobble:  true,

		ReplDev: true,

		DevMode: "local",
	}
}

func Test() Env {
	return Env{
		Name: "test",

		WebHost:  "localhost",
		ReplHost: "localhost",
		BoxHost:  "localhost",

		// tests start their own cobble/pebble instances as needed
		UseRoute53: false,
		UseCobble:  false,

		ReplDev: false,

		DevMode: "test",
	}
}

func Staging() Env {
	return Env{
		Name: "staging",

		WebHost:  "exe-staging.dev",
		ReplHost: "exe-staging.dev",
		BoxHost:  "exe-staging.dev",

		UseRoute53: true,
		UseCobble:  false,

		ReplDev: false,

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
		UseCobble:  false,

		ReplDev: false,

		DevMode: "",
	}
}
