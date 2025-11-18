package stage

func Local() Env {
	return Env{
		WebHost:  "localhost",
		ReplHost: "localhost",
		BoxHost:  "localhost",

		DevMode: "TODO", // should be manually overridden by caller
	}
}

func Staging() Env {
	return Env{
		WebHost:  "exe-staging.dev",
		ReplHost: "exe-staging.dev",
		BoxHost:  "exe-staging.dev",

		DevMode: "",
	}
}

func Prod() Env {
	return Env{
		WebHost:  "exe.dev",
		ReplHost: "exe.dev",
		BoxHost:  "exe.dev",

		DevMode: "",
	}
}
