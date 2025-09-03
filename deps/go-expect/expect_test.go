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
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"
)

var ErrWrongAnswer = errors.New("wrong answer")

type Survey struct {
	Prompt string
	Answer string
}

func Prompt(in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)

	for _, survey := range []Survey{
		{
			"What is 1+1?", "2",
		},
		{
			"What is Netflix backwards?", "xilfteN",
		},
	} {
		fmt.Fprint(out, fmt.Sprintf("%s: ", survey.Prompt))
		text, err := reader.ReadString('\n')
		if err != nil {
			return err
		}

		fmt.Fprint(out, text)
		text = strings.TrimSpace(text)
		if text != survey.Answer {
			return ErrWrongAnswer
		}
	}

	return nil
}

func newTestConsole(t *testing.T, opts ...ConsoleOpt) (*Console, error) {
	opts = append([]ConsoleOpt{
		expectNoError(t),
		sendNoError(t),
		WithDefaultTimeout(time.Second),
	}, opts...)
	return NewTestConsole(t, opts...)
}

func expectNoError(t *testing.T) ConsoleOpt {
	return WithExpectObserver(
		func(matchers []Matcher, buf string, err error) {
			if err == nil {
				return
			}
			if len(matchers) == 0 {
				t.Fatalf("Error occurred while matching %q: %s\n%s", buf, err, string(debug.Stack()))
			} else {
				var criteria []string
				for _, matcher := range matchers {
					criteria = append(criteria, fmt.Sprintf("%q", matcher.Criteria()))
				}
				t.Fatalf("Failed to find [%s] in %q: %s\n%s", strings.Join(criteria, ", "), buf, err, string(debug.Stack()))
			}
		},
	)
}

func sendNoError(t *testing.T) ConsoleOpt {
	return WithSendObserver(
		func(msg string, n int, err error) {
			if err != nil {
				t.Fatalf("Failed to send %q: %s\n%s", msg, err, string(debug.Stack()))
			}
			if len(msg) != n {
				t.Fatalf("Only sent %d of %d bytes for %q\n%s", n, len(msg), msg, string(debug.Stack()))
			}
		},
	)
}

func testCloser(t *testing.T, closer io.Closer) {
	if err := closer.Close(); err != nil {
		t.Errorf("Close failed: %s", err)
		debug.PrintStack()
	}
}

func TestExpectf(t *testing.T) {
	t.Parallel()

	c, err := newTestConsole(t)
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.Expectf("What is 1+%d?", 1)
		c.SendLine("2")
		c.Expectf("What is %s backwards?", "Netflix")
		c.SendLine("xilfteN")
		c.ExpectEOF()
	}()

	err = Prompt(c.Tty(), c.Tty())
	if err != nil {
		t.Errorf("Expected no error but got '%s'", err)
	}
	testCloser(t, c.Tty())
	wg.Wait()
}

func TestExpect(t *testing.T) {
	t.Parallel()

	c, err := newTestConsole(t)
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.ExpectString("What is 1+1?")
		c.SendLine("2")
		c.ExpectString("What is Netflix backwards?")
		c.SendLine("xilfteN")
		c.ExpectEOF()
	}()

	err = Prompt(c.Tty(), c.Tty())
	if err != nil {
		t.Errorf("Expected no error but got '%s'", err)
	}
	// close the pts so we can expect EOF
	testCloser(t, c.Tty())
	wg.Wait()
}

func TestExpectOutput(t *testing.T) {
	t.Parallel()

	c, err := newTestConsole(t)
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.ExpectString("What is 1+1?")
		c.SendLine("3")
		c.ExpectEOF()
	}()

	err = Prompt(c.Tty(), c.Tty())
	if err == nil || err != ErrWrongAnswer {
		t.Errorf("Expected error '%s' but got '%s' instead", ErrWrongAnswer, err)
	}
	testCloser(t, c.Tty())
	wg.Wait()
}

func TestExpectDefaultTimeout(t *testing.T) {
	t.Parallel()

	c, err := NewTestConsole(t, WithDefaultTimeout(0))
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		Prompt(c.Tty(), c.Tty())
	}()

	_, err = c.ExpectString("What is 1+2?")
	if err == nil || !strings.Contains(err.Error(), "i/o timeout") {
		t.Errorf("Expected error to contain 'i/o timeout' but got '%s' instead", err)
	}

	// Close to unblock Prompt and wait for the goroutine to exit.
	c.Tty().Close()
	wg.Wait()
}

func TestExpectTimeout(t *testing.T) {
	t.Parallel()

	c, err := NewTestConsole(t)
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		Prompt(c.Tty(), c.Tty())
	}()

	_, err = c.Expect(String("What is 1+2?"), WithTimeout(0))
	if err == nil || !strings.Contains(err.Error(), "i/o timeout") {
		t.Errorf("Expected error to contain 'i/o timeout' but got '%s' instead", err)
	}

	// Close to unblock Prompt and wait for the goroutine to exit.
	c.Tty().Close()
	wg.Wait()
}

func TestExpectDefaultTimeoutOverride(t *testing.T) {
	t.Parallel()

	c, err := newTestConsole(t, WithDefaultTimeout(100*time.Millisecond))
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err = Prompt(c.Tty(), c.Tty())
		if err != nil {
			t.Errorf("Expected no error but got '%s'", err)
		}
		time.Sleep(200 * time.Millisecond)
		c.Tty().Close()
	}()

	c.ExpectString("What is 1+1?")
	c.SendLine("2")
	c.ExpectString("What is Netflix backwards?")
	c.SendLine("xilfteN")
	c.Expect(EOF, PTSClosed, WithTimeout(time.Second))

	wg.Wait()
}

func TestConsoleChain(t *testing.T) {
	t.Parallel()

	c1, err := NewConsole(expectNoError(t), sendNoError(t))
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c1)

	var wg1 sync.WaitGroup
	wg1.Add(1)
	go func() {
		defer wg1.Done()
		c1.ExpectString("What is Netflix backwards?")
		c1.SendLine("xilfteN")
		c1.ExpectEOF()
	}()

	c2, err := newTestConsole(t, WithStdin(c1.Tty()), WithStdout(c1.Tty()))
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c2)

	var wg2 sync.WaitGroup
	wg2.Add(1)
	go func() {
		defer wg2.Done()
		c2.ExpectString("What is 1+1?")
		c2.SendLine("2")
		c2.ExpectEOF()
	}()

	err = Prompt(c2.Tty(), c2.Tty())
	if err != nil {
		t.Errorf("Expected no error but got '%s'", err)
	}

	testCloser(t, c2.Tty())
	wg2.Wait()

	testCloser(t, c1.Tty())
	wg1.Wait()
}

func TestEditor(t *testing.T) {
	if _, err := exec.LookPath("vi"); err != nil {
		t.Skip("vi not found in PATH")
	}
	t.Parallel()

	c, err := NewConsole(expectNoError(t), sendNoError(t))
	if err != nil {
		t.Errorf("Expected no error but got '%s'", err)
	}
	defer testCloser(t, c)

	file, err := ioutil.TempFile("", "")
	if err != nil {
		t.Errorf("Expected no error but got '%s'", err)
	}

	cmd := exec.Command("vi", file.Name())
	cmd.Stdin = c.Tty()
	cmd.Stdout = c.Tty()
	cmd.Stderr = c.Tty()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.Send("iHello world\x1b")
		c.SendLine(":wq!")
		c.ExpectEOF()
	}()

	err = cmd.Run()
	if err != nil {
		t.Errorf("Expected no error but got '%s'", err)
	}

	testCloser(t, c.Tty())
	wg.Wait()

	data, err := ioutil.ReadFile(file.Name())
	if err != nil {
		t.Errorf("Expected no error but got '%s'", err)
	}
	if string(data) != "Hello world\n" {
		t.Errorf("Expected '%s' to equal '%s'", string(data), "Hello world\n")
	}
}

func TestExpectRefreshingTimeout(t *testing.T) {
	t.Parallel()

	c, err := NewTestConsole(t)
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(50 * time.Millisecond)
		c.Send(".")
		time.Sleep(50 * time.Millisecond)
		c.Send(".")
		time.Sleep(50 * time.Millisecond)
		c.Send(".")
		time.Sleep(50 * time.Millisecond)
		c.SendLine("done")
		time.Sleep(10 * time.Millisecond)
		c.Tty().Close()
	}()

	start := time.Now()
	_, err = c.Expect(String("done"), WithRefreshingTimeout(80*time.Millisecond))
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("Expected no error but got '%s'", err)
	}

	if elapsed < 200*time.Millisecond {
		t.Errorf("Expected refreshing timeout to extend beyond 200ms, but completed in %v", elapsed)
	}

	wg.Wait()
}

func TestExpectRefreshingTimeoutExpires(t *testing.T) {
	t.Parallel()

	c, err := NewTestConsole(t)
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(50 * time.Millisecond)
		c.Send(".")
		time.Sleep(200 * time.Millisecond)
	}()

	start := time.Now()
	_, err = c.Expect(String("never_comes"), WithRefreshingTimeout(100*time.Millisecond))
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "i/o timeout") {
		t.Errorf("Expected timeout error but got '%s'", err)
	}

	if elapsed < 150*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Errorf("Expected timeout around 150ms (50ms + 100ms), but got %v", elapsed)
	}

	c.Tty().Close()
	wg.Wait()
}

func TestExpectRefreshingTimeoutWithSpinner(t *testing.T) {
	t.Parallel()

	c, err := NewTestConsole(t)
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		spinner := []string{"|", "/", "-", "\\"}
		for i := 0; i < 10; i++ {
			c.Send("\r" + spinner[i%4] + " Processing...")
			time.Sleep(75 * time.Millisecond)
		}
		c.SendLine("\r✓ Complete!")
		time.Sleep(10 * time.Millisecond)
		c.Tty().Close()
	}()

	start := time.Now()
	_, err = c.Expect(String("Complete!"), WithRefreshingTimeout(150*time.Millisecond))
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("Expected no error but got '%s'", err)
	}

	if elapsed < 750*time.Millisecond {
		t.Errorf("Expected to wait for full spinner cycle (~750ms), but completed in %v", elapsed)
	}

	wg.Wait()
}

func TestExpectRefreshingTimeoutOverridesDefault(t *testing.T) {
	t.Parallel()

	c, err := newTestConsole(t, WithDefaultTimeout(50*time.Millisecond))
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(30 * time.Millisecond)
		c.Send(".")
		time.Sleep(30 * time.Millisecond)
		c.Send(".")
		time.Sleep(30 * time.Millisecond)
		c.SendLine("done")
		time.Sleep(10 * time.Millisecond)
		c.Tty().Close()
	}()

	start := time.Now()
	_, err = c.Expect(String("done"), WithRefreshingTimeout(100*time.Millisecond))
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("Expected no error but got '%s'", err)
	}

	if elapsed < 90*time.Millisecond {
		t.Errorf("Expected to wait at least 90ms with refreshing timeout, but completed in %v", elapsed)
	}

	wg.Wait()
}

func TestExpectDefaultRefreshingTimeout(t *testing.T) {
	t.Parallel()

	c, err := NewTestConsole(t, WithDefaultRefreshingTimeout(100*time.Millisecond))
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(50 * time.Millisecond)
		c.Send(".")
		time.Sleep(50 * time.Millisecond)
		c.Send(".")
		time.Sleep(200 * time.Millisecond)
		c.Tty().Close()
	}()

	start := time.Now()
	_, err = c.Expect(String("never_comes"))
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "i/o timeout") {
		t.Errorf("Expected timeout error but got '%s'", err)
	}

	if elapsed < 200*time.Millisecond || elapsed > 350*time.Millisecond {
		t.Errorf("Expected timeout around 300ms (100ms + 100ms + 100ms), but got %v", elapsed)
	}

	c.Tty().Close()
	wg.Wait()
}

func TestExpectDefaultRefreshingTimeoutWithSpinner(t *testing.T) {
	t.Parallel()

	c, err := NewTestConsole(t, WithDefaultRefreshingTimeout(150*time.Millisecond))
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		spinner := []string{"|", "/", "-", "\\"}
		for i := 0; i < 10; i++ {
			c.Send("\r" + spinner[i%4] + " Processing...")
			time.Sleep(75 * time.Millisecond)
		}
		c.SendLine("\r✓ Complete!")
		time.Sleep(10 * time.Millisecond)
		c.Tty().Close()
	}()

	start := time.Now()
	_, err = c.Expect(String("Complete!"))
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("Expected no error but got '%s'", err)
	}

	if elapsed < 750*time.Millisecond {
		t.Errorf("Expected to wait for full spinner cycle (~750ms), but completed in %v", elapsed)
	}

	wg.Wait()
}

func TestExpectDefaultRefreshingTimeoutOverrideByPerCall(t *testing.T) {
	t.Parallel()

	c, err := NewTestConsole(t, WithDefaultRefreshingTimeout(200*time.Millisecond))
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(50 * time.Millisecond)
		c.Send(".")
		time.Sleep(200 * time.Millisecond)
		c.Tty().Close()
	}()

	start := time.Now()
	_, err = c.Expect(String("never_comes"), WithRefreshingTimeout(75*time.Millisecond))
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "i/o timeout") {
		t.Errorf("Expected timeout error but got '%s'", err)
	}

	if elapsed < 120*time.Millisecond || elapsed > 150*time.Millisecond {
		t.Errorf("Expected timeout around 125ms (50ms + 75ms), but got %v", elapsed)
	}

	c.Tty().Close()
	wg.Wait()
}

func TestRejectStringPersistent(t *testing.T) {
	t.Parallel()

	c, err := NewTestConsole(t)
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	// Set up rejection rule
	c.RejectString("error")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.Send("This contains an error message!")
		time.Sleep(10 * time.Millisecond)
		c.Tty().Close()
	}()

	// The subsequent expect should fail due to persistent rejection
	_, err = c.ExpectEOF()
	if err == nil || !strings.Contains(err.Error(), "rejected string \"error\" found in output") {
		t.Errorf("Expected rejection error but got '%s'", err)
	}

	wg.Wait()
}

func TestRejectStringClearedOnSuccess(t *testing.T) {
	t.Parallel()

	c, err := NewTestConsole(t)
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	// Set up rejection rule
	c.RejectString("error")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.Send("success: operation completed")
		time.Sleep(10 * time.Millisecond)
		c.Send("error occurred after success")
		time.Sleep(10 * time.Millisecond)
		c.Tty().Close()
	}()

	// First expect should succeed and clear rejection rules
	_, err = c.ExpectString("success")
	if err != nil {
		t.Errorf("First expect should succeed but got '%s'", err)
	}

	// Second expect should succeed even though "error" appears (rules cleared)
	_, err = c.ExpectEOF()
	if err != nil {
		t.Errorf("Second expect should succeed (rejection cleared) but got '%s'", err)
	}

	wg.Wait()
}

func TestRejectStringFailsFast(t *testing.T) {
	t.Parallel()

	c, err := NewTestConsole(t)
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	// Set up rejection rule
	c.RejectString("error")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.Send("error occurred")
		time.Sleep(10 * time.Millisecond)
		c.Send("more output that should not be processed")
		time.Sleep(10 * time.Millisecond)
		c.Tty().Close()
	}()

	start := time.Now()
	_, err = c.ExpectString("never_appears")
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "rejected string \"error\" found in output") {
		t.Errorf("Expected rejection error but got '%s'", err)
	}

	// Should fail quickly, not wait for timeout
	if elapsed > 100*time.Millisecond {
		t.Errorf("Expected fast failure but took %v", elapsed)
	}

	wg.Wait()
}

func TestRejectStringMultiple(t *testing.T) {
	t.Parallel()

	c, err := NewTestConsole(t)
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	// Set up multiple rejection rules
	c.RejectString("error")
	c.RejectString("failure")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.Send("Processing... failure occurred")
		time.Sleep(10 * time.Millisecond)
		c.Tty().Close()
	}()

	_, err = c.ExpectString("success")
	if err == nil || !strings.Contains(err.Error(), "rejected string \"failure\" found in output") {
		t.Errorf("Expected rejection error for 'failure' but got '%s'", err)
	}

	wg.Wait()
}

func TestRejectStringUsagePattern(t *testing.T) {
	t.Parallel()

	c, err := NewTestConsole(t)
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	// This mimics the exact pattern: pty.reject("Sorry"); pty.wantRe("Creating .*")
	c.RejectString("Sorry")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.Send("Creating e1e-1756860627-testboxcreation (medium) using image boldsoftware/exeuntu...")
		time.Sleep(10 * time.Millisecond)
		c.Send("Ready in 2.8s!")
		time.Sleep(10 * time.Millisecond)
		c.Tty().Close()
	}()

	// This should succeed because "Creating" appears before "Sorry" (which never appears)
	_, err = c.Expect(RegexpPattern("Creating .*"))
	if err != nil {
		t.Errorf("Should succeed when expected pattern appears before rejected string but got '%s'", err)
	}

	wg.Wait()
}

func TestRejectStringFailsWhenRejectedStringAppears(t *testing.T) {
	t.Parallel()

	c, err := NewTestConsole(t)
	if err != nil {
		t.Errorf("Expected no error but got'%s'", err)
	}
	defer testCloser(t, c)

	// Set up rejection for "Sorry"
	c.RejectString("Sorry")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.Send("Sorry, that operation failed")
		time.Sleep(10 * time.Millisecond)
		c.Tty().Close()
	}()

	// This should fail immediately when "Sorry" appears
	_, err = c.Expect(RegexpPattern("Creating .*"))
	if err == nil || !strings.Contains(err.Error(), "rejected string \"Sorry\" found in output") {
		t.Errorf("Expected rejection error when 'Sorry' appears but got '%s'", err)
	}

	wg.Wait()
}

func ExampleConsole_echo() {
	c, err := NewConsole(WithStdout(os.Stdout))
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	cmd := exec.Command("echo")
	cmd.Stdin = c.Tty()
	cmd.Stdout = c.Tty()
	cmd.Stderr = c.Tty()

	err = cmd.Start()
	if err != nil {
		log.Fatal(err)
	}

	c.Send("Hello world")
	c.ExpectString("Hello world")
	c.Tty().Close()
	c.ExpectEOF()

	err = cmd.Wait()
	if err != nil {
		log.Fatal(err)
	}

	// Output: Hello world
}
