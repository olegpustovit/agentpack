//go:build linux || darwin

package cli

import (
	"context"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/olegpustovit/agentpack/internal/state"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

type accountPromptWriter struct{ prompts chan struct{} }

func (w accountPromptWriter) Write(data []byte) (int, error) {
	if strings.Contains(strings.ToLower(string(data)), "password: ") {
		w.prompts <- struct{}{}
	}
	return len(data), nil
}

func TestGatewayPasswordEntryThroughTerminal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB.Exec("INSERT INTO users(email,role) VALUES ('owner@example.com','owner')"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		action        string
		args          []string
		first, second string
		ok            bool
	}{
		{"add", []string{"gateway", "user", "add", "member@example.com"}, "a-long-password", "a-long-password", true},
		{"passwd", []string{"gateway", "passwd", "member@example.com"}, "a-different-password", "a-different-password", true},
		{"mismatch", []string{"gateway", "passwd", "member@example.com"}, "a-long-password", "does-not-match", false},
	} {
		t.Run(test.action, func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			original, err := term.GetState(int(slave.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			prompts := make(chan struct{}, 2)
			runner := Runner{Stdin: slave, Stdout: io.Discard, Stderr: accountPromptWriter{prompts}}
			type result struct {
				code int
				err  error
			}
			done := make(chan result, 1)
			go func() { code, err := runner.Execute(context.Background(), test.args); done <- result{code, err} }()
			for _, password := range []string{test.first, test.second} {
				select {
				case <-prompts:
				case <-time.After(time.Second):
					t.Fatal("password prompt missing")
				}
				deadline := time.Now().Add(time.Second)
				for {
					current, err := term.GetState(int(slave.Fd()))
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(original, current) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("terminal echo was not disabled")
					}
					runtime.Gosched()
				}
				if _, err := io.WriteString(master, password+"\n"); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case result := <-done:
				if (result.err == nil && result.code == 0) != test.ok {
					t.Fatalf("result=%+v", result)
				}
			case <-time.After(time.Second):
				t.Fatal("password command did not finish")
			}
			if test.ok {
				var hash string
				if err := store.Reader.QueryRow("SELECT password_hash FROM users WHERE email='member@example.com'").Scan(&hash); err != nil {
					t.Fatal(err)
				}
				if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(test.first)); err != nil {
					t.Fatal("saved password does not match", err)
				}
			}
		})
	}
}
