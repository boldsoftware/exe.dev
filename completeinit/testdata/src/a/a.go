package a

//exe:completeinit
type Config struct { // want Config:"completeinit"
	Host     string
	Port     int
	Username string
	Password string
}

// NotMarked has no completeinit annotation
type NotMarked struct {
	Field1 string
	Field2 int
}

//exe:completeinit
type AnotherMarked struct { // want AnotherMarked:"completeinit"
	Name  string
	Value int
}

// unexportedConfig is an unexported type - all fields should be checked
//exe:completeinit
type unexportedConfig struct { // want unexportedConfig:"completeinit"
	host   string
	port   int
	secret string
}

func good() {
	// Complete initialization - all fields present
	_ = Config{
		Host:     "localhost",
		Port:     8080,
		Username: "admin",
		Password: "secret",
	}

	// Pointer with complete initialization
	_ = &Config{
		Host:     "localhost",
		Port:     8080,
		Username: "admin",
		Password: "secret",
	}

	// NotMarked - no annotation, so partial init is fine
	_ = NotMarked{
		Field1: "hello",
	}

	// AnotherMarked - complete
	_ = AnotherMarked{
		Name:  "test",
		Value: 42,
	}

	// unexportedConfig - complete (all unexported fields)
	_ = unexportedConfig{
		host:   "localhost",
		port:   8080,
		secret: "abc",
	}
}

func bad() {
	// Missing Password
	_ = Config{ // want "struct literal of Config is missing fields: Password"
		Host:     "localhost",
		Port:     8080,
		Username: "admin",
	}

	// Missing multiple fields
	_ = Config{ // want "struct literal of Config is missing fields: Username, Password"
		Host: "localhost",
		Port: 8080,
	}

	// Pointer with missing fields
	_ = &Config{ // want "struct literal of Config is missing fields: Port, Username, Password"
		Host: "localhost",
	}

	// Empty struct literal (missing all fields)
	_ = Config{} // want "struct literal of Config is missing fields: Host, Port, Username, Password"

	// AnotherMarked missing Value
	_ = AnotherMarked{ // want "struct literal of AnotherMarked is missing fields: Value"
		Name: "test",
	}

	// Unkeyed fields not allowed
	_ = Config{"localhost", 8080, "admin", "secret"} // want "struct literal of Config must use keyed fields"

	// unexportedConfig missing secret (unexported fields should be checked for unexported types)
	_ = unexportedConfig{ // want "struct literal of unexportedConfig is missing fields: secret"
		host: "localhost",
		port: 8080,
	}
}
