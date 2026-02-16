package b

import "a"

func useImportedType() {
	// Config is marked with completeinit in package a
	// This should be flagged because it's missing fields
	_ = a.Config{ // want "struct literal of Config is missing fields: Port, Username, Password"
		Host: "localhost",
	}

	// Complete initialization - should be fine
	_ = a.Config{
		Host:     "localhost",
		Port:     8080,
		Username: "admin",
		Password: "secret",
	}

	// NotMarked is not annotated, so partial init is fine
	_ = a.NotMarked{
		Field1: "hello",
	}

	// MixedConfig from another package — only exported fields required.
	// port is unexported, so it can't be set here and shouldn't be required.
	_ = a.MixedConfig{
		Host: "localhost",
	}

	// MixedConfig pointer from another package — only exported fields required
	_ = &a.MixedConfig{
		Host: "localhost",
	}

	// MixedConfig pointer missing exported field from another package
	_ = &a.MixedConfig{} // want "struct literal of MixedConfig is missing fields: Host"

	// MixedConfig missing exported field from another package
	_ = a.MixedConfig{} // want "struct literal of MixedConfig is missing fields: Host"
}
