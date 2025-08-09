package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// This is a standalone program to debug terminal output issues
func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run debug_terminal.go [start-server|connect]")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "start-server":
		startServer()
	case "connect":
		connectToSSH()
	case "test-output":
		testTerminalOutput()
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func startServer() {
	fmt.Println("Starting SSH server on :12347...")
	cmd := exec.Command("./exed", "-http=:18100", "-ssh=:12347")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	
	err := cmd.Run()
	if err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func connectToSSH() {
	fmt.Println("Connecting to SSH server and capturing terminal behavior...")
	
	// Use expect-like behavior to interact with SSH
	cmd := exec.Command("ssh", 
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-p", "12347",
		"localhost")
	
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	
	fmt.Println("Starting interactive SSH session...")
	fmt.Println("Watch carefully for offset issues in the output!")
	fmt.Println("Type 'test@example.com' when prompted for email")
	fmt.Println("Use Ctrl+C to exit")
	fmt.Println("")
	
	err := cmd.Run()
	if err != nil {
		log.Printf("SSH exited with: %v", err)
	}
}

func testTerminalOutput() {
	fmt.Println("=== Terminal Output Test ===")
	fmt.Println("This test shows how different line endings appear in your terminal")
	fmt.Println("")
	
	// Test different line ending scenarios
	scenarios := []struct{
		name string
		output string
	}{
		{"Standard lines", "Line 1\nLine 2\nLine 3\n"},
		{"Windows style", "Line 1\r\nLine 2\r\nLine 3\r\n"}, 
		{"Mixed style", "Line 1\nLine 2\r\nLine 3\n"},
		{"Prompt without newline + input", "Email: "},
		{"User input simulation", "user@example.com"},
		{"Response with CRLF", "\r\nGot: user@example.com\r\n"},
		{"Carriage return overwrite", "Loading...\rDone!      \n"},
	}
	
	for i, scenario := range scenarios {
		fmt.Printf("--- Scenario %d: %s ---\n", i+1, scenario.name)
		fmt.Print("Output: ")
		
		// Show each character as we write it
		for _, char := range scenario.output {
			fmt.Printf("%c", char)
			time.Sleep(50 * time.Millisecond) // Slow output to see behavior
		}
		
		if !strings.HasSuffix(scenario.output, "\n") {
			fmt.Print(" [NO NEWLINE]")
		}
		
		fmt.Println("")
		fmt.Printf("Raw bytes: %q\n", scenario.output)
		fmt.Println("")
		
		// Pause between scenarios
		fmt.Print("Press Enter to continue...")
		bufio.NewReader(os.Stdin).ReadLine()
		fmt.Println("")
	}
	
	fmt.Println("=== Test Complete ===")
	fmt.Println("Did you notice any offset issues in the output above?")
}