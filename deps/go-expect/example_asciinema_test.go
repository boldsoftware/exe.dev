// Copyright 2018 Netflix, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package expect

import (
	"fmt"
	"io/ioutil"
	"os"
)

// ExampleConsole_asciinemaRecording demonstrates basic ASCIIcinema recording
func ExampleConsole_asciinemaRecording() {
	// Create a temporary file for recording
	file, err := ioutil.TempFile("", "example_*.cast")
	if err != nil {
		panic(err)
	}
	defer os.Remove(file.Name())
	defer file.Close()

	// Create console with ASCIIcinema recording
	c, err := NewConsole(WithAsciinemaRecording(file.Name()))
	if err != nil {
		panic(err)
	}
	defer c.Close()

	// Send some commands
	c.Send("echo 'Hello, ASCIIcinema!'")
	c.SendLine("")
	c.Send("ls -la")
	c.SendLine("")

	// Stop recording
	c.StopRecording()

	fmt.Println("Recording completed")
	// Output: Recording completed
}

// ExampleConsole_programmaticRecording demonstrates starting and stopping recording programmatically
func ExampleConsole_programmaticRecording() {
	// Create console without initial recording
	c, err := NewConsole()
	if err != nil {
		panic(err)
	}
	defer c.Close()

	// Create temporary file
	file, err := ioutil.TempFile("", "programmatic_*.cast")
	if err != nil {
		panic(err)
	}
	defer os.Remove(file.Name())
	defer file.Close()

	fmt.Printf("Recording active: %v\n", c.IsRecording())

	// Start recording
	err = c.StartRecording(file.Name())
	if err != nil {
		panic(err)
	}

	fmt.Printf("Recording active: %v\n", c.IsRecording())

	// Send some data
	c.Send("date")
	c.SendLine("")

	// Stop recording
	err = c.StopRecording()
	if err != nil {
		panic(err)
	}

	fmt.Printf("Recording active: %v\n", c.IsRecording())

	// Output:
	// Recording active: false
	// Recording active: true
	// Recording active: false
}